//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func createCLIAPIKey(t *testing.T, c *cli, name, expires string) controlplane.APIKeyCreated {
	t.Helper()
	var created controlplane.APIKeyCreated
	if err := json.Unmarshal(c.run(nil, "--json", "auth", "keys", "create", name, "--expires", expires), &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.Key.ID == "" || created.Key.Name != name || created.Key.Status != "active" {
		t.Fatal("creation did not return a usable key and its one-time secret")
	}
	return created
}

func TestAPIKeysCLILoginRestartRevocationAndExistingMount(t *testing.T) {
	r := newRedis(t)
	p := newManagementProcess(t, r)
	endpoint := "http://" + p.addr
	admin := newManagedCLI(t, r, endpoint, p.token)
	initialConfig, err := os.ReadFile(admin.config)
	if err != nil {
		t.Fatal(err)
	}
	issued := createCLIAPIKey(t, admin, "build agent", "30d")
	if got, err := os.ReadFile(admin.config); err != nil || !bytes.Equal(got, initialConfig) {
		t.Fatal("key creation modified the administrator's saved credentials")
	}
	agent := newManagedCLI(t, r, endpoint, "")
	authResult(t, agent, []byte(issued.Token+"\n"), []string{issued.Token, p.token}, "login", "--url", endpoint, "--token-stdin")
	assertAuthConfigPrivate(t, agent.config)
	agent.run(nil, "create", "key-owned")
	listing := admin.run(nil, "--json", "auth", "keys", "list")
	assertAuthOutputHasNoSecrets(t, listing, issued.Token, p.token)
	var keys controlplane.APIKeyList
	if err := json.Unmarshal(listing, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 1 || keys.Keys[0].LastUsedAt == "" || keys.Keys[0].ExpiresAt == "" {
		t.Fatal("key usage/expiry was not recorded")
	}
	// The actual processes restart over persisted, test-owned Redis data.
	p.stop()
	r.stop()
	r.start()
	p.start()
	agent.run(nil, "list")
	root := filepath.Join(t.TempDir(), "mounted")
	agent.mount("key-owned", root)
	write(t, filepath.Join(root, "proof"), []byte("before revocation\n"))
	agent.run(nil, "sync", "--wait", root, "--timeout", "20s")
	awaitRemote(t, agent, "key-owned", "proof", []byte("before revocation\n"))
	revoked := admin.run(nil, "--json", "auth", "keys", "revoke", issued.Key.ID)
	assertAuthOutputHasNoSecrets(t, revoked, issued.Token, p.token)
	if !bytes.Contains(revoked, []byte(`"revoked"`)) {
		t.Fatal("revoke did not report revoked status")
	}
	for _, args := range [][]string{{"list"}, {"auth", "keys", "list"}, {"mount", "key-owned", filepath.Join(t.TempDir(), "new-mount")}} {
		out, diagnostics, err := agent.runTimeout(15*time.Second, nil, args...)
		assertAuthOutputHasNoSecrets(t, append(out, diagnostics...), issued.Token, p.token)
		if err == nil {
			t.Fatalf("revoked key still authorized %v", args)
		}
	}
	// API revocation must not claim or accidentally implement a daemon cutoff.
	write(t, filepath.Join(root, "proof"), []byte("existing Redis access continues\n"))
	awaitRemote(t, agent, "key-owned", "proof", []byte("existing Redis access continues\n"))
	agent.unmount(root)
	p.stop()
	p.start()
	agent.mustFail("list")
	admin.run(nil, "list")                                  // Recovery token is independent of named-key revocation.
	admin.run(nil, "auth", "keys", "revoke", issued.Key.ID) // Idempotent.
}

func TestAPIKeysExpiryAndScopedDatabaseUse(t *testing.T) {
	primary, secondary := newRedis(t), newRedis(t)
	p := newManagementProcess(t, primary)
	endpoint := "http://" + p.addr
	admin := newManagedCLI(t, primary, endpoint, p.token)
	issued := createCLIAPIKey(t, admin, "short lived", "3s")
	expires, err := time.Parse(time.RFC3339Nano, issued.Key.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	agent := newManagedCLI(t, primary, endpoint, issued.Token)
	agent.run(nil, "list")
	eventually(t, 8*time.Second, "key expiration", func() bool { return time.Now().After(expires) })
	agent.mustFail("list")
	permanent := createCLIAPIKey(t, admin, "database automation", "never")
	if permanent.Key.ExpiresAt != "" {
		t.Fatal("never key has an expiration")
	}
	var added observedDatabase
	if err := json.Unmarshal(p.request(http.MethodPost, "/v1/databases", map[string]any{"name": "Other Redis", "redis_addr": secondary.addr}), &added); err != nil {
		t.Fatal(err)
	}
	scoped := newManagedCLI(t, secondary, endpoint+"/databases/"+added.ID, permanent.Token)
	scoped.run(nil, "create", "secondary-key-workspace")
	if got := admin.run(nil, "list"); bytes.Contains(got, []byte("secondary-key-workspace")) {
		t.Fatal("scoped creation went to primary Redis")
	}
	listing := scoped.run(nil, "--json", "auth", "keys", "list")
	if !bytes.Contains(listing, []byte(permanent.Key.ID)) || !bytes.Contains(listing, []byte(`"expired"`)) {
		t.Fatal("scoped key list did not use central registry")
	}
	assertAuthOutputHasNoSecrets(t, listing, issued.Token, permanent.Token, p.token)
	// Additional database data must not contain central API-key records.
	var cursor uint64
	for {
		keys, next, err := secondary.client.Scan(context.Background(), cursor, "afs:{management-auth}:*", 100).Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 0 {
			t.Fatal("API-key metadata leaked into an additional database")
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	admin.run(nil, "auth", "keys", "revoke", permanent.Key.ID)
	scoped.mustFail("list")
}
