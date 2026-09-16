package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rowantrollope/afs/internal/mountcontrol"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapPrivateRegularFile(t *testing.T) {
	for _, kind := range []string{"good", "public", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			boot := mountcontrol.Bootstrap{RuntimeDir: dir, ReadyPath: filepath.Join(dir, "ready"), Mountpoint: filepath.Join(dir, "mount"), WorkspaceID: "ws", Token: "token", Backend: "nfs"}
			data, _ := json.Marshal(boot)
			path := filepath.Join(dir, "bootstrap")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "public" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				link := filepath.Join(dir, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			got, err := loadBootstrap(path)
			if kind == "good" {
				if err != nil || got.Token != "token" {
					t.Fatalf("bootstrap=%+v, %v", got, err)
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("bootstrap credentials remain")
				}
			} else if err == nil {
				t.Fatal("accepted insecure bootstrap")
			}
		})
	}
}
func TestRuntimeLockExclusiveAndSymlinkSafe(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockRuntime(dir); err == nil {
		other()
		t.Fatal("second runtime owner acquired lock")
	}
	unlock()
	next, err := lockRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	next()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockRuntime(link); err == nil {
		unlock()
		t.Fatal("accepted symlink runtime")
	}
}

type fakeSession struct {
	calls  int
	fail   error
	forced bool
}

func (f *fakeSession) Status(context.Context) mountcontrol.Result {
	return mountcontrol.Result{Success: true, Connected: true}
}
func (f *fakeSession) Flush(context.Context) error { f.calls++; return f.fail }
func (f *fakeSession) Unmount(_ context.Context, force bool) error {
	f.calls++
	f.forced = force
	return f.fail
}
func controlTestCall(t *testing.T, req mountcontrol.Request, f *fakeSession) (mountcontrol.Result, bool) {
	t.Helper()
	server, conn := net.Pipe()
	defer conn.Close()
	stop := make(chan struct{}, 1)
	done := make(chan struct{})
	boot := mountcontrol.Bootstrap{WorkspaceID: "ws", Mountpoint: "/tmp/mount", Token: "secret", Backend: "nfs"}
	go func() { defer close(done); serveControl(server, boot, f, make(chan struct{}, 1), stop) }()
	req.Version = mountcontrol.Version
	req.DeadlineUnixMilli = time.Now().Add(time.Second).UnixMilli()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	var r mountcontrol.Result
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		t.Fatal(err)
	}
	<-done
	select {
	case <-stop:
		return r, true
	default:
		return r, false
	}
}
func TestControlAuthenticatesAndRetainsSessionOnFailure(t *testing.T) {
	base := mountcontrol.Request{Operation: mountcontrol.Unmount, Token: "secret", WorkspaceID: "ws", Mountpoint: "/tmp/mount"}
	bad := base
	bad.Token = "wrong"
	f := &fakeSession{}
	r, stop := controlTestCall(t, bad, f)
	if r.Success || r.Token != "" || stop || f.calls != 0 {
		t.Fatalf("wrong-token request acted: %+v", r)
	}
	f.fail = errors.New("busy")
	r, stop = controlTestCall(t, base, f)
	if r.Success || r.Flushed || stop || f.forced {
		t.Fatalf("failed normal unmount changed lifecycle: %+v", r)
	}
	f.fail = nil
	base.Operation = mountcontrol.Detach
	r, stop = controlTestCall(t, base, f)
	if !r.Success || r.Flushed || !stop || !f.forced {
		t.Fatalf("explicit detach response: %+v", r)
	}
}
