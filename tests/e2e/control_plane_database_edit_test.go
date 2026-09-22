//go:build integration

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"
)

type observedDatabaseSettings struct {
	observedDatabase
	Description    string `json:"description"`
	RedisUsername  string `json:"redis_username"`
	RedisTLS       bool   `json:"redis_tls"`
	HasPassword    bool   `json:"has_password"`
	ConfigRevision string `json:"config_revision"`
}

func databaseSettings(t *testing.T, p *managementProcess, id string) observedDatabaseSettings {
	t.Helper()
	return decodeDatabaseSettings(t, p.request(http.MethodGet, "/v1/databases/"+id, nil))
}

func decodeDatabaseSettings(t *testing.T, raw []byte) observedDatabaseSettings {
	t.Helper()
	var settings observedDatabaseSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if settings.ID == "" || settings.ConfigRevision == "" || fields["has_password"] == nil || fields["redis_username"] == nil {
		t.Fatalf("database settings omit identity, revision, username, or password state: %s", raw)
	}
	if fields["redis_password"] != nil {
		t.Fatal("database settings response exposes a password field")
	}
	return settings
}

func (settings observedDatabaseSettings) update() map[string]any {
	return map[string]any{
		"name": settings.Name, "description": settings.Description,
		"redis_addr": settings.RedisAddr, "redis_username": settings.RedisUsername,
		"redis_db": settings.RedisDB, "redis_tls": settings.RedisTLS,
		"config_revision": settings.ConfigRevision,
	}
}

func readDatabaseSettingsFile(t *testing.T, p *managementProcess) []byte {
	t.Helper()
	filename := p.databasesFile + ".sqlite"
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata database permissions: %o, want 600", info.Mode().Perm())
	}
	// Query active rows through SQLite, including committed WAL contents. Raw
	// file bytes may retain superseded credentials in freed pages after an edit.
	dsn := &url.URL{Scheme: "file", Path: filename}
	query := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}}
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var snapshot struct {
		DefaultDatabaseID string            `json:"default_database_id"`
		Profiles          []json.RawMessage `json:"profiles"`
	}
	if err := tx.QueryRow("SELECT value FROM metadata_settings WHERE name = 'default_database_id'").Scan(&snapshot.DefaultDatabaseID); err != nil {
		t.Fatalf("read persisted default/initialization marker: %v", err)
	}
	rows, err := tx.Query("SELECT id, profile FROM database_profiles ORDER BY position, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	snapshot.Profiles = []json.RawMessage{}
	defaultFound := false
	for rows.Next() {
		var id, profile string
		if err := rows.Scan(&id, &profile); err != nil {
			t.Fatal(err)
		}
		snapshot.Profiles = append(snapshot.Profiles, json.RawMessage(profile))
		defaultFound = defaultFound || id == snapshot.DefaultDatabaseID
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Profiles) == 0 && snapshot.DefaultDatabaseID != "" || len(snapshot.Profiles) != 0 && !defaultFound {
		t.Fatal("persisted default does not match the active connection registry")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestControlPlaneEditAddedDatabasePersistsCredentialsAndIdentity(t *testing.T) {
	primary, secondary := newRedis(t), newRedis(t)
	p := newManagementProcess(t, primary)
	const username, password = "editable-user", "editable-original-secret"
	const nextUsername, nextPassword = "replacement-user", "editable-replacement-secret"
	ctx := context.Background()
	for _, credentials := range [][2]string{{username, password}, {nextUsername, nextPassword}} {
		if err := secondary.client.Do(ctx, "ACL", "SETUSER", credentials[0], "on", ">"+credentials[1], "~*", "&*", "+@all").Err(); err != nil {
			t.Fatal(err)
		}
	}
	created := p.request(http.MethodPost, "/v1/databases", map[string]any{
		"name": "Editable Redis", "description": "Original description", "redis_addr": secondary.addr,
		"redis_username": username, "redis_password": password, "redis_db": 0, "redis_tls": false,
	})
	assertDatabaseSecretsAbsent(t, created, password)
	added := decodeDatabaseSettings(t, created)
	if !added.HasPassword || added.RedisUsername != username || added.IsDefault {
		t.Fatalf("added database settings do not describe credentials: %+v", added)
	}
	base := "/v1/databases/" + added.ID
	update := added.update()
	update["name"], update["description"], update["redis_db"] = "Renamed Redis", "Edited description", 1
	edited := decodeDatabaseSettings(t, p.request(http.MethodPut, base, update))
	if edited.ID != added.ID || edited.IsDefault || edited.Name != "Renamed Redis" || edited.Description != "Edited description" || edited.RedisDB != 1 || edited.RedisAddr != secondary.addr || edited.RedisUsername != username || !edited.HasPassword || edited.ConfigRevision == added.ConfigRevision {
		t.Fatalf("edit failed to preserve identity and credentials or apply settings: %+v", edited)
	}
	assertDatabaseRegistry(t, p, added.ID)
	if saved := readDatabaseSettingsFile(t, p); !bytes.Contains(saved, []byte(password)) {
		t.Fatal("omitting the password erased the saved credential")
	}
	p.request(http.MethodPost, base+"/workspaces", map[string]any{"name": "edited-database-workspace"})
	if size, err := secondary.client.DBSize(ctx).Result(); err != nil || size != 0 {
		t.Fatalf("edit kept writing to previous database index: size=%d err=%v", size, err)
	}
	db1 := redis.NewClient(&redis.Options{Addr: secondary.addr, Username: username, Password: password, DB: 1})
	t.Cleanup(func() { _ = db1.Close() })
	if size, err := db1.DBSize(ctx).Result(); err != nil || size == 0 {
		t.Fatalf("edited database index has no workspace data: size=%d err=%v", size, err)
	}
	p.stop()
	p.start()
	if got := databaseSettings(t, p, added.ID); got != edited {
		t.Fatalf("settings changed across restart: got %+v, want %+v", got, edited)
	}
	p.request(http.MethodGet, base+"/workspaces/edited-database-workspace", nil)
	beforeStaleEdit := readDatabaseSettingsFile(t, p)
	if status, raw := p.requestStatus(http.MethodPut, base, added.update()); status != http.StatusConflict {
		t.Fatalf("editing with the previously valid revision returned %d, want 409: %s", status, raw)
	}
	if got := databaseSettings(t, p, added.ID); got != edited || !bytes.Equal(beforeStaleEdit, readDatabaseSettingsFile(t, p)) {
		t.Fatal("stale browser settings overwrote a successfully saved edit")
	}
	for _, path := range []string{"/v1/databases", base} {
		assertDatabaseSecretsAbsent(t, p.request(http.MethodGet, path, nil), password)
	}

	update = edited.update()
	update["redis_username"], update["redis_password"] = nextUsername, nextPassword
	replaced := decodeDatabaseSettings(t, p.request(http.MethodPut, base, update))
	if replaced.RedisUsername != nextUsername || !replaced.HasPassword || replaced.ConfigRevision == edited.ConfigRevision {
		t.Fatalf("replacement credentials not recorded: %+v", replaced)
	}
	if saved := readDatabaseSettingsFile(t, p); bytes.Contains(saved, []byte(password)) || !bytes.Contains(saved, []byte(nextPassword)) {
		t.Fatal("saved database credentials were not replaced")
	}
	if err := secondary.client.Do(ctx, "ACL", "DELUSER", username).Err(); err != nil {
		t.Fatal(err)
	}
	p.stop()
	p.start()
	if got := databaseSettings(t, p, added.ID); got != replaced {
		t.Fatalf("replacement settings changed across restart: got %+v, want %+v", got, replaced)
	}
	p.request(http.MethodGet, base+"/workspaces/edited-database-workspace", nil)
	for _, path := range []string{"/v1/databases", base} {
		assertDatabaseSecretsAbsent(t, p.request(http.MethodGet, path, nil), password, nextPassword)
	}
	update = replaced.update()
	update["redis_username"], update["redis_password"] = "", ""
	cleared := decodeDatabaseSettings(t, p.request(http.MethodPut, base, update))
	if cleared.RedisUsername != "" || cleared.HasPassword || cleared.ConfigRevision == replaced.ConfigRevision {
		t.Fatalf("explicit empty password did not clear authentication: %+v", cleared)
	}
	assertDatabaseSecretsAbsent(t, readDatabaseSettingsFile(t, p), password, nextPassword)
	if err := secondary.client.Do(ctx, "ACL", "DELUSER", nextUsername).Err(); err != nil {
		t.Fatal(err)
	}
	p.stop()
	p.start()
	if got := databaseSettings(t, p, added.ID); got != cleared {
		t.Fatalf("cleared credentials returned after restart: got %+v, want %+v", got, cleared)
	}
	p.request(http.MethodGet, base+"/workspaces/edited-database-workspace", nil)
	for _, path := range []string{"/v1/databases", base, "/v1/workspaces", "/v1/agents"} {
		assertDatabaseSecretsAbsent(t, p.request(http.MethodGet, path, nil), password, nextPassword)
	}
	log, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseSecretsAbsent(t, log, password, nextPassword)
}

func TestControlPlaneEditDatabaseRejectsWithoutChangingActiveOrSavedSettings(t *testing.T) {
	primary, secondary := newRedis(t), newRedis(t)
	p := newManagementProcess(t, primary)
	added := decodeDatabaseSettings(t, p.request(http.MethodPost, "/v1/databases", map[string]any{
		"name": "Editable Redis", "redis_addr": secondary.addr, "redis_db": 0, "redis_tls": false,
	}))
	local := databaseSettings(t, p, "local")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	const invalidPassword = "rejected-edit-secret"
	for _, test := range []struct {
		name   string
		status int
		fields map[string]any
	}{
		{"unreachable", http.StatusBadRequest, map[string]any{"redis_addr": listener.Addr().String()}},
		{"invalid-credentials", http.StatusBadRequest, map[string]any{"redis_username": "nonexistent-user", "redis_password": invalidPassword}},
		{"duplicate-name", http.StatusConflict, map[string]any{"name": local.Name}},
		{"duplicate-endpoint", http.StatusConflict, map[string]any{"redis_addr": primary.addr}},
		{"stale-revision", http.StatusConflict, map[string]any{"name": "Stale edit", "config_revision": "obsolete-config-revision"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := readDatabaseSettingsFile(t, p)
			update := added.update()
			for key, value := range test.fields {
				update[key] = value
			}
			status, raw := p.requestStatus(http.MethodPut, "/v1/databases/"+added.ID, update)
			if status != test.status {
				t.Fatalf("rejected edit returned status %d, want %d: %s", status, test.status, raw)
			}
			assertDatabaseSecretsAbsent(t, raw, invalidPassword)
			if got := databaseSettings(t, p, added.ID); got != added {
				t.Fatalf("rejected edit changed live settings: got %+v, want %+v", got, added)
			}
			if after := readDatabaseSettingsFile(t, p); !bytes.Equal(before, after) {
				t.Fatal("rejected edit changed the saved database registry")
			}
		})
	}
	p.request(http.MethodPost, "/v1/databases/"+added.ID+"/workspaces", map[string]any{"name": "after-rejected-edits"})
	if size, err := primary.client.DBSize(context.Background()).Result(); err != nil || size != 0 {
		t.Fatalf("rejected edit redirected workspace writes: size=%d err=%v", size, err)
	}
	p.stop()
	p.start()
	if got := databaseSettings(t, p, added.ID); got != added {
		t.Fatalf("rejected edit persisted after restart: got %+v, want %+v", got, added)
	}
	p.request(http.MethodGet, "/v1/databases/"+added.ID+"/workspaces/after-rejected-edits", nil)
}

func TestControlPlaneEditDefaultDatabasePersistsAndRoutesManagedCLI(t *testing.T) {
	primary, replacement := newRedis(t), newRedis(t)
	const username, password = "new-default-user", "new-default-secret"
	if err := replacement.client.Do(context.Background(), "ACL", "SETUSER", username, "on", ">"+password, "~*", "&*", "+@all").Err(); err != nil {
		t.Fatal(err)
	}
	p := newManagementProcess(t, primary)
	local := databaseSettings(t, p, "local")
	update := local.update()
	update["name"], update["description"] = "Updated primary", "Default connection edited through its database entry"
	update["redis_addr"], update["redis_username"], update["redis_password"] = replacement.addr, username, password
	edited := decodeDatabaseSettings(t, p.request(http.MethodPut, "/v1/databases/local", update))
	if edited.ID != "local" || !edited.IsDefault || edited.RedisAddr != replacement.addr || edited.RedisUsername != username || !edited.HasPassword || edited.ConfigRevision == local.ConfigRevision {
		t.Fatalf("default edit lost identity or ignored settings: %+v", edited)
	}
	readDatabaseSettingsFile(t, p)
	for phase := 0; phase < 2; phase++ {
		if phase == 1 {
			if size, err := primary.client.DBSize(context.Background()).Result(); err != nil || size != 0 {
				t.Fatalf("default edit kept routing commands to old Redis: size=%d err=%v", size, err)
			}
			p.stop()
			primary.stop()
			// The startup flag still names the original endpoint. A persisted edit
			// must replace it before startup readiness checks or CLI bootstrap.
			p.start()
		}
		if got := databaseSettings(t, p, "local"); got != edited {
			t.Fatalf("default settings changed in phase %d: got %+v, want %+v", phase, got, edited)
		}
		items := p.databases()
		if len(items) != 1 || items[0].ID != "local" || !items[0].IsDefault {
			t.Fatalf("default edit duplicated or replaced database identity: %+v", items)
		}
		for _, path := range []string{"/v1/connection", "/v1/databases/local/connection", "/databases/local/v1/connection"} {
			var bootstrap struct {
				RedisURL string `json:"redis_url"`
			}
			if err := json.Unmarshal(p.request(http.MethodGet, path, nil), &bootstrap); err != nil {
				t.Fatal(err)
			}
			connection, err := url.Parse(bootstrap.RedisURL)
			if err != nil {
				t.Fatal("invalid bootstrap Redis URL")
			}
			bootPassword, _ := connection.User.Password()
			if connection.Host != replacement.addr || connection.User.Username() != username || bootPassword != password || connection.Path != "/0" {
				t.Fatalf("%s returned stale bootstrap settings in phase %d", path, phase)
			}
		}
		for index, endpoint := range []string{"http://" + p.addr, "http://" + p.addr + "/databases/local"} {
			c := newManagedCLI(t, replacement, endpoint, p.token)
			workspace := fmt.Sprintf("edited-primary-%d-%d", phase, index)
			c.run(nil, "create", workspace)
			root := filepath.Join(t.TempDir(), "mount")
			c.mount(workspace, root)
			data := []byte("direct Redis writes use the edited default connection\n")
			write(t, filepath.Join(root, "proof"), data)
			c.run(nil, "sync", "--wait", root, "--timeout", "20s")
			awaitRemote(t, c, workspace, "proof", data)
			c.unmount(root)
			p.request(http.MethodGet, "/v1/databases/local/workspaces/"+workspace, nil)
			assertManagedSecretAbsent(t, c, password)
		}
	}
	assertDatabaseSecretsAbsent(t, p.request(http.MethodGet, "/v1/databases", nil), password)
	log, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseSecretsAbsent(t, log, password)
}
