package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func sharedMetadataHandlers(t *testing.T) (*DatabaseHandler, *DatabaseHandler) {
	t.Helper()
	dsn := postgresTestDatabase(t)
	open := func() *DatabaseHandler {
		metadata, err := OpenPostgresMetadataStore(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = metadata.Close() })
		h, err := NewMetadataDatabaseHandler(metadata, HandlerOptions{AuthToken: "test-secret"}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	return open(), open()
}

func sharedMetadataAdd(t *testing.T, h *DatabaseHandler, name, addr string) string {
	t.Helper()
	result := metadataTestJSON(t, serverTestCall(t, h, http.MethodPost, "/v1/databases", map[string]any{"name": name, "redis_addr": addr}))
	return result["id"].(string)
}

func TestSharedMetadataRefreshesRoutingAndDrainsReplacedClients(t *testing.T) {
	first, second := sharedMetadataHandlers(t)
	backendA, backendB := miniredis.RunT(t), miniredis.RunT(t)
	idA := sharedMetadataAdd(t, first, "Database A", backendA.Addr())
	serverTestJSON(t, serverTestCall(t, second, http.MethodGet, "/v1/databases", nil))
	old, release := second.acquire(idA)
	if old == nil {
		t.Fatal("second instance did not discover added connection")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	serverTestJSON(t, serverTestCall(t, second, http.MethodGet, "/v1/databases", nil))
	if second.lookup(idA) != old {
		t.Fatal("unchanged snapshot replaced a live client")
	}
	input := databaseEditInput(t, first, idA)
	input["name"] = "Renamed A"
	serverTestJSON(t, serverTestCall(t, first, http.MethodPut, "/v1/databases/"+idA, input))
	record := serverTestJSON(t, serverTestCall(t, second, http.MethodGet, "/v1/databases/"+idA, nil))
	if record["name"] != "Renamed A" || second.lookup(idA) == old {
		t.Fatal("second instance served stale edited settings")
	}
	if err := old.service.store.rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("refresh interrupted a leased request: %v", err)
	}
	release()
	release = nil
	if err := old.service.store.rdb.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("retired client did not close after request drained: %v", err)
	}
	stale := serverTestCall(t, second, http.MethodPut, "/v1/databases/"+idA, input)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "stale_database_settings") {
		t.Fatalf("stale profile revision accepted: %d %s", stale.Code, stale.Body.String())
	}

	idB := sharedMetadataAdd(t, first, "Database B", backendB.Addr())
	signature, err := second.root.monitorSignature(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unchanged := second.lookup(idA)
	serverTestJSON(t, serverTestCall(t, first, http.MethodPost, "/v1/databases/"+idB+"/default", nil))
	nextSignature, err := second.root.monitorSignature(context.Background())
	if err != nil || nextSignature == signature || second.defaultDatabaseID() != idB {
		t.Fatalf("monitor did not refresh changed default: %v", err)
	}
	if second.lookup(idA) != unchanged {
		t.Fatal("default-only refresh replaced unchanged connection")
	}
	connection := serverTestJSON(t, serverTestCall(t, second, http.MethodGet, "/v1/connection", nil))
	if !strings.Contains(connection["redis_url"].(string), backendB.Addr()) {
		t.Fatal("default route used previous Redis connection")
	}
	serverTestJSON(t, serverTestCall(t, second, http.MethodPost, "/v1/workspaces", map[string]any{"name": "on-new-default"}))
	if len(backendA.Keys()) != 0 || len(backendB.Keys()) == 0 {
		t.Fatal("workspace creation did not use the shared default")
	}
	removed, releaseRemoved := second.acquire(idB)
	defer func() {
		if releaseRemoved != nil {
			releaseRemoved()
		}
	}()
	serverTestJSON(t, serverTestCall(t, first, http.MethodDelete, "/v1/databases/"+idB, nil))
	if response := serverTestCall(t, second, http.MethodGet, "/v1/databases/"+idB, nil); response.Code != http.StatusNotFound {
		t.Fatalf("removed connection still routed: %d", response.Code)
	}
	if second.defaultDatabaseID() != idA || len(backendB.Keys()) == 0 {
		t.Fatal("removal lost fallback default or deleted Redis workspace data")
	}
	if err := removed.service.store.rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("removal interrupted a leased client: %v", err)
	}
	releaseRemoved()
	releaseRemoved = nil
	if err := removed.service.store.rdb.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("removed client remained open after draining: %v", err)
	}
}

func TestSharedMetadataRejectsEveryStaleRegistryMutation(t *testing.T) {
	first, second := sharedMetadataHandlers(t)
	backendA, backendB, backendC := miniredis.RunT(t), miniredis.RunT(t), miniredis.RunT(t)
	idA := sharedMetadataAdd(t, first, "A", backendA.Addr())
	input := databaseEditInput(t, second, idA)
	beforeHandler, beforeRevision := second.lookup(idA), second.revision
	idB := sharedMetadataAdd(t, first, "B", backendB.Addr())
	// Invoke mutation handlers directly to simulate another instance committing
	// after this request's initial refresh but before its write transaction.
	for _, action := range []struct {
		name, method string
		body         any
		call         func(http.ResponseWriter, *http.Request)
	}{
		{"add", http.MethodPost, map[string]any{"name": "C", "redis_addr": backendC.Addr()}, second.databasesRoute},
		{"edit", http.MethodPut, input, func(w http.ResponseWriter, r *http.Request) { second.updateDatabaseRoute(w, r, idA) }},
		{"remove", http.MethodDelete, nil, func(w http.ResponseWriter, r *http.Request) { second.removeDatabaseRoute(w, r, idA) }},
		{"default", http.MethodPost, nil, func(w http.ResponseWriter, r *http.Request) { second.setDefaultDatabaseRoute(w, r, idA) }},
	} {
		t.Run(action.name, func(t *testing.T) {
			raw, err := json.Marshal(action.body)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			action.call(response, httptest.NewRequest(action.method, "/v1/databases", bytes.NewReader(raw)))
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "stale_database_registry") {
				t.Fatalf("stale mutation: %d %s", response.Code, response.Body.String())
			}
			if second.lookup(idA) != beforeHandler || second.revision != beforeRevision || len(second.profiles) != 1 {
				t.Fatal("failed compare-and-swap changed active registry")
			}
		})
	}
	serverTestJSON(t, serverTestCall(t, second, http.MethodDelete, "/v1/databases/"+idA, nil))
	if second.defaultDatabaseID() != idB || second.lookup(idB) == nil {
		t.Fatal("retry after refresh lost the other instance's connection")
	}
}

func TestSharedMetadataRefreshFailureDoesNotBlockAuthentication(t *testing.T) {
	first, second := sharedMetadataHandlers(t)
	backend := miniredis.RunT(t)
	id := sharedMetadataAdd(t, first, "A", backend.Addr())
	serverTestJSON(t, serverTestCall(t, second, http.MethodGet, "/v1/databases", nil))
	key := apiKeyTestCreate(t, first, "shared operator")
	if _, err := first.metadata.db.Exec(first.metadata.querySQL("UPDATE database_profiles SET profile = ? WHERE id = ?"), "{", id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/connection", "/v1/databases", "/v1/databases/" + id + "/workspaces", "/v1/workspaces", "/v1/monitor/stream"} {
		response := apiKeyTestCall(t, second, key.Token, http.MethodGet, path, nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("stale registry fallback on %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/v1/auth/config", "/v1/auth/verify", "/v1/api-keys", "/v1/version"} {
		if response := apiKeyTestCall(t, second, key.Token, http.MethodGet, path, nil); response.Code != http.StatusOK {
			t.Fatalf("registry failure blocked %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if response := apiKeyTestCall(t, second, "", http.MethodGet, "/v1/databases", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("refresh occurred before authentication: %d", response.Code)
	}
}

func TestSharedMetadataConcurrentInitializationKeepsOneWinner(t *testing.T) {
	dsn := postgresTestDatabase(t)
	backendA, backendB := miniredis.RunT(t), miniredis.RunT(t)
	stores := make([]*MetadataStore, 2)
	for i := range stores {
		var err error
		stores[i], err = OpenPostgresMetadataStore(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		store := stores[i]
		t.Cleanup(func() { _ = store.Close() })
	}
	var wg sync.WaitGroup
	handlers := make([]*DatabaseHandler, 2)
	errors := make([]error, 2)
	start := make(chan struct{})
	for i, backend := range []*miniredis.Miniredis{backendA, backendB} {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			<-start
			handlers[i], errors[i] = NewMetadataDatabaseHandler(stores[i], HandlerOptions{AuthToken: "test-secret"}, "", "redis://"+addr+"/0")
		}(i, backend.Addr())
	}
	close(start)
	wg.Wait()
	for i, h := range handlers {
		if errors[i] != nil {
			t.Fatal(errors[i])
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(handlers[0].profiles) != 1 || len(handlers[1].profiles) != 1 || handlers[0].revision != handlers[1].revision || handlers[0].profiles[0] != handlers[1].profiles[0] {
		t.Fatal("concurrent startup published different registries")
	}
}

func TestMonitorStreamDurationBoundsHostedRequests(t *testing.T) {
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "catalog.sqlite"), "")
	h.root.options.StreamDuration = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/v1/monitor/stream", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer test-secret")
	response := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: ready") || !response.Flushed {
		t.Fatalf("monitor stream did not begin: %d %s", response.Code, response.Body.String())
	}
	if time.Since(start) > 500*time.Millisecond || ctx.Err() != nil {
		t.Fatal("stream waited for caller cancellation instead of its configured duration")
	}
}
