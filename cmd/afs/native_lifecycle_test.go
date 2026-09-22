package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/mountcontrol"
)

func nativeControlFixture(t *testing.T, rec mountRecord, reply func(mountcontrol.Request, *mountcontrol.Result)) {
	t.Helper()
	if err := os.MkdirAll(rec.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(rec.RuntimeDir, "owner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() })
	socket := mountcontrol.SocketPath(rec.RuntimeDir)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req mountcontrol.Request
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if json.NewDecoder(conn).Decode(&req) == nil {
				result := mountcontrol.Result{Version: req.Version, Operation: req.Operation, Token: req.Token,
					WorkspaceID: req.WorkspaceID, Mountpoint: req.Mountpoint, Backend: rec.Backend,
					Success: true, Flushed: true, Connected: true}
				if reply != nil {
					reply(req, &result)
				}
				_ = json.NewEncoder(conn).Encode(result)
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done; _ = os.Remove(socket) })
}

func testNativeRecord(t *testing.T) mountRecord {
	t.Helper()
	return mountRecord{Backend: "fuse", Workspace: "native", WorkspaceID: "ws_native",
		LocalPath: filepath.Join(t.TempDir(), "unavailable-mountpoint"), RuntimeDir: t.TempDir(), Token: "private-control-capability"}
}

func TestNativeLifecycleControlStaysOutsideMountpoint(t *testing.T) {
	rec := testNativeRecord(t)
	nativeControlFixture(t, rec, nil)
	if owned, err := mountOwned(rec); err != nil || !owned {
		t.Fatalf("ownership %v, %v", owned, err)
	}
	for _, op := range []string{"status", "flush"} {
		if _, err := callNativeMount(rec, op, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(rec.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("native control touched inaccessible mountpoint: %v", err)
	}
	row := map[string]any{}
	nativeMountStatus(rec, row)
	raw, _ := json.Marshal(row)
	if row["state"] != "running" || strings.Contains(string(raw), rec.Token) {
		t.Fatalf("native status unavailable or leaked capability: %s", raw)
	}
}

func TestNativeFlushRequiresVerifiedReceipt(t *testing.T) {
	for _, failure := range []string{"no-receipt", "wrong-identity", "backend-error"} {
		t.Run(failure, func(t *testing.T) {
			rec := testNativeRecord(t)
			nativeControlFixture(t, rec, func(req mountcontrol.Request, result *mountcontrol.Result) {
				switch failure {
				case "no-receipt":
					result.Flushed = false
				case "wrong-identity":
					result.WorkspaceID = "another-workspace"
				case "backend-error":
					result.Success, result.Error = false, "kernel flush failed"
				}
			})
			if _, err := callNativeMount(rec, "flush", time.Second); err == nil {
				t.Fatal("unverified flush accepted")
			}
			if owned, _ := mountOwned(rec); !owned {
				t.Fatal("failed flush stopped the native owner")
			}
		})
	}
}

func TestNativeCheckpointAndRestoreUseSharedRegistry(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	a := &app{config: config{Redis: "redis://" + server.Addr()}, rdb: rdb,
		service: controlplane.NewService(controlplane.NewStore(rdb))}
	ctx := context.Background()
	meta, err := a.service.CreateWorkspace(ctx, "native")
	if err != nil {
		t.Fatal(err)
	}
	rec := testNativeRecord(t)
	rec.WorkspaceID, rec.RedisIdentity = meta.ID, redisIdentity(a.config)
	requests := make(chan string, 8)
	nativeControlFixture(t, rec, func(req mountcontrol.Request, result *mountcontrol.Result) { requests <- req.Operation })
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	if err := a.requireUnmounted(ctx, "native"); err == nil {
		t.Fatal("native mount bypassed restore/delete guard")
	}
	if err := a.flushLocalMounts(ctx, "native"); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-requests:
		if op != "flush" {
			t.Fatalf("checkpoint requested %q", op)
		}
	default:
		t.Fatal("checkpoint did not flush native helper")
	}
	if _, err := os.Lstat(rec.LocalPath); !os.IsNotExist(err) {
		t.Fatal("checkpoint scanned native mountpoint as a sync root")
	}
}

func TestNativeUnmountFailurePreservesRegistration(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	rec := testNativeRecord(t)
	nativeControlFixture(t, rec, func(req mountcontrol.Request, result *mountcontrol.Result) {
		if req.Operation != "unmount" {
			result.Success, result.Error = false, "unexpected operation: "+req.Operation
			return
		}
		result.Success, result.Error = false, "filesystem busy"
	})
	reg := mountRegistry{Mounts: []mountRecord{rec}}
	if err := saveMountRegistry(reg); err != nil {
		t.Fatal(err)
	}
	a := &app{options: cliOptions{json: true}}
	if err := a.unmount([]string{rec.Workspace}); err == nil || !strings.Contains(err.Error(), "filesystem busy") {
		t.Fatalf("workspace name did not reach native unmount: %v", err)
	}
	loaded, err := loadMountRegistry()
	if err != nil || len(loaded.Mounts) != 1 || len(reg.Mounts) != 1 {
		t.Fatalf("lost live mount registration: %v", err)
	}
	if owned, _ := mountOwned(rec); !owned {
		t.Fatal("busy unmount stopped helper")
	}
}

func TestNativeMountRejectsPopulatedDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "keep")
	if err := os.WriteFile(path, []byte("local user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeMountpoint(root); err == nil {
		t.Fatal("populated mountpoint accepted")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "local user data" {
		t.Fatal("local bytes changed")
	}
}
