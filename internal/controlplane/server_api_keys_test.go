package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func apiKeyTestCreate(t *testing.T, h http.Handler, name string) APIKeyCreated {
	t.Helper()
	r := serverTestCall(t, h, "POST", "/v1/api-keys", map[string]any{"name": name})
	if r.Code != 200 {
		t.Fatalf("create API key: %d %s", r.Code, r.Body.String())
	}
	var created APIKeyCreated
	if err := json.Unmarshal(r.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.Key.Status != "active" {
		t.Fatalf("invalid creation: %+v", created)
	}
	return created
}
func apiKeyTestCall(t *testing.T, h http.Handler, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return historyHTTPCall(t, h, method, path, body, map[string]string{"Authorization": "Bearer " + token})
}

func TestAPIKeyLifecyclePersistenceAndSecretStorage(t *testing.T) {
	s, rdb := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	created := apiKeyTestCreate(t, h, "  CI deploy  ")
	if created.Key.Name != "CI deploy" {
		t.Fatalf("name not trimmed: %+v", created.Key)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, created.Key.CreatedAt)
	expires, _ := time.Parse(time.RFC3339Nano, created.Key.ExpiresAt)
	if expires.Sub(createdAt) != 30*24*time.Hour {
		t.Fatalf("default expiry: %+v", created.Key)
	}
	record := rdb.HGetAll(context.Background(), apiKeyRecord(created.Key.ID)).Val()
	encoded, _ := json.Marshal(record)
	if record["hash"] != apiKeyHash(created.Token) || strings.Contains(string(encoded), created.Token) || strings.Contains(string(encoded), strings.Split(created.Token, ".")[1]) {
		t.Fatalf("storage should contain only token digest: %s", encoded)
	}
	// A replacement process/handler uses the persisted key without in-memory state.
	h = NewHandler(NewService(NewStore(rdb)), HandlerOptions{AuthToken: "test-secret"})
	auth := serverTestJSON(t, apiKeyTestCall(t, h, created.Token, "GET", "/v1/auth/config", nil))
	user := auth["user"].(map[string]any)
	if user["subject"] != "api-key:"+created.Key.ID || user["name"] != created.Key.Name {
		t.Fatalf("identity: %v", user)
	}
	listed := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/api-keys", nil))
	if strings.Contains(fmt.Sprint(listed), created.Token) || strings.Contains(fmt.Sprint(listed), record["hash"]) {
		t.Fatalf("list revealed secret: %v", listed)
	}
	row := listed["keys"].([]any)[0].(map[string]any)
	if row["last_used_at"] == nil || row["status"] != "active" {
		t.Fatalf("last-used not touched: %v", row)
	}
	for _, token := range []string{"", "invalid", created.Token + "x", strings.Replace(created.Token, ".", ".A", 1), created.Token[:len(created.Token)-1] + "!"} {
		if r := apiKeyTestCall(t, h, token, "GET", "/v1/workspaces", nil); r.Code != 401 {
			t.Fatalf("bad token accepted: %d", r.Code)
		}
	}
	revoked := serverTestJSON(t, serverTestCall(t, h, "DELETE", "/v1/api-keys/"+created.Key.ID, nil))["key"].(map[string]any)
	if revoked["status"] != "revoked" || revoked["revoked_at"] == "" {
		t.Fatalf("revoke: %v", revoked)
	}
	if r := apiKeyTestCall(t, h, created.Token, "GET", "/v1/workspaces", nil); r.Code != 401 {
		t.Fatalf("revoked token accepted: %d", r.Code)
	}
	if r := serverTestCall(t, h, "GET", "/v1/workspaces", nil); r.Code != 200 {
		t.Fatalf("bootstrap token stopped working: %d", r.Code)
	}
	again := serverTestJSON(t, serverTestCall(t, h, "DELETE", "/v1/api-keys/"+created.Key.ID, nil))["key"].(map[string]any)
	if again["revoked_at"] != revoked["revoked_at"] {
		t.Fatal("idempotent revoke changed timestamp")
	}
}

func TestAPIKeyExpiryDisabledModeAndValidation(t *testing.T) {
	s, rdb := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	for _, input := range []map[string]any{
		{"name": ""}, {"name": "  "}, {"name": strings.Repeat("x", 129)}, {"name": "a\nb"},
		{"name": "ci", "expires_at": "yesterday"}, {"name": "ci", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}, {"name": "ci", "unknown": true},
	} {
		if r := serverTestCall(t, h, "POST", "/v1/api-keys", input); r.Code != 400 {
			t.Fatalf("bad input accepted: %v, %d %s", input, r.Code, r.Body.String())
		}
	}
	noExpiry := serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/api-keys", map[string]any{"name": "service", "expires_at": ""}))
	if noExpiry["key"].(map[string]any)["expires_at"] != nil {
		t.Fatalf("unexpected expiry: %v", noExpiry)
	}
	created := apiKeyTestCreate(t, h, "expires")
	now := time.Now()
	if err := rdb.HSet(context.Background(), apiKeyRecord(created.Key.ID), "expires_ms", now.Add(-time.Second).UnixMilli(), "expires_at", serverTime(now.Add(-time.Second))).Err(); err != nil {
		t.Fatal(err)
	}
	if r := apiKeyTestCall(t, h, created.Token, "GET", "/v1/workspaces", nil); r.Code != 401 {
		t.Fatalf("expired key accepted: %d", r.Code)
	}
	list, _, err := s.store.listAPIKeys(context.Background(), "", 100, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range list {
		if key.ID == created.Key.ID && key.Status != "expired" {
			t.Fatalf("expiry status: %+v", key)
		}
	}
	// A malformed expiry fails closed even if its token digest is valid.
	if err := rdb.HDel(context.Background(), apiKeyRecord(created.Key.ID), "expires_ms").Err(); err != nil {
		t.Fatal(err)
	}
	if r := apiKeyTestCall(t, h, created.Token, "GET", "/v1/workspaces", nil); r.Code != 401 {
		t.Fatalf("corrupt key accepted: %d", r.Code)
	}
	h = NewHandler(s, HandlerOptions{})
	listDisabled := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/api-keys", nil))
	if listDisabled["enabled"] != false || len(listDisabled["keys"].([]any)) != 0 {
		t.Fatalf("disabled mode: %v", listDisabled)
	}
	for _, request := range []struct {
		method, path string
		body         any
	}{{"POST", "/v1/api-keys", map[string]string{"name": "no"}}, {"DELETE", "/v1/api-keys/" + created.Key.ID, nil}} {
		if r := serverTestCall(t, h, request.method, request.path, request.body); r.Code != 409 {
			t.Fatalf("disabled mutation: %d", r.Code)
		}
	}
	// Stored keys cannot activate authentication on a server without its team token.
	auth := serverTestJSON(t, apiKeyTestCall(t, h, noExpiry["token"].(string), "GET", "/v1/auth/config", nil))
	if auth["user"].(map[string]any)["subject"] != "operator" {
		t.Fatalf("key used while disabled: %v", auth)
	}
}

func TestAPIKeyPagingRevocationRaceAndUnavailableStore(t *testing.T) {
	s, rdb := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	created := apiKeyTestCreate(t, h, "race")
	for i := 0; i < 4; i++ {
		apiKeyTestCreate(t, h, fmt.Sprintf("key %d", i))
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/api-keys?limit=2&cursor="+cursor, nil))
		keys := page["keys"].([]any)
		if len(keys) > 2 {
			t.Fatal("unbounded page")
		}
		for _, value := range keys {
			id := value.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate pagination record")
			}
			seen[id] = true
		}
		cursor, _ = page["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("lost keys: %v", seen)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = s.store.authenticateAPIKey(context.Background(), created.Token, time.Now())
		}()
	}
	if _, err := s.store.revokeAPIKey(context.Background(), created.Key.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		_, valid, err := s.store.authenticateAPIKey(context.Background(), created.Token, time.Now())
		if err != nil || valid {
			t.Fatalf("revocation overwritten: valid=%v err=%v", valid, err)
		}
	}
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	if r := apiKeyTestCall(t, h, created.Token, "GET", "/v1/workspaces", nil); r.Code != 503 || strings.Contains(r.Body.String(), rdb.Options().Addr) {
		t.Fatalf("failed-open or exposed storage: %d %s", r.Code, r.Body.String())
	}
	if r := serverTestCall(t, h, "GET", "/v1/api-keys", nil); r.Code != 503 {
		t.Fatalf("unavailable list: %d", r.Code)
	}
}

func TestAPIKeyIdentityCannotBeSpoofedBySessionLabels(t *testing.T) {
	s, rdb := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	created := apiKeyTestCreate(t, h, "CI runner")
	workspace := serverTestJSON(t, apiKeyTestCall(t, h, created.Token, "POST", "/v1/workspaces", map[string]any{"name": "attributed"}))
	id := workspace["id"].(string)
	registration := serverTestJSON(t, apiKeyTestCall(t, h, created.Token, "POST", "/v1/workspaces/"+id+"/sessions", map[string]any{"user": "someone-else", "agent_name": "Label", "label": "helpful"}))
	if registration["user"] != "someone-else" || registration["auth_subject"] != "api-key:"+created.Key.ID || registration["api_key_id"] != created.Key.ID {
		t.Fatalf("session identity conflated: %v", registration)
	}
	rows := rdb.XRange(context.Background(), managementEventsKey, "-", "+").Val()
	var workspaceEvent, sessionEvent bool
	for _, row := range rows {
		var event serverEvent
		if err := json.Unmarshal([]byte(fmt.Sprint(row.Values["event"])), &event); err != nil {
			t.Fatal(err)
		}
		if event.WorkspaceID != id {
			continue
		}
		if event.Actor != "api-key:"+created.Key.ID || event.APIKeyID != created.Key.ID || event.APIKeyName != "CI runner" {
			t.Fatalf("unattributed API event: %+v", event)
		}
		workspaceEvent = workspaceEvent || event.Kind == "workspace"
		sessionEvent = sessionEvent || event.Kind == "session"
	}
	if !workspaceEvent || !sessionEvent {
		t.Fatalf("missing attribution events: workspace=%v session=%v", workspaceEvent, sessionEvent)
	}
}

func TestAPIKeyMetadataIsolationAcrossDatabaseEditsAndRestart(t *testing.T) {
	s, rootRedis := serviceFixture(t)
	filename := filepath.Join(t.TempDir(), "databases.json")
	h, err := NewDatabaseHandler(s, HandlerOptions{AuthToken: "test-secret"}, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	rootKey := apiKeyTestCreate(t, h, "root")
	secondary := miniredis.RunT(t)
	added := serverTestCall(t, h, "POST", "/v1/databases", map[string]any{"name": "Secondary", "redis_addr": secondary.Addr()})
	if added.Code != 201 {
		t.Fatalf("add database: %d %s", added.Code, added.Body.String())
	}
	var db map[string]any
	if err := json.Unmarshal(added.Body.Bytes(), &db); err != nil {
		t.Fatal(err)
	}
	id := db["id"].(string)
	// An independently minted token in the added Redis must never grant root access.
	foreignHandler := NewHandler(h.lookup(id).service, HandlerOptions{AuthToken: "test-secret"})
	foreignKey := apiKeyTestCreate(t, foreignHandler, "foreign")
	for _, path := range []string{"/v1/workspaces", "/v1/databases/" + id + "/workspaces", "/databases/" + id + "/v1/api-keys"} {
		if r := apiKeyTestCall(t, h, rootKey.Token, "GET", path, nil); r.Code != 200 {
			t.Fatalf("root key failed %s: %d %s", path, r.Code, r.Body.String())
		}
		if r := apiKeyTestCall(t, h, foreignKey.Token, "GET", path, nil); r.Code != 401 {
			t.Fatalf("foreign key accepted %s: %d", path, r.Code)
		}
	}
	// Key creation using a scoped URL still persists into independent metadata.
	scoped := serverTestJSON(t, apiKeyTestCall(t, h, rootKey.Token, "POST", "/databases/"+id+"/v1/api-keys", map[string]string{"name": "scoped"}))
	scopedID := scoped["key"].(map[string]any)["id"].(string)
	var sqlCount int
	if err := h.metadata.db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE id = ?", scopedID).Scan(&sqlCount); err != nil {
		t.Fatal(err)
	}
	if sqlCount != 1 || rootRedis.Exists(context.Background(), apiKeyRecord(scopedID)).Val() != 0 || secondary.Exists(apiKeyRecord(scopedID)) {
		t.Fatal("scoped creation used wrong auth store")
	}
	// Editing the default connection must neither orphan nor replace auth records.
	replacement := miniredis.RunT(t)
	input := databaseEditInput(t, h, "local")
	input["redis_addr"] = replacement.Addr()
	if r := serverTestCall(t, h, "PUT", "/v1/databases/local", input); r.Code != 200 {
		t.Fatalf("edit: %d %s", r.Code, r.Body.String())
	}
	if r := apiKeyTestCall(t, h, rootKey.Token, "GET", "/v1/workspaces", nil); r.Code != 200 {
		t.Fatalf("default edit orphaned key: %d %s", r.Code, r.Body.String())
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = NewDatabaseHandler(NewService(NewStore(rootRedis)), HandlerOptions{AuthToken: "test-secret"}, filename)
	if err != nil {
		t.Fatal(err)
	}
	if r := apiKeyTestCall(t, h, rootKey.Token, "GET", "/v1/workspaces", nil); r.Code != 200 {
		t.Fatalf("restart orphaned key: %d %s", r.Code, r.Body.String())
	}
	serverTestJSON(t, apiKeyTestCall(t, h, rootKey.Token, "DELETE", "/databases/"+id+"/v1/api-keys/"+rootKey.Key.ID, nil))
	if r := apiKeyTestCall(t, h, rootKey.Token, "GET", "/v1/workspaces", nil); r.Code != 401 {
		t.Fatalf("scoped revoke did not revoke root identity: %d", r.Code)
	}
}

func TestAPIKeyCLICheckpointAuthorUsesAuthenticatedIdentity(t *testing.T) {
	s, _ := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	created := apiKeyTestCreate(t, h, "checkpoint creator")
	workspace := serverTestJSON(t, apiKeyTestCall(t, h, created.Token, "POST", "/v1/workspaces", map[string]any{"name": "checkpoint-auth"}))
	id := workspace["id"].(string)
	for _, author := range []string{"", "forged-author"} {
		cp := serverTestJSON(t, apiKeyTestCall(t, h, created.Token, "POST", "/v1/cli", cliRequest{Operation: "checkpoint.save", Workspace: id, Name: defaultString(author, "default-author"), CheckpointOptions: CheckpointOptions{Author: author, AllowUnchanged: true}}))
		if cp["author"] != "api-key:"+created.Key.ID || cp["source"] != CheckpointSourceCLI {
			t.Fatalf("CLI checkpoint author can be spoofed: %v", cp)
		}
	}
}
