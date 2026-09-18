package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/managedclient"
)

func TestManagedConnectionOverridesOnlyRuntimeRedis(t *testing.T) {
	const serverURL = "rediss://server-user:server-password@redis.example:6380/4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connection" || r.Header.Get("Authorization") != "Bearer team-secret" {
			t.Errorf("unexpected bootstrap request: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": serverURL})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(map[string]any{"redis": "not a redis URL", "controlPlane": map[string]string{"url": server.URL, "token": "team-secret"}})
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFS_REDIS_URL", "also invalid and unused")
	t.Setenv("AFS_REDIS_PASSWORD", "wrong-local-password")
	cfg, err := readConfig(file, "")
	if err != nil {
		t.Fatal(err)
	}
	a := app{config: cfg}
	if err := a.resolveManagedRedis(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := buildRedisOptions(a.config, 1)
	if opts.Addr != "redis.example:6380" || opts.Username != "server-user" || opts.Password != "server-password" || opts.DB != 4 || opts.TLSConfig == nil {
		t.Fatal("server Redis connection was not preserved")
	}
	for _, entry := range daemonEnvironment(a.config) {
		if strings.HasPrefix(entry, "AFS_REDIS_URL=") || strings.HasPrefix(entry, "AFS_REDIS_PASSWORD=") {
			t.Fatal("local Redis overrides leaked into managed daemon environment")
		}
	}
	// Re-exec deserializes only the private bootstrap; local overrides are absent.
	boot, _ := json.Marshal(syncDaemonBootstrap{Config: a.config})
	var decoded syncDaemonBootstrap
	if err := json.Unmarshal(boot, &decoded); err != nil || decoded.Config.Redis != serverURL {
		t.Fatal("daemon bootstrap lost connection")
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(raw) || strings.Contains(redisDisplay(a.config), "server-password") {
		t.Fatal("bootstrap modified saved configuration or leaked credentials")
	}
}

func TestManagedConnectionFailureDoesNotFallBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
	}))
	defer server.Close()
	a := app{config: config{Redis: "redis://localhost:6379", ControlPlane: &managedclient.Settings{URL: server.URL}}}
	if err := a.resolveManagedRedis(context.Background()); err == nil {
		t.Fatal("authentication failure accepted")
	}
	if a.config.redisFromControlPlane || a.rdb != nil || a.config.Redis != "redis://localhost:6379" {
		t.Fatal("failed bootstrap changed Redis connection")
	}
}

func TestManagedEndpointOverrideDoesNotForwardSavedToken(t *testing.T) {
	for _, explicitToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved-token-is-not-forwarded", true: "explicit-token-is-used"}[explicitToken], func(t *testing.T) {
			wantHeader := ""
			if explicitToken {
				wantHeader = "Bearer new-server-token"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != wantHeader {
					t.Error("override sent credentials belonging to a different server")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"redis_url": "redis://localhost:6379"})
			}))
			defer server.Close()
			file := filepath.Join(t.TempDir(), "config.json")
			raw := []byte(`{"controlPlane":{"url":"https://original.example","token":"original-server-secret"}}`)
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AFS_CONTROL_PLANE_URL", server.URL)
			t.Setenv("AFS_CONTROL_PLANE_TOKEN", "new-server-token")
			if !explicitToken {
				if err := os.Unsetenv("AFS_CONTROL_PLANE_TOKEN"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := readConfig(file, "")
			if err != nil {
				t.Fatal(err)
			}
			a := app{config: cfg}
			if err := a.resolveManagedRedis(context.Background()); err != nil {
				t.Fatal(err)
			}
			if after, err := os.ReadFile(file); err != nil || string(after) != string(raw) {
				t.Fatal("runtime override changed saved credentials")
			}
		})
	}
}

func TestExplicitRedisSelectsStandaloneDespiteManagedEnvironment(t *testing.T) {
	t.Setenv("AFS_CONTROL_PLANE_URL", "invalid endpoint ignored by explicit Redis")
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", "unused-token")
	t.Setenv("AFS_REDIS_PASSWORD", "standalone-password")
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"controlPlane":{"url":"https://control.example"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(file, "redis://localhost:6379/6")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlPlane != nil || buildRedisOptions(cfg, 1).Password != "standalone-password" {
		t.Fatal("explicit standalone selection did not preserve its password override")
	}
}

func TestManagedStatusRemainsOfflineAndShowsControlPlane(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	a := app{config: config{ControlPlane: &managedclient.Settings{URL: "http://127.0.0.1:1"}}}
	for _, command := range []func() error{
		func() error { return a.status(nil) },
		func() error { return a.syncCommandStatus(nil) },
	} {
		out, err := captureStdout(t, command)
		if err != nil || !strings.HasPrefix(out, "CONTROL PLANE: http://127.0.0.1:1") || strings.Contains(out, "invalid URL") {
			t.Fatalf("managed offline status: %q, %v", out, err)
		}
	}
}
