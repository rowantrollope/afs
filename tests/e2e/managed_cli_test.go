//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
)

// The client starts with only a control-plane URL and its team token. In
// particular, no test helper supplies --redis or quietly fills in its config.
func newManagedCLI(t *testing.T, r *redisServer, endpoint, token string) *cli {
	t.Helper()
	c := newCLI(t, r)
	c.managed = true
	c.environment = map[string]string{}
	writeManagedCLIConfig(t, c, endpoint, token, "")
	return c
}

func writeManagedCLIConfig(t *testing.T, c *cli, endpoint, token, savedRedis string) {
	t.Helper()
	config := map[string]any{"controlPlane": map[string]string{"url": endpoint, "token": token}}
	if savedRedis != "" {
		config["redis"] = savedRedis
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCLIURLOnlyWorkflow(t *testing.T) {
	r := newRedis(t)
	p := newManagementProcess(t, r)
	c := newManagedCLI(t, r, "http://"+p.addr, p.token)
	initialConfig, err := os.ReadFile(c.config)
	if err != nil {
		t.Fatal(err)
	}
	runJSON := func(args ...string) []byte {
		t.Helper()
		out := c.run(nil, append([]string{"--json"}, args...)...)
		jsonValue(t, out)
		return out
	}
	jsonEqual(t, runJSON("list"), []any{})
	created := jsonValue(t, runJSON("create", "managed-contract")).(map[string]any)
	workspaceID, _ := created["id"].(string)
	if created["name"] != "managed-contract" || workspaceID == "" {
		t.Fatalf("managed create identity: %v", created)
	}
	jsonEqual(t, runJSON("info", "managed-contract"), created)
	jsonEqual(t, runJSON("list"), []any{created})

	// Import must read the caller's directory and preserve arbitrary bytes,
	// modes, symlinks, and empty files through the HTTP management path.
	importRoot := t.TempDir()
	imported := []byte{0, 255, 128, '\n', 'i'}
	write(t, filepath.Join(importRoot, "nested", "binary"), imported)
	if err := os.Chmod(filepath.Join(importRoot, "nested", "binary"), 0o640); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(importRoot, "empty"), nil)
	if err := os.Symlink("nested/binary", filepath.Join(importRoot, "link")); err != nil {
		t.Fatal(err)
	}
	runJSON("create", "managed-import", "--from", importRoot)
	importMount := filepath.Join(t.TempDir(), "import-mount")
	c.mount("managed-import", importMount)
	awaitFile(t, filepath.Join(importMount, "nested", "binary"), imported)
	awaitFile(t, filepath.Join(importMount, "empty"), nil)
	if info, err := os.Stat(filepath.Join(importMount, "nested", "binary")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("managed import mode: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(importMount, "link")); err != nil || target != "nested/binary" {
		t.Fatalf("managed import symlink: %q %v", target, err)
	}
	c.unmount(importMount)

	policy := jsonValue(t, runJSON("history", "policy", "managed-contract", "--mode", "all")).(map[string]any)
	if policy["policy"].(map[string]any)["mode"] != "all" {
		t.Fatalf("managed policy was not saved: %v", policy)
	}
	root := filepath.Join(t.TempDir(), "mount")
	c.mount("managed-contract", root)
	first := []byte{0, 255, 128, '\n', 'a'}
	write(t, filepath.Join(root, "file"), first)
	if err := os.Chmod(filepath.Join(root, "file"), 0o640); err != nil {
		t.Fatal(err)
	}
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	awaitRemote(t, c, "managed-contract", "file", first)
	runJSON("cp", "create", "managed-contract", "--name", "first")
	var history controlplane.FileHistoryResponse
	if err := json.Unmarshal(runJSON("history", "list", "managed-contract", "file"), &history); err != nil {
		t.Fatal(err)
	}
	lineage := historyLineage(t, history, "")
	if len(lineage.Versions) == 0 {
		t.Fatal("managed write has no recorded history")
	}
	firstVersion := lineage.Versions[0].VersionID
	second := []byte("second checkpoint\n")
	write(t, filepath.Join(root, "file"), second)
	// This command must drain the local daemon before asking the server to
	// checkpoint, preserving the standalone CLI's ordinary workflow.
	runJSON("cp", "create", "managed-contract", "--name", "second")
	awaitRemote(t, c, "managed-contract", "file", second)
	shown := runJSON("cp", "show", "managed-contract", "second")
	if !bytes.Contains(shown, []byte("manifest")) {
		t.Fatalf("managed checkpoint show omitted manifest: %s", shown)
	}
	listed := runJSON("cp", "list", "managed-contract")
	if !bytes.Contains(listed, []byte("first")) || !bytes.Contains(listed, []byte("second")) {
		t.Fatalf("managed checkpoints missing: %s", listed)
	}
	exported := filepath.Join(t.TempDir(), "historical-file")
	c.run(nil, "history", "export", "managed-contract", "file", "--version", firstVersion, "--to", exported)
	if got, err := os.ReadFile(exported); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("managed historical export: %q %v", got, err)
	}
	if info, err := os.Stat(exported); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("managed historical export mode: %v %v", info, err)
	}
	c.unmount(root)
	runJSON("fork", "managed-contract", "managed-fork", "--checkpoint", "first")
	if got := c.published("managed-fork", "file"); !bytes.Equal(got, first) {
		t.Fatalf("managed fork ignored checkpoint: %q", got)
	}
	runJSON("cp", "restore", "managed-contract", "first", "--yes")
	if got := c.published("managed-contract", "file"); !bytes.Equal(got, first) {
		t.Fatalf("managed restore ignored checkpoint: %q", got)
	}
	runJSON("cp", "delete", "managed-contract", "second", "--yes")
	for _, workspace := range []string{"managed-contract", "managed-fork", "managed-import"} {
		runJSON("delete", workspace, "--yes")
	}
	jsonEqual(t, runJSON("list"), []any{})
	if saved, err := os.ReadFile(c.config); err != nil || !bytes.Equal(saved, initialConfig) {
		t.Fatalf("managed commands changed the URL-only configuration: %s %v", saved, err)
	}
}

func TestManagedCLIServerCredentialsAndControlPlaneOutage(t *testing.T) {
	const password = "managed-bootstrap-secret:@/with-specials"
	r := newRedis(t)
	if err := r.client.ConfigSet(context.Background(), "requirepass", password).Err(); err != nil {
		t.Fatal(err)
	}
	_ = r.client.Close()
	r.client = redis.NewClient(&redis.Options{Addr: r.addr, Password: password})
	endpoint, err := url.Parse(r.url())
	if err != nil {
		t.Fatal(err)
	}
	endpoint.User = url.UserPassword("default", password)
	p := newManagementProcess(t, r, endpoint.String())
	wrongRedis := newRedis(t)
	c := newManagedCLI(t, r, "http://"+p.addr, p.token)
	writeManagedCLIConfig(t, c, "http://"+p.addr, p.token, wrongRedis.url())
	c.environment["AFS_REDIS_URL"] = wrongRedis.url()
	c.environment["AFS_REDIS_PASSWORD"] = "stale-local-password"
	c.run(nil, "create", "server-credentials")
	root := filepath.Join(t.TempDir(), "mount")
	c.mount("server-credentials", root)
	first := []byte("authenticated with the server-supplied Redis password\n")
	write(t, filepath.Join(root, "proof"), first)
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	awaitRemote(t, c, "server-credentials", "proof", first)
	assertManagedSecretAbsent(t, c, password)

	p.stop()
	second := []byte("existing direct Redis I/O survives control-plane outage\n")
	write(t, filepath.Join(root, "proof"), second)
	awaitRemote(t, c, "server-credentials", "proof", second)
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	c.mustFail("list")
	c.mustFail("create", "must-not-fallback")
	c.mustFail("mount", "server-credentials", filepath.Join(t.TempDir(), "offline-mount"))
	third := []byte("unmount also drains pending data while control plane is offline\n")
	write(t, filepath.Join(root, "proof"), third)
	c.unmount(root)
	if got := c.published("server-credentials", "proof"); !bytes.Equal(got, third) {
		t.Fatalf("offline unmount failed to publish final bytes: %q", got)
	}
	assertManagedSecretAbsent(t, c, password)
	if size, err := wrongRedis.client.DBSize(context.Background()).Result(); err != nil || size != 0 {
		t.Fatalf("managed command touched stale local Redis: size=%d err=%v", size, err)
	}

	// An explicit --redis opts this invocation into standalone operation even
	// with a saved control-plane URL that is currently unavailable.
	c.environment["AFS_REDIS_PASSWORD"] = ""
	c.run(nil, "--redis", wrongRedis.url(), "create", "explicit-standalone")
	listed := c.run(nil, "--redis", wrongRedis.url(), "--json", "list")
	if !bytes.Contains(listed, []byte("explicit-standalone")) || bytes.Contains(listed, []byte("server-credentials")) {
		t.Fatalf("explicit standalone command selected wrong backend: %s", listed)
	}
}

func assertManagedSecretAbsent(t *testing.T, c *cli, secret string) {
	t.Helper()
	secrets := [][]byte{[]byte(secret), []byte(url.QueryEscape(secret)), []byte(url.PathEscape(secret)), []byte(url.UserPassword("", secret).String())}
	check := func(where string, raw []byte) {
		t.Helper()
		for _, candidate := range secrets {
			if bytes.Contains(raw, candidate) {
				t.Errorf("server Redis credential leaked into %s", where)
			}
		}
	}
	for _, base := range []string{c.config, c.state} {
		if err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err == nil {
				check(path, raw)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, flags := range [][]string{{"status"}, {"--json", "status"}} {
		out, diagnostic, err := c.runTimeout(10*time.Second, nil, flags...)
		if err != nil {
			t.Fatalf("status after managed bootstrap: %v %s", err, diagnostic)
		}
		check("status output", out)
		check("status diagnostics", diagnostic)
	}
}

func TestManagedCLIControlPlaneErrorsNeverFallBack(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			r := newRedis(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":"isolated control-plane rejection"}`))
			}))
			defer server.Close()
			c := newManagedCLI(t, r, server.URL, "rejected-token")
			writeManagedCLIConfig(t, c, server.URL, "rejected-token", r.url())
			c.mustFail("list")
			c.mustFail("create", "must-not-fallback")
			c.mustFail("mount", "must-not-fallback", filepath.Join(t.TempDir(), "mount"))
			if size, err := r.client.DBSize(context.Background()).Result(); err != nil || size != 0 {
				t.Fatalf("control-plane error fell back to saved Redis: size=%d err=%v", size, err)
			}
		})
	}
}
