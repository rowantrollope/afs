package main

import (
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/managedclient"
)

func TestManagedMountIdentityAndLabelsSurviveBootstrap(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	cfg := config{ControlPlane: &managedclient.Settings{URL: "https://control.example", Token: "private-token"}}
	record := mountRecord{WorkspaceID: "workspace", SessionID: "human session", User: "user label", Label: "Agent One", ReadOnly: true}
	if err := prepareManagedMount(cfg, &record); err != nil {
		t.Fatal(err)
	}
	if record.SessionName != "human session" || !strings.HasPrefix(record.SessionID, "sess_") || record.AgentID == "" || record.User != "user label" || record.Label != "Agent One" || !record.ReadOnly {
		t.Fatalf("incorrect identity: %+v", record)
	}
	original := record
	if err := prepareManagedMount(cfg, &record); err != nil || record != original {
		t.Fatalf("restart changed identity: %v", err)
	}
	second := mountRecord{WorkspaceID: "workspace"}
	if err := prepareManagedMount(cfg, &second); err != nil {
		t.Fatal(err)
	}
	if second.AgentID != record.AgentID || second.SessionID == record.SessionID {
		t.Fatal("agent identity not stable or session identity reused")
	}
	raw, err := json.Marshal(syncDaemonBootstrap{Config: cfg, Record: record})
	if err != nil {
		t.Fatal(err)
	}
	var restored syncDaemonBootstrap
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Record != record || restored.Config.ControlPlane.Token != "private-token" {
		t.Fatal("private bootstrap lost managed settings")
	}
	registry, _ := json.Marshal(record)
	if strings.Contains(string(registry), "private-token") {
		t.Fatal("management token leaked to registry")
	}
}

func TestManagedSettingsStoredOfflineAndEnvironmentOverrides(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := setConfigValue(file, "controlPlane.url", json.RawMessage(`"http://localhost:8091"`)); err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(file, "controlPlane.token", json.RawMessage(`"saved-token"`)); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("settings are not private")
	}
	t.Setenv("AFS_CONTROL_PLANE_URL", "https://override.example")
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", "env-token")
	cfg, err := readConfig(file, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlPlane.URL != "https://override.example" || cfg.ControlPlane.Token != "env-token" {
		t.Fatal("environment overrides lost")
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), "env-token") {
		t.Fatal("runtime token persisted")
	}
	_, err = captureStdout(t, func() error {
		return configCommand(cliOptions{configPath: file}, []string{"set", "controlPlane.token", "argv-secret"})
	})
	if err == nil || strings.Contains(err.Error(), "argv-secret") {
		t.Fatal("token accepted on argv or exposed")
	}
}

func TestManagedUnavailableIsVisibleInMountStatus(t *testing.T) {
	row := map[string]any{"workspace": "workspace", "sync": &syncStatus{Connected: true, Management: &managedclient.Status{Configured: true, LastError: "control plane unavailable"}}}
	for _, detailed := range []bool{false, true} {
		text := formatMountStatus([]map[string]any{row}, detailed)
		if !strings.Contains(text, "management unavailable") || !strings.Contains(text, "connected") {
			t.Fatalf("status hides independent management failure: %s", text)
		}
	}
}

func TestNativeMountAcceptsSameAttributionOptions(t *testing.T) {
	for _, backend := range []string{"fuse", "nfs"} {
		flags := flag.NewFlagSet("mount", flag.ContinueOnError)
		options := addMountOptions(flags)
		if _, err := parseCommandFlags(flags, []string{"--session", "session", "--agent-id", "agent"}); err != nil {
			t.Fatal(err)
		}
		if err := validateMountOptions(flags, backend); err != nil || options.SessionID != "session" {
			t.Fatalf("native metadata rejected: %v", err)
		}
	}
}

func TestManagedNativeBootstrapKeepsTokenOutOfArgumentsAndEnvironment(t *testing.T) {
	a, markerPath, gate, mountpoint := nativeStartupFixture(t)
	redisURL := a.config.Redis
	server := httptest.NewServer(controlplane.NewHandler(controlplane.NewService(controlplane.NewStore(a.rdb)), controlplane.HandlerOptions{RedisURL: redisURL, AuthToken: "private-bootstrap-token"}))
	defer server.Close()
	// Mount must use credentials from HTTP, independently of local Redis setup.
	a.rdb, a.service = nil, nil
	a.config.Redis = "redis://127.0.0.1:1"
	t.Setenv("AFS_CONTROL_PLANE_URL", "https://environment.example")
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", "environment-secret")
	t.Setenv("AFS_REDIS_URL", "redis://127.0.0.1:1")
	t.Setenv("AFS_REDIS_PASSWORD", "wrong-local-password")
	a.config.ControlPlane = &managedclient.Settings{URL: server.URL, Token: "private-bootstrap-token"}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := captureStdout(t, func() error {
		return a.mount([]string{"startup", mountpoint, "--backend", "fuse", "--session", "descriptive session", "--readonly"})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.rdb.Close()
	marker := waitStartupMarker(t, markerPath)
	if marker.HasManagementEnv || marker.HasRedisEnv || strings.Contains(strings.Join(marker.Args, " "), "secret") || strings.Contains(strings.Join(marker.Args, " "), "private-bootstrap-token") {
		t.Fatal("management settings escaped the private bootstrap")
	}
	if marker.BootstrapMode != 0600 || marker.Bootstrap.Management.Token != "private-bootstrap-token" || marker.Bootstrap.Management.URL != server.URL || marker.Bootstrap.RedisURL != redisURL {
		t.Fatal("private bootstrap lost settings or permissions")
	}
	registration := marker.Bootstrap.Registration
	if !strings.HasPrefix(registration.SessionID, "sess_") || registration.AgentID == "" || registration.SessionName != "descriptive session" || !registration.Readonly {
		t.Fatalf("private bootstrap lost managed mount identity: %+v", registration)
	}
	reg, err := loadMountRegistry()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-bootstrap-token") || strings.Contains(string(raw), "environment-secret") {
		t.Fatal("management credential persisted outside private bootstrap")
	}
}

func TestManagedDaemonEnvironmentExcludesResolvedSettings(t *testing.T) {
	t.Setenv("AFS_CONTROL_PLANE_URL", "https://control.example")
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", "secret")
	t.Setenv("AFS_TEST_RETAINED_ENV", "retained")
	retained := false
	for _, entry := range daemonEnvironment() {
		if strings.HasPrefix(entry, "AFS_CONTROL_PLANE_URL=") || strings.HasPrefix(entry, "AFS_CONTROL_PLANE_TOKEN=") {
			t.Fatal("daemon inherited management settings")
		}
		if entry == "AFS_TEST_RETAINED_ENV=retained" {
			retained = true
		}
	}
	if !retained {
		t.Fatal("unrelated daemon environment was removed")
	}
}
