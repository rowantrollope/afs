package fuse

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestMountContextCancelsDescriptorHandoff(t *testing.T) {
	local, remote, err := unixgramSocketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := getConnectionContext(ctx, local); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked fd handoff: %v", err)
	}
}
func TestMountContextDescriptorHandoffSuccess(t *testing.T) {
	local, remote, err := unixgramSocketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	defer remote.Close()
	source, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := syscall.Sendmsg(int(remote.Fd()), []byte{1}, syscall.UnixRights(int(source.Fd())), nil, 0); err != nil {
		t.Fatal(err)
	}
	fd, err := getConnectionContext(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	cancel()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		t.Fatalf("cancellation closed handed-off fd: %v", err)
	}
}
func TestMountContextCancelsReadiness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s := &Server{opts: &MountOptions{MountContext: ctx}, ready: make(chan error)}
	if err := s.WaitMount(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness: %v", err)
	}
}
func TestMountContextCancelsInitialRequest(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitMountReadable(ctx, int(r.Fd())); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial request wait: %v", err)
	}
}

// This subprocess supplies a communication fd before failing, reproducing a
// mount utility that fails after its descriptor handoff but before readiness.
func TestMain(m *testing.M) {
	if os.Getenv("AFS_FUSE_TEST_UTILITY") == "handoff-fail" {
		source, err := os.Open(os.DevNull)
		if err != nil {
			os.Exit(24)
		}
		err = syscall.Sendmsg(3, []byte{1}, syscall.UnixRights(int(source.Fd())), nil, 0)
		if err != nil {
			os.Exit(25)
		}
		os.Exit(23)
	}
	os.Exit(m.Run())
}
