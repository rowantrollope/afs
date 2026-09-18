package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/mountcontrol"
)

type startupHelperMarker struct {
	PID              int
	Bootstrap        mountcontrol.Bootstrap
	Args             []string
	HasManagementEnv bool
	HasRedisEnv      bool
	BootstrapMode    os.FileMode
}

// The detached test helper owns the normal runtime lock but waits before
// readiness. It never starts a kernel mount or accesses a user Redis instance.
func TestNativeStartupSubprocess(t *testing.T) {
	switch os.Getenv("AFS_TEST_NATIVE_STARTUP_ROLE") {
	case "initiator":
		a := &app{config: config{Redis: os.Getenv("AFS_TEST_NATIVE_STARTUP_REDIS")}}
		if err := a.mountNative("startup", os.Getenv("AFS_TEST_NATIVE_STARTUP_MOUNT"), "fuse", false); err != nil {
			t.Fatal(err)
		}
	case "helper":
		raw, err := os.ReadFile(os.Getenv(mountcontrol.BootstrapEnv))
		if err != nil {
			t.Fatal(err)
		}
		var boot mountcontrol.Bootstrap
		if err := json.Unmarshal(raw, &boot); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("AFS_TEST_NATIVE_STARTUP_FAIL") == "1" {
			if err := os.WriteFile(boot.ReadyPath, []byte(`{"error":"test helper failed after launch"}`), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		lock, err := os.OpenFile(filepath.Join(boot.RuntimeDir, "owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(os.Getenv(mountcontrol.BootstrapEnv))
		if err != nil {
			t.Fatal(err)
		}
		_, hasURL := os.LookupEnv("AFS_CONTROL_PLANE_URL")
		_, hasToken := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN")
		_, hasRedisURL := os.LookupEnv("AFS_REDIS_URL")
		_, hasRedisPassword := os.LookupEnv("AFS_REDIS_PASSWORD")
		marker, _ := json.Marshal(startupHelperMarker{PID: os.Getpid(), Bootstrap: boot, Args: os.Args, HasManagementEnv: hasURL || hasToken, HasRedisEnv: hasRedisURL || hasRedisPassword, BootstrapMode: info.Mode().Perm()})
		if err := os.WriteFile(os.Getenv("AFS_TEST_NATIVE_STARTUP_MARKER"), marker, 0600); err != nil {
			t.Fatal(err)
		}
		gate := os.Getenv("AFS_TEST_NATIVE_STARTUP_GATE")
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(boot.ReadyPath, []byte(`{"ready":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
}

func nativeStartupFixture(t *testing.T) (*app, string, string, string) {
	t.Helper()
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	a := &app{config: config{Redis: "redis://" + server.Addr()}, rdb: rdb, service: controlplane.NewService(controlplane.NewStore(rdb))}
	if _, err := a.service.CreateWorkspace(context.Background(), "startup"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker, gate, mountpoint := filepath.Join(dir, "started.json"), filepath.Join(dir, "continue"), filepath.Join(dir, "mountpoint")
	mountpoint, err := normalizeMountPath(mountpoint)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "fake-afsmount")
	script := "#!/bin/sh\nAFS_TEST_NATIVE_STARTUP_ROLE=helper exec \"$AFS_TEST_NATIVE_STARTUP_BINARY\" -test.run='^TestNativeStartupSubprocess$'\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"AFS_NATIVE_HELPER": helper, "AFS_TEST_NATIVE_STARTUP_BINARY": binary, "AFS_TEST_NATIVE_STARTUP_MARKER": marker, "AFS_TEST_NATIVE_STARTUP_GATE": gate, "AFS_TEST_NATIVE_STARTUP_MOUNT": mountpoint, "AFS_TEST_NATIVE_STARTUP_REDIS": a.config.Redis} {
		t.Setenv(key, value)
	}
	return a, marker, gate, mountpoint
}

func waitStartupMarker(t *testing.T, path string) startupHelperMarker {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var marker startupHelperMarker
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &marker) == nil && marker.PID > 0 {
			t.Cleanup(func() {
				if p, err := os.FindProcess(marker.PID); err == nil {
					_ = p.Kill()
				}
			})
			return marker
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached test helper did not acquire ownership")
	return startupHelperMarker{}
}

func TestNativeStartupIntentSurvivesInitiatorKill(t *testing.T) {
	_, markerPath, _, mountpoint := nativeStartupFixture(t)
	binary := os.Getenv("AFS_TEST_NATIVE_STARTUP_BINARY")
	cmd := exec.Command(binary, "-test.run=^TestNativeStartupSubprocess$")
	cmd.Env = append(os.Environ(), "AFS_TEST_NATIVE_STARTUP_ROLE=initiator")
	log, err := os.Create(filepath.Join(t.TempDir(), "initiator.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	marker := waitStartupMarker(t, markerPath)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reg, err := loadMountRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := mountByPath(reg, mountpoint)
	if !ok {
		t.Fatal("killed initiator left an owned helper without recoverable mount registration")
	}
	if rec.Token != marker.Bootstrap.Token || rec.WorkspaceID != marker.Bootstrap.WorkspaceID || rec.RuntimeDir != marker.Bootstrap.RuntimeDir || rec.Generation != marker.Bootstrap.Generation {
		t.Fatal("interrupted registration lost helper control identity")
	}
	if rec.PID != 0 && rec.PID != marker.PID {
		t.Fatalf("wrong helper PID: %d, want %d or pending zero", rec.PID, marker.PID)
	}
	if owned, err := mountOwned(rec); err != nil || !owned {
		t.Fatalf("interrupted helper not recoverable through ownership: %v %v", owned, err)
	}
}

func TestNativeStartupPIDRecordedBeforeReady(t *testing.T) {
	a, markerPath, gate, mountpoint := nativeStartupFixture(t)
	result := make(chan error, 1)
	go func() { result <- a.mountNative("startup", mountpoint, "fuse", false) }()
	marker := waitStartupMarker(t, markerPath)
	// Always release a still-running initiating goroutine before its test fixture
	// is removed, even when the pre-readiness assertion fails.
	defer func() {
		_ = os.WriteFile(gate, nil, 0600)
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("mount completion: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("initiator did not complete")
		}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reg, err := loadMountRegistry()
		if err != nil {
			t.Fatal(err)
		}
		if rec, ok := mountByPath(reg, mountpoint); ok && rec.PID == marker.PID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper PID was not durably registered while readiness remained pending")
}

func TestNativeStartupClearsIntentWhenExecNeverStarted(t *testing.T) {
	a, _, _, mountpoint := nativeStartupFixture(t)
	t.Setenv("AFS_NATIVE_HELPER", filepath.Join(t.TempDir(), "absent-helper"))
	if err := a.mountNative("startup", mountpoint, "fuse", false); err == nil {
		t.Fatal("missing helper accepted")
	}
	reg, err := loadMountRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Mounts) != 0 {
		t.Fatalf("helper never started but left %d stale mount intents", len(reg.Mounts))
	}
}

func TestNativeStartupRetainsIntentAfterStartedHelperFails(t *testing.T) {
	a, _, _, mountpoint := nativeStartupFixture(t)
	t.Setenv("AFS_TEST_NATIVE_STARTUP_FAIL", "1")
	if err := a.mountNative("startup", mountpoint, "fuse", false); err == nil {
		t.Fatal("failed helper accepted")
	}
	reg, err := loadMountRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := mountByPath(reg, mountpoint)
	if !ok || rec.PID <= 0 || rec.Token == "" || rec.RuntimeDir == "" {
		t.Fatal("started helper failure lost its recoverable identity")
	}
}
