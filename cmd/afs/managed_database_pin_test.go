package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/managedclient"
)

func TestManagedMountPinsDatabaseAcrossDefaultChanges(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	var defaultB atomic.Bool
	events := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		database, path := "db-a", r.URL.Path
		if defaultB.Load() {
			database = "db-b"
		}
		if strings.HasPrefix(path, "/databases/db-a/") {
			database, path = "db-a", strings.TrimPrefix(path, "/databases/db-a")
		}
		switch {
		case path == "/v1/connection":
			_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": "redis://127.0.0.1:1/0", "database_id": "db-a"})
			// Switch before the caller fetches workspace metadata: both parts of
			// mount bootstrap must stay on the database selected above.
			defaultB.Store(true)
		case path == "/v1/cli":
			_ = json.NewEncoder(w).Encode(controlplane.WorkspaceMeta{ID: "ws-" + database, Name: "shared"})
		case strings.HasSuffix(path, "/sessions"):
			var registration managedclient.Registration
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Error(err)
			}
			events <- "register:" + database
			_ = json.NewEncoder(w).Encode(managedclient.Session{SessionID: registration.SessionID, WorkspaceID: registration.WorkspaceID, HeartbeatIntervalSeconds: 1})
		case strings.HasSuffix(path, "/heartbeat"):
			events <- "heartbeat:" + database
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			events <- "close:" + database
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected management request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	savedSettings := &managedclient.Settings{URL: server.URL, Token: "private-token"}
	a := app{config: config{ControlPlane: savedSettings}, options: cliOptions{json: true}}
	if err := a.connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.service.(*controlplane.CLIClient).Close() }()
	if err := a.resolveManagedRedis(context.Background()); err != nil {
		t.Fatal(err)
	}
	workspace, err := a.service.GetWorkspace(context.Background(), "shared")
	if err != nil || workspace.ID != "ws-db-a" {
		t.Fatalf("default change redirected mount workspace lookup: %+v, %v", workspace, err)
	}
	wantURL := server.URL + "/databases/db-a"
	if managedConfig(a.config).URL != wantURL || savedSettings.URL != server.URL {
		t.Fatal("database pin was not limited to the runtime configuration")
	}
	record := mountRecord{WorkspaceID: workspace.ID}
	if err := prepareManagedMount(a.config, &record); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(syncDaemonBootstrap{Config: a.config, Record: record})
	if err != nil {
		t.Fatal(err)
	}
	var boot syncDaemonBootstrap
	if err := json.Unmarshal(raw, &boot); err != nil || boot.Record.ControlPlaneURL != wantURL || managedConfig(boot.Config).URL != wantURL {
		t.Fatal("mount record or daemon bootstrap lost the selected database")
	}
	defaultB.Store(false)
	lifecycle := managedclient.Start(context.Background(), managedConfig(boot.Config), managedRegistration(boot.Record), nil, nil)
	defer lifecycle.Close()
	if !lifecycle.Snapshot().Registered {
		t.Fatalf("session did not register: %+v", lifecycle.Snapshot())
	}
	waitEvent := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("session was redirected: got %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	waitEvent("register:db-a")
	waitEvent("heartbeat:db-a")
	defaultB.Store(true)
	waitEvent("heartbeat:db-a")
	lifecycle.Close()
	waitEvent("close:db-a")
}

func TestManagedDatabasePinPreservesExplicitScopeAndLegacyServers(t *testing.T) {
	for _, test := range []struct {
		name, prefix, databaseID string
	}{
		{name: "explicit", prefix: "/databases/db-a", databaseID: "db-a"},
		{name: "legacy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.prefix+"/v1/connection" {
					t.Errorf("unexpected bootstrap path %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": "redis://127.0.0.1:1/0", "database_id": test.databaseID})
			}))
			defer server.Close()
			endpoint := server.URL + test.prefix
			a := app{config: config{ControlPlane: &managedclient.Settings{URL: endpoint}}}
			if err := a.resolveManagedRedis(context.Background()); err != nil {
				t.Fatal(err)
			}
			if managedConfig(a.config).URL != endpoint {
				t.Fatal("explicit scope or legacy endpoint was rewritten")
			}
		})
	}
}

func TestManagedMountGuardPinsBeforeWorkspaceLookup(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	redisURL := "redis://127.0.0.1:1/3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/connection":
			_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": redisURL, "database_id": "db-b"})
		case "/databases/db-b/v1/cli":
			_ = json.NewEncoder(w).Encode(controlplane.WorkspaceMeta{ID: "ws-b", Name: "shared"})
		default:
			_ = json.NewEncoder(w).Encode(controlplane.WorkspaceMeta{ID: "ws-a", Name: "shared"})
		}
	}))
	defer server.Close()
	record := mountRecord{WorkspaceID: "ws-b", LocalPath: "/selected-database-mount", RedisIdentity: redisIdentity(config{Redis: redisURL})}
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{record}}); err != nil {
		t.Fatal(err)
	}
	a := app{config: config{ControlPlane: &managedclient.Settings{URL: server.URL}}, options: cliOptions{json: true}}
	if err := a.connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.service.(*controlplane.CLIClient).Close() }()
	matching, err := a.localWorkspaceMounts(context.Background(), "shared")
	if err != nil || len(matching) != 1 || matching[0] != record {
		t.Fatalf("mount guard paired different database lookups: %v, %v", matching, err)
	}
}

func TestManagedMountRejectsChangedDatabaseIdentity(t *testing.T) {
	for _, databaseID := range []string{"db-b", "../db-a"} {
		t.Run(databaseID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": "redis://127.0.0.1:1/3", "database_id": databaseID})
			}))
			defer server.Close()
			endpoint := server.URL + "/databases/db-a"
			a := app{config: config{Redis: "old-value", ControlPlane: &managedclient.Settings{URL: endpoint}}}
			if err := a.resolveManagedRedis(context.Background()); err == nil {
				t.Fatal("accepted a different or invalid database identity")
			}
			if a.config.Redis != "old-value" || a.config.redisFromControlPlane || managedConfig(a.config).URL != endpoint {
				t.Fatal("rejected database identity changed runtime connection settings")
			}
		})
	}
}
