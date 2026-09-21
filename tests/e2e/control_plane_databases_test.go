//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type observedDatabase struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RedisAddr string `json:"redis_addr"`
	RedisDB   int    `json:"redis_db"`
	IsDefault bool   `json:"is_default"`
}

func (p *managementProcess) databases() []observedDatabase {
	p.t.Helper()
	var result struct {
		Items []observedDatabase `json:"items"`
	}
	if err := json.Unmarshal(p.request(http.MethodGet, "/v1/databases", nil), &result); err != nil {
		p.t.Fatal(err)
	}
	return result.Items
}

func TestControlPlaneAddDatabasePersistsAndIsolatesWorkspaces(t *testing.T) {
	primary, secondary := newRedis(t), newRedis(t)
	p := newManagementProcess(t, primary)
	const username = "database-e2e-user"
	const password = "database-e2e-secret"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := secondary.client.Do(ctx, "ACL", "SETUSER", username, "on", ">"+password, "~*", "&*", "+@all").Err(); err != nil {
		t.Fatal(err)
	}
	status, raw := p.requestStatus(http.MethodPost, "/v1/databases", map[string]any{
		"name": "Added Redis", "description": "Isolated secondary backend", "redis_addr": secondary.addr,
		"redis_username": username, "redis_password": password, "redis_db": 1, "redis_tls": false,
	})
	if status != http.StatusCreated {
		t.Fatalf("add database: status %d: %s", status, raw)
	}
	assertDatabaseSecretsAbsent(t, raw, password)
	var added observedDatabase
	if err := json.Unmarshal(raw, &added); err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || added.ID == "local" || added.Name != "Added Redis" || added.RedisAddr != secondary.addr || added.RedisDB != 1 || added.IsDefault {
		t.Fatalf("unexpected added database: %s", raw)
	}
	assertDatabaseRegistry(t, p, added.ID)
	registry, err := os.ReadFile(p.databasesFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.databasesFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database credentials file permissions: %o, want 600", info.Mode().Perm())
	}
	if !bytes.Contains(registry, []byte(password)) {
		t.Fatal("saved database credentials are missing; authenticated restart cannot work")
	}

	// Identical workspace names must resolve through their selected backend.
	const workspace = "same-name"
	localPath := "/v1/databases/local/workspaces/" + workspace
	addedBase := "/v1/databases/" + added.ID
	addedPath := addedBase + "/workspaces/" + workspace
	for _, base := range []string{"/v1/databases/local", addedBase} {
		p.request(http.MethodPost, base+"/workspaces", map[string]any{"name": workspace})
	}
	p.request(http.MethodPost, addedPath+":save-from-live", map[string]any{"checkpoint_id": "secondary-only", "allow_unchanged": true})
	if status, raw := p.requestStatus(http.MethodGet, localPath+"/checkpoints/secondary-only", nil); status != http.StatusNotFound {
		t.Fatalf("secondary checkpoint leaked to default backend: status %d: %s", status, raw)
	}
	p.request(http.MethodGet, addedPath+"/checkpoints/secondary-only", nil)
	for _, item := range []struct{ path, session string }{{localPath, "local-session"}, {addedPath, "added-session"}} {
		p.request(http.MethodPost, item.path+"/sessions", map[string]any{"session_id": item.session, "agent_name": item.session})
	}
	assertDatabaseWorkspaceAggregation(t, p, added.ID)
	agents := p.request(http.MethodGet, "/v1/agents", nil)
	if !bytes.Contains(agents, []byte("local-session")) || !bytes.Contains(agents, []byte("added-session")) {
		t.Fatalf("root agents list does not include both backends: %s", agents)
	}
	localAgents := p.request(http.MethodGet, "/v1/databases/local/agents", nil)
	if bytes.Contains(localAgents, []byte("added-session")) {
		t.Fatalf("scoped agents list leaked another database: %s", localAgents)
	}
	// Selection of database 1 must never place workspace keys in database 0.
	if size, err := secondary.client.DBSize(ctx).Result(); err != nil || size != 0 {
		t.Fatalf("secondary Redis DB 0 was mutated: size=%d err=%v", size, err)
	}
	secondaryDB := redis.NewClient(&redis.Options{Addr: secondary.addr, Username: username, Password: password, DB: 1})
	t.Cleanup(func() { _ = secondaryDB.Close() })
	if size, err := secondaryDB.DBSize(ctx).Result(); err != nil || size == 0 {
		t.Fatalf("secondary Redis DB 1 missing workspace state: size=%d err=%v", size, err)
	}

	p.stop()
	p.start()
	assertDatabaseRegistry(t, p, added.ID)
	assertDatabaseWorkspaceAggregation(t, p, added.ID)
	p.request(http.MethodGet, addedPath+"/checkpoints/secondary-only", nil)
	for _, path := range []string{"/v1/databases", addedBase, "/v1/workspaces", "/v1/agents"} {
		assertDatabaseSecretsAbsent(t, p.request(http.MethodGet, path, nil), password)
	}
	log, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseSecretsAbsent(t, log, password)
}

func assertDatabaseRegistry(t *testing.T, p *managementProcess, addedID string) {
	t.Helper()
	items := p.databases()
	if len(items) != 2 {
		t.Fatalf("database registry contains %d entries, want 2: %+v", len(items), items)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.ID] = true
		if item.IsDefault != (item.ID == "local") {
			t.Fatalf("unexpected default database selection: %+v", item)
		}
	}
	if !seen["local"] || !seen[addedID] {
		t.Fatalf("database registry lost a backend: %+v", items)
	}
}

func assertDatabaseWorkspaceAggregation(t *testing.T, p *managementProcess, addedID string) {
	t.Helper()
	var result struct {
		Items []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			DatabaseID string `json:"database_id"`
		} `json:"items"`
	}
	raw := p.request(http.MethodGet, "/v1/workspaces", nil)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("root workspace list does not aggregate both backends: %s", raw)
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if item.Name != "same-name" || item.ID == "" {
			t.Fatalf("unexpected aggregated workspace: %s", raw)
		}
		seen[item.DatabaseID] = true
	}
	if !seen["local"] || !seen[addedID] {
		t.Fatalf("aggregated workspace records lost their database identity: %s", raw)
	}
}

func assertDatabaseSecretsAbsent(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("database response or logs disclosed a Redis password")
		}
	}
}

func TestControlPlaneAddDatabaseRejectsFailedConnections(t *testing.T) {
	r, candidate := newRedis(t), newRedis(t)
	p := newManagementProcess(t, r)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Own the unused port so no unrelated Redis can appear during validation.
	defer listener.Close()
	const password = "rejected-database-secret"
	for _, test := range []struct{ name, addr, username string }{
		{"unreachable", listener.Addr().String(), ""},
		{"invalid-credentials", candidate.addr, "nonexistent-database-user"},
	} {
		status, raw := p.requestStatus(http.MethodPost, "/v1/databases", map[string]any{
			"name": test.name, "redis_addr": test.addr, "redis_username": test.username,
			"redis_password": password, "redis_db": 0, "redis_tls": false,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("%s connection should reject the database: status %d: %s", test.name, status, raw)
		}
		assertDatabaseSecretsAbsent(t, raw, password)
		if items := p.databases(); len(items) != 1 || items[0].ID != "local" {
			t.Fatalf("%s connection was retained: %+v", test.name, items)
		}
	}
	p.stop()
	p.start()
	if items := p.databases(); len(items) != 1 || items[0].ID != "local" {
		t.Fatalf("failed connection was persisted: %+v", items)
	}
	log, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseSecretsAbsent(t, log, password)
}

func TestControlPlaneAddedDatabaseManagedCLI(t *testing.T) {
	primary, secondary := newRedis(t), newRedis(t)
	const password = "added-database-managed-secret"
	if err := secondary.client.ConfigSet(context.Background(), "requirepass", password).Err(); err != nil {
		t.Fatal(err)
	}
	_ = secondary.client.Close()
	secondary.client = redis.NewClient(&redis.Options{Addr: secondary.addr, Password: password})
	p := newManagementProcess(t, primary)
	raw := p.request(http.MethodPost, "/v1/databases", map[string]any{
		"name": "CLI Redis", "redis_addr": secondary.addr, "redis_username": "default",
		"redis_password": password, "redis_db": 0, "redis_tls": false,
	})
	var added observedDatabase
	if err := json.Unmarshal(raw, &added); err != nil || added.ID == "" {
		t.Fatalf("added database identity missing: %s err=%v", raw, err)
	}
	endpoint := "http://" + p.addr + "/databases/" + added.ID
	c := newCLI(t, secondary)
	c.managed = true
	c.environment = map[string]string{}
	if err := os.Remove(c.config); err != nil {
		t.Fatal(err)
	}
	result := authResult(t, c, []byte(p.token+"\n"), []string{p.token, password}, "login", "--url", endpoint, "--token-stdin")
	if result["logged_in"] != true || result["url"] != endpoint {
		t.Fatalf("scoped database login: %v", result)
	}
	const workspace = "added-managed-cli"
	c.run(nil, "create", workspace)
	root := filepath.Join(t.TempDir(), "mount")
	c.mount(workspace, root)
	first := []byte("direct I/O to the database selected at login\n")
	write(t, filepath.Join(root, "proof"), first)
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	awaitRemote(t, c, workspace, "proof", first)
	eventually(t, 10*time.Second, "added database sync session is active", func() bool {
		for _, session := range p.sessions() {
			if session.State == "active" && session.WorkspaceID != "" && session.DatabaseID == added.ID {
				return true
			}
		}
		return false
	})
	c.run(nil, "checkpoint", "create", workspace, "--name", "added-cli-checkpoint")
	p.request(http.MethodGet, "/v1/databases/"+added.ID+"/workspaces/"+workspace+"/checkpoints/added-cli-checkpoint", nil)
	if size, err := primary.client.DBSize(context.Background()).Result(); err != nil || size != 0 {
		t.Fatalf("added database CLI touched the default Redis: size=%d err=%v", size, err)
	}
	assertManagedSecretAbsent(t, c, password)
	p.stop()
	second := []byte("direct Redis writes continue with the control plane offline\n")
	write(t, filepath.Join(root, "proof"), second)
	awaitRemote(t, c, workspace, "proof", second)
	c.unmount(root)
	assertManagedSecretAbsent(t, c, password)
}
