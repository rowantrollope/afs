package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Existing engine fixtures supply an initial Redis. Production uses the empty
// metadata constructor directly and never synthesizes a localhost connection.
func NewDatabaseHandler(service *Service, options HandlerOptions, filename string) (*DatabaseHandler, error) {
	metadata, err := OpenMetadataStore(filename)
	if err != nil {
		return nil, err
	}
	_, _, initialized, err := metadata.LoadProfiles(context.Background())
	if err != nil {
		metadata.Close()
		return nil, err
	}
	if !initialized {
		original := NewHandler(service, options).(*serverHandler)
		profile := profileFromHandler(original)
		profile.Revision, err = randomManagementToken()
		if err == nil {
			err = metadata.SaveProfiles(context.Background(), []databaseProfile{profile}, profile.ID)
		}
		if err != nil {
			metadata.Close()
			return nil, err
		}
	}
	h, err := NewMetadataDatabaseHandler(metadata, options, "", "")
	if err != nil {
		metadata.Close()
	}
	return h, err
}

func emptyMetadataHandler(t *testing.T, filename, legacy string) *DatabaseHandler {
	t.Helper()
	metadata, err := OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewMetadataDatabaseHandler(metadata, HandlerOptions{AuthToken: "test-secret"}, legacy, "")
	if err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestMetadataEmptyStartupAndOfflineAdministration(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "control-plane.sqlite")
	h := emptyMetadataHandler(t, filename, "")
	key := apiKeyTestCreate(t, h, "offline operator")
	for _, path := range []string{"/healthz", "/v1/auth/verify", "/v1/databases", "/v1/workspaces", "/v1/agents", "/v1/events", "/v1/api-keys"} {
		r := apiKeyTestCall(t, h, key.Token, "GET", path, nil)
		if r.Code != 200 {
			t.Fatalf("empty %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	if r := apiKeyTestCall(t, h, key.Token, "GET", "/v1/connection", nil); r.Code != 409 {
		t.Fatalf("empty connection: %d", r.Code)
	}
	backend := miniredis.RunT(t)
	added := metadataTestJSON(t, serverTestCall(t, h, "POST", "/v1/databases", map[string]any{"name": "First", "redis_addr": backend.Addr()}))
	id := added["id"].(string)
	if added["is_default"] != true {
		t.Fatal("first connection was not default")
	}
	backend.Close()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h = emptyMetadataHandler(t, filename, "")
	for _, path := range []string{"/healthz", "/v1/auth/verify", "/v1/api-keys"} {
		if r := apiKeyTestCall(t, h, key.Token, "GET", path, nil); r.Code != 200 {
			t.Fatalf("offline %s: %d", path, r.Code)
		}
	}
	// Removing an unavailable backend needs neither a Redis connection nor its
	// credentials, and cannot orphan the control-plane administrator key.
	r := apiKeyTestCall(t, h, key.Token, "DELETE", "/v1/databases/"+id, nil)
	if r.Code != 200 {
		t.Fatalf("remove offline: %d %s", r.Code, r.Body.String())
	}
	if r := apiKeyTestCall(t, h, key.Token, "GET", "/v1/api-keys", nil); r.Code != 200 {
		t.Fatal("removal orphaned key")
	}
}

func TestMetadataDefaultRemovalPersistenceAndRequestDraining(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "metadata.sqlite")
	h := emptyMetadataHandler(t, filename, "")
	first, second := miniredis.RunT(t), miniredis.RunT(t)
	add := func(name, addr string) string {
		return metadataTestJSON(t, serverTestCall(t, h, "POST", "/v1/databases", map[string]any{"name": name, "redis_addr": addr}))["id"].(string)
	}
	firstID, secondID := add("First", first.Addr()), add("Second", second.Addr())
	if r := serverTestCall(t, h, "POST", "/v1/databases/"+secondID+"/default", nil); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if h.defaultDatabaseID() != secondID {
		t.Fatal("default not switched")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h = emptyMetadataHandler(t, filename, "")
	if h.defaultDatabaseID() != secondID {
		t.Fatal("default not persisted")
	}
	leased, release := h.acquire(secondID)
	second.Set("untouched", "data")
	if r := serverTestCall(t, h, "DELETE", "/v1/databases/"+secondID, nil); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if h.defaultDatabaseID() != firstID {
		t.Fatal("default not reassigned")
	}
	if err := leased.service.store.rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal("removal interrupted request")
	}
	release()
	if err := leased.service.store.rdb.Ping(context.Background()).Err(); err != redis.ErrClosed {
		t.Fatalf("retired connection not closed: %v", err)
	}
	if value, _ := second.Get("untouched"); value != "data" {
		t.Fatal("removal changed Redis contents")
	}
	if r := serverTestCall(t, h, "GET", "/databases/"+secondID+"/v1/connection", nil); r.Code != 404 {
		t.Fatal("removed scope fell back to default")
	}
	metadataTestJSON(t, serverTestCall(t, h, "DELETE", "/v1/databases/"+firstID, nil))
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h = emptyMetadataHandler(t, filename, "")
	if len(h.handlers) != 0 || h.defaultDatabaseID() != "" {
		t.Fatal("empty catalog did not persist")
	}
}

func TestMetadataLegacyJSONImportsOnceAndPreservesSource(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "metadata.sqlite")
	legacy := filepath.Join(dir, "databases.json")
	source := []byte(`[{"id":"local","name":"Saved cloud","redis_addr":"127.0.0.1:1","redis_password":"private-import-password","redis_db":2,"redis_tls":false}]`)
	if err := os.WriteFile(legacy, source, 0600); err != nil {
		t.Fatal(err)
	}
	h := emptyMetadataHandler(t, filename, legacy)
	if h.lookup("local").service.store.rdb.Options().Password != "private-import-password" {
		t.Fatal("migration lost credentials")
	}
	after, _ := os.ReadFile(legacy)
	if string(after) != string(source) {
		t.Fatal("migration changed source backup")
	}
	metadataTestJSON(t, serverTestCall(t, h, http.MethodDelete, "/v1/databases/local", nil))
	h.Close()
	h = emptyMetadataHandler(t, filename, legacy)
	if len(h.handlers) != 0 {
		t.Fatal("legacy import resurrected removed connection")
	}
}

func TestMetadataLegacyAPIKeyMigrationPreservesCredentialsAndRevocation(t *testing.T) {
	service, rdb := serviceFixture(t)
	old := NewHandler(service, HandlerOptions{AuthToken: "test-secret"})
	active := apiKeyTestCreate(t, old, "active")
	revoked := apiKeyTestCreate(t, old, "revoked")
	metadataTestJSON(t, serverTestCall(t, old, "DELETE", "/v1/api-keys/"+revoked.Key.ID, nil))
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "metadata.sqlite"), "")
	if err := MigrateLegacyAPIKeys(context.Background(), h.metadata, rdb); err != nil {
		t.Fatal(err)
	}
	if r := apiKeyTestCall(t, h, active.Token, "GET", "/v1/auth/verify", nil); r.Code != 200 {
		t.Fatal("migration changed active token")
	}
	if r := apiKeyTestCall(t, h, revoked.Token, "GET", "/v1/auth/verify", nil); r.Code != 401 {
		t.Fatal("migration reactivated revoked token")
	}
	metadataTestJSON(t, serverTestCall(t, h, "DELETE", "/v1/api-keys/"+active.Key.ID, nil))
	if err := MigrateLegacyAPIKeys(context.Background(), h.metadata, rdb); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := h.metadata.authenticateAPIKey(context.Background(), active.Token, time.Now()); err != nil || valid {
		t.Fatal("repeat migration reactivated locally revoked key")
	}
	if _, valid, err := service.store.authenticateAPIKey(context.Background(), active.Token, time.Now()); err != nil || !valid {
		t.Fatal("migration changed source key")
	}
}

func TestMetadataFailedDefaultOrDeleteDoesNotPublishChanges(t *testing.T) {
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "metadata.sqlite"), "")
	for _, name := range []string{"First", "Second"} {
		backend := miniredis.RunT(t)
		metadataTestJSON(t, serverTestCall(t, h, "POST", "/v1/databases", map[string]any{"name": name, "redis_addr": backend.Addr()}))
	}
	first := h.defaultDatabaseID()
	second := ""
	for id := range h.handlers {
		if id != first {
			second = id
		}
	}
	if _, err := h.metadata.db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path string }{{"POST", "/v1/databases/" + second + "/default"}, {"DELETE", "/v1/databases/" + first}} {
		if r := serverTestCall(t, h, request.method, request.path, nil); r.Code != 400 {
			t.Fatal("failed write accepted")
		}
	}
	if h.defaultDatabaseID() != first || h.lookup(first) == nil {
		t.Fatal("failed write published changes")
	}
	profiles, defaultID, _, err := h.metadata.LoadProfiles(context.Background())
	if err != nil || len(profiles) != 2 || defaultID != first {
		t.Fatal("failed write changed persisted settings")
	}
}

func metadataTestJSON(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
