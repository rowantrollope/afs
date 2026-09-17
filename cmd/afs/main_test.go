package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndVersionDoNotLoadRedisOrConfiguration(t *testing.T) {
	for _, args := range [][]string{{"--config", "/does/not/exist", "--redis", "not-a-url", "--help"}, {"--config", "/does/not/exist", "create", "--help"}, {"--redis", "invalid", "--version"}} {
		out, err := captureStdout(t, func() error { return runCLI(args) })
		if err != nil || out == "" {
			t.Fatalf("%v: output %q error %v", args, out, err)
		}
	}
	out, _ := captureStdout(t, func() error { return runCLI([]string{"--help"}) })
	for _, excluded := range []string{"vol ", "\n  ws ", "\n  fs ", "\n  recover ", "\n  versioning ", "\n  file ", "\n  serve ", "mcp", "auth", "query", "cloud"} {
		if strings.Contains(out, excluded) {
			t.Errorf("root help contains excluded surface %q", excluded)
		}
	}
}

func TestRedisConfigurationPrecedenceAndRedaction(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"redis":"rediss://user:secret@host.example:6380/4","sync":{"fileSizeCapMB":512,"watcherQueueCapacity":2048}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(file, "")
	if err != nil {
		t.Fatal(err)
	}
	opts := buildRedisOptions(cfg, 8)
	if opts.Username != "user" || opts.Password != "secret" || opts.DB != 4 || opts.TLSConfig == nil || cfg.SyncWatcherQueueCapacity != 2048 || cfg.SyncFileSizeCapMB != 512 {
		t.Fatal("Redis URL authentication, TLS, database, or retained sync config was lost")
	}
	if got := redisDisplay(cfg); strings.Contains(got, "user") || strings.Contains(got, "secret") {
		t.Fatalf("display leaked credentials: %s", got)
	}
	if got := redisConnectionError(cfg).Error(); strings.Contains(got, "secret") {
		t.Fatalf("error leaked password: %s", got)
	}
	cfg, err = readConfig(file, "redis://localhost:12345/6")
	if err != nil {
		t.Fatal(err)
	}
	if opts := buildRedisOptions(cfg, 1); opts.Addr != "localhost:12345" || opts.DB != 6 || opts.Password != "" || opts.TLSConfig != nil {
		t.Fatal("--redis did not fully override configured connection")
	}
	_, err = readConfig(file, "redis://secret:password@/bad-db")
	if err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("invalid URI error = %v", err)
	}
}

func TestLocalMountDatabaseIdentity(t *testing.T) {
	want := redisIdentity(config{Redis: "redis://localhost:6379/0"})
	for _, url := range []string{"redis://LOCALHOST", "redis://user:secret@localhost:6379/0", "rediss://localhost:6379/0?read_timeout=1s"} {
		if got := redisIdentity(config{Redis: url}); got != want {
			t.Errorf("equivalent endpoint %q did not match local mounts", url)
		}
	}
	if got := redisIdentity(config{Redis: "redis://localhost:6379/1"}); got == want {
		t.Fatal("different Redis databases share local mount identity")
	}
}

func TestGlobalOptions(t *testing.T) {
	opts, args, err := parseGlobalOptions([]string{"info", "w", "--redis=redis://localhost:6379/8", "--json"})
	if err != nil || len(args) != 2 || !opts.json || opts.redisURL != "redis://localhost:6379/8" {
		t.Fatalf("opts=%+v args=%v err=%v", opts, args, err)
	}
}

func TestRemovedCommandGroupsFailBeforeConfigOrRedis(t *testing.T) {
	for _, group := range []string{"fs", "ws", "recover", "versioning", "file", "serve"} {
		for _, args := range [][]string{{group}, {group, "--help"}, {group, "create", "demo"}} {
			args = append([]string{"--config", "/does/not/exist", "--redis", "invalid"}, args...)
			out, err := captureStdout(t, func() error { return runCLI(args) })
			if err == nil || !strings.Contains(err.Error(), "unknown command") || out != "" {
				t.Fatalf("removed command %v: output=%q error=%v", args, out, err)
			}
		}
	}
}

func TestLocalRegistryCannotSignalUnrelatedPID(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	root, normalizeErr := normalizeMountPath(t.TempDir())
	if normalizeErr != nil {
		t.Fatal(normalizeErr)
	}
	rec := mountRecord{ID: "stale", Workspace: "w", WorkspaceID: "ws", LocalPath: root, PID: os.Getpid(), Token: "obsolete"}
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	a := app{options: cliOptions{json: true}}
	_, err := captureStdout(t, func() error { return a.unmount([]string{root, "--force"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("test process was signaled")
	}
	reg, err := loadMountRegistry()
	if err != nil || len(reg.Mounts) != 0 {
		t.Fatalf("stale registration remains: %v %v", reg, err)
	}
}

func TestRootOwnershipAndControlSymlinks(t *testing.T) {
	root := t.TempDir()
	release, err := claimSyncRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = claimSyncRoot(root); err == nil {
		t.Fatal("second owner accepted")
	}
	if owned, err := syncRootOwned(root); err != nil || !owned {
		t.Fatalf("ownership: %v %v", owned, err)
	}
	other := t.TempDir()
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(other, syncControlDirName)); err != nil {
		t.Fatal(err)
	}
	if err = prepareSyncControlDirs(other); err == nil {
		t.Fatal("symlink control directory accepted")
	}
}

func TestColdMaterializationPreservesRootOwnership(t *testing.T) {
	root := t.TempDir()
	release, err := claimSyncRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, err := os.Stat(filepath.Join(root, syncControlDirName, "owner.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	_, err = materializeManifestToDirectory(root, manifest{Entries: map[string]manifestEntry{"/": {Type: "dir", Mode: 0o755}, "/file": {Type: "file", Mode: 0o644, Inline: "eA=="}}}, nil, manifestMaterializeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(root, syncControlDirName, "owner.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("materialization replaced owner lock")
	}
	if owned, err := syncRootOwned(root); err != nil || !owned {
		t.Fatalf("ownership lost: %v %v", owned, err)
	}
}

func TestMountRejectsReplacedEstablishedRootBeforeConnecting(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	if err := writeSyncControlJSON(filepath.Join(runtimeDir, "mounted"), successfulMountState{Generation: "g_test", RootIdentity: localFileIdentityFromPath(root)}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "unrelated")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := serveSyncDaemon(syncDaemonBootstrap{Record: mountRecord{LocalPath: root, RuntimeDir: runtimeDir, WorkspaceID: "ws", Token: "token", Generation: "g_test"}})
	if err == nil || !strings.Contains(err.Error(), "local sync root was replaced") {
		t.Fatalf("expected root identity rejection before Redis setup, got %v", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
		t.Fatalf("unrelated content changed: %q %v", data, err)
	}
}
