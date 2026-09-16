package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisDisplayEffectiveDatabase(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"redis://localhost", "redis://localhost:6379/0"},
		{"rediss://user:secret@host:6380/4?db=7&read_timeout=1s", "rediss://host:6380/7"},
		{"redis://[::1]:6379/2", "redis://[::1]:6379/2"},
		{"unix://user:secret@/tmp/redis.sock?db=3", "unix:///tmp/redis.sock?db=3"},
	} {
		if got := redisDisplay(config{Redis: tc.raw}); got != tc.want {
			t.Errorf("display = %q, want %q", got, tc.want)
		}
	}
}

func TestStatusDatabaseContextOffline(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"redis":"redis://user:secret@localhost:1/4"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := captureStdout(t, func() error { return runCLI(append([]string{"--config", cfg}, args...)) })
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "secret") {
			t.Fatal("credentials leaked")
		}
		return out
	}
	if got := run("status"); got != "Configured REDIS: redis://localhost:1/4\n\nNo mounts.\n" {
		t.Fatalf("empty status: %q", got)
	}
	rec := mountRecord{LocalPath: root, Redis: "redis://other:6379/7", Workspace: "demo"}
	if err := saveMountRegistry(mountRegistry{Version: mountRegistryVersion, Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	got := run("status")
	if !strings.Contains(got, "Configured REDIS: redis://localhost:1/4") || !strings.Contains(got, rec.Redis) {
		t.Fatalf("mixed context: %s", got)
	}
	if got := run("status", root); !strings.HasPrefix(got, "REDIS: "+rec.Redis+"\n\n") {
		t.Fatalf("targeted status: %s", got)
	}
	if got := run("--json", "status"); strings.Contains(got, "Configured REDIS:") || !strings.HasPrefix(got, "[") {
		t.Fatalf("JSON changed: %s", got)
	}
	if got := run("unmount", root, "--force"); !strings.HasPrefix(got, "REDIS: "+rec.Redis+"\n\n") {
		t.Fatalf("unmount: %s", got)
	}
}
