package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func databaseEditInput(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	record := serverTestJSON(t, serverTestCall(t, h, http.MethodGet, "/v1/databases/"+id, nil))
	input := map[string]any{}
	for _, field := range []string{"name", "description", "redis_addr", "redis_username", "redis_db", "redis_tls", "config_revision"} {
		if value, exists := record[field]; exists {
			input[field] = value
		}
	}
	if input["config_revision"] == nil || input["config_revision"] == "" {
		t.Fatal("database settings did not include a configuration revision")
	}
	return input
}

func TestDatabaseEditDrainsRequestAndSnapshotLeases(t *testing.T) {
	h, _, _, id := databaseEventsFixture(t)
	old, release := h.acquire(id)
	if old == nil {
		t.Fatal("missing registered database")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	snapshot, releaseSnapshot := h.root.acquireDatabaseHandlers()
	defer func() {
		if releaseSnapshot != nil {
			releaseSnapshot()
		}
	}()
	found := false
	for _, handler := range snapshot {
		found = found || handler == old
	}
	if !found {
		t.Fatal("aggregate snapshot omitted registered database")
	}
	input := databaseEditInput(t, h, id)
	input["name"] = "Renamed secondary"
	response := serverTestCall(t, h, http.MethodPut, "/v1/databases/"+id, input)
	if response.Code != http.StatusOK {
		t.Fatalf("edit: HTTP %d: %s", response.Code, response.Body.String())
	}
	current := h.lookup(id)
	if current == old || current.options.DatabaseName != "Renamed secondary" {
		t.Fatal("edit did not publish a new immutable handler")
	}
	beforeOptions, afterOptions := old.service.store.rdb.Options(), current.service.store.rdb.Options()
	if afterOptions.DialTimeout != beforeOptions.DialTimeout || afterOptions.MaxRetries != beforeOptions.MaxRetries {
		t.Fatal("metadata edit changed the added connection's dial timeout or retry policy")
	}
	ctx := context.Background()
	if err := old.service.store.rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("edit closed an in-flight request client: %v", err)
	}
	release()
	release = nil
	if err := old.service.store.rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("request release closed a client still held by an aggregate snapshot: %v", err)
	}
	releaseSnapshot()
	releaseSnapshot = nil
	if err := old.service.store.rdb.Ping(ctx).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("retired client was not closed after final release: %v", err)
	}
	if err := current.service.store.rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("new database client is unavailable: %v", err)
	}
}

func TestDatabaseEditKeepsInFlightHTTPQueriesConnected(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "scoped request"
		if aggregate {
			name = "aggregate query"
		}
		t.Run(name, func(t *testing.T) {
			h, _, _, id := databaseEventsFixture(t)
			input := databaseEditInput(t, h, id)
			input["name"] = "Edited during query"
			old := h.lookup(id)
			started, unblock := make(chan struct{}), make(chan struct{})
			var blocked atomic.Bool
			var unblockOnce sync.Once
			resume := func() { unblockOnce.Do(func() { close(unblock) }) }
			defer resume()
			old.service.store.rdb.AddHook(faultHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
				if cmd.Name() == "ping" && blocked.CompareAndSwap(false, true) {
					close(started)
					select {
					case <-unblock:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return next(ctx, cmd)
			}})
			path := "/v1/databases/" + id
			if aggregate {
				path = "/v1/databases"
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- serverTestCall(t, h, http.MethodGet, path, nil) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request never reached its Redis connection")
			}
			response := serverTestCall(t, h, http.MethodPut, "/v1/databases/"+id, input)
			if response.Code != http.StatusOK {
				t.Fatalf("edit: HTTP %d: %s", response.Code, response.Body.String())
			}
			if err := old.service.store.rdb.Ping(context.Background()).Err(); err != nil {
				t.Fatalf("edit closed the connection of an in-flight HTTP query: %v", err)
			}
			resume()
			select {
			case response = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("in-flight query did not drain")
			}
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "connection_error") {
				t.Fatalf("edit interrupted HTTP query: %d %s", response.Code, response.Body.String())
			}
			if err := old.service.store.rdb.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
				t.Fatalf("HTTP query did not release its retired client: %v", err)
			}
		})
	}
}

func TestConcurrentDatabaseEditsRequireCurrentRevision(t *testing.T) {
	h, _, _, id := databaseEventsFixture(t)
	first := databaseEditInput(t, h, id)
	second := databaseEditInput(t, h, id)
	first["name"], second["name"] = "First change", "Second change"
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, input := range []map[string]any{first, second} {
		wg.Add(1)
		go func(input map[string]any) {
			defer wg.Done()
			<-start
			response := serverTestCall(t, h, http.MethodPut, "/v1/databases/"+id, input)
			results <- response.Code
		}(input)
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent edits must have exactly one winner: %v", counts)
	}
	updated := databaseEditInput(t, h, id)
	if updated["config_revision"] == first["config_revision"] {
		t.Fatal("successful edit reused its old configuration revision")
	}
	profiles, _, _, err := h.metadata.LoadProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, profile := range profiles {
		if profile.ID == id {
			found = true
			if profile.Name != updated["name"] {
				t.Fatal("persisted profile disagrees with winning edit")
			}
		}
	}
	if !found {
		t.Fatal("edited profile missing")
	}
}

func TestDatabaseEditFailedSavePreservesConnectionAndCredentials(t *testing.T) {
	service, _ := serviceFixture(t)
	filename := filepath.Join(t.TempDir(), "databases.json")
	h, err := NewDatabaseHandler(service, HandlerOptions{AuthToken: "test-secret"}, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	original := miniredis.RunT(t)
	original.RequireAuth("old-private-password")
	response := serverTestCall(t, h, http.MethodPost, "/v1/databases", map[string]any{
		"name": "Protected", "redis_addr": original.Addr(), "redis_password": "old-private-password",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d: %s", response.Code, response.Body.String())
	}
	var record map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	id := record["id"].(string)
	before := h.lookup(id)
	persisted, _, _, err := h.metadata.LoadProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input := databaseEditInput(t, h, id)
	replacement := miniredis.RunT(t)
	replacement.RequireAuth("new-private-password")
	input["name"], input["redis_addr"], input["redis_password"] = "Replacement", replacement.Addr(), "new-private-password"
	// Force SQLite to reject writes without altering the active Redis client.
	if _, err := h.metadata.db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	response = serverTestCall(t, h, http.MethodPut, "/v1/databases/"+id, input)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("failed persistence: HTTP %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-password") {
		t.Fatal("failed edit leaked connection credentials")
	}
	if current := h.lookup(id); current != before || current.service.store.rdb.Options().Password != "old-private-password" {
		t.Fatal("failed persistence published candidate settings or credentials")
	}
	if err := before.service.store.rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("failed persistence closed the current connection: %v", err)
	}
	for _, p := range h.profiles {
		if p.ID == id && (p.Name != "Protected" || p.RedisPassword != "old-private-password") {
			t.Fatal("failed persistence changed active profile")
		}
	}
	after, _, _, err := h.metadata.LoadProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, _ := json.Marshal(persisted)
	afterJSON, _ := json.Marshal(after)
	if string(originalJSON) != string(afterJSON) {
		t.Fatal("failed persistence changed saved profiles")
	}
}

func TestDefaultDatabaseEditPreservesPrivateURLAndAdvancedOptions(t *testing.T) {
	backend := miniredis.RunT(t)
	const password = "private-password-for-settings-edit"
	backend.RequireAuth(password)
	u := &url.URL{Scheme: "redis", Host: backend.Addr(), Path: "/0", User: url.UserPassword("", password),
		RawQuery: "read_timeout=1500ms&write_timeout=1700ms&client_name=primary-advanced&protocol=2&dial_timeout=4s&max_retries=2"}
	options, err := redis.ParseURL(u.String())
	if err != nil {
		t.Fatal(err)
	}
	// Startup flags can override these URL options before constructing the client.
	options.DialTimeout, options.MaxRetries = 3*time.Second, 1
	rdb := redis.NewClient(options)
	defer rdb.Close()
	service := NewService(NewStore(rdb))
	filename := filepath.Join(t.TempDir(), "databases.json")
	handlerOptions := HandlerOptions{AuthToken: "test-secret", RedisURL: u.String()}
	h, err := NewDatabaseHandler(service, handlerOptions, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	input := databaseEditInput(t, h, "local")
	input["name"], input["description"] = "Primary renamed", "Updated description"
	response := serverTestCall(t, h, http.MethodPut, "/v1/databases/local", input)
	if response.Code != http.StatusOK {
		t.Fatalf("default edit: HTTP %d: %s", response.Code, response.Body.String())
	}
	assertPreserved := func(t *testing.T, handler *DatabaseHandler) {
		t.Helper()
		current := handler.lookup("local")
		connection := current.service.store.rdb.Options()
		if connection.Password != password || connection.ReadTimeout != 1500*time.Millisecond || connection.WriteTimeout != 1700*time.Millisecond || connection.ClientName != "primary-advanced" || connection.Protocol != 2 {
			t.Fatal("editing metadata changed private credentials or advanced Redis options")
		}
		if connection.DialTimeout != 3*time.Second || connection.MaxRetries != 1 {
			t.Fatal("editing metadata replaced the effective startup dial timeout or retry policy with the original URL options")
		}
		if current.options.AdditionalDatabase || current.options.DatabaseName != "Primary renamed" {
			t.Fatal("editing the default connection lost its name or default status")
		}
		parsed, err := url.Parse(current.options.RedisURL)
		if err != nil || parsed.Query().Get("read_timeout") != "1500ms" || parsed.Query().Get("client_name") != "primary-advanced" {
			t.Fatal("connection bootstrap lost advanced Redis URL options")
		}
		if parsed.Query().Get("dial_timeout") != "3s" || parsed.Query().Get("max_retries") != "1" {
			t.Fatal("connection bootstrap does not preserve the effective dial timeout and retry policy")
		}
		for _, path := range []string{"/v1/databases", "/v1/databases/local", "/v1/database"} {
			r := serverTestCall(t, handler, http.MethodGet, path, nil)
			if r.Code != http.StatusOK || strings.Contains(r.Body.String(), password) || strings.Contains(r.Body.String(), "redis_url") {
				t.Fatalf("database settings did not remain available with redacted credentials at %s (HTTP %d)", path, r.Code)
			}
		}
	}
	if strings.Contains(response.Body.String(), password) {
		t.Fatal("edit response leaked the saved password")
	}
	assertPreserved(t, h)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewDatabaseHandler(service, handlerOptions, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertPreserved(t, restored)
}
