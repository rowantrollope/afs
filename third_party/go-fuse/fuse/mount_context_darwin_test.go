//go:build darwin

package fuse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMountContextCancelsUtility(t *testing.T) {
	dir := t.TempDir()
	utility := filepath.Join(dir, "mount-helper")
	if err := os.WriteFile(utility, []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	before := time.Now()
	_, err := mountWithHelper(dir, &MountOptions{MountContext: ctx}, make(chan error, 1), utility)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mount utility: %v", err)
	}
	if time.Since(before) > time.Second {
		t.Fatal("mount utility was not joined promptly")
	}
}

func TestMountContextReportsUtilityFailureAfterHandoff(t *testing.T) {
	t.Setenv("AFS_FUSE_TEST_UTILITY", "handoff-fail")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	fd, err := mountWithHelper(t.TempDir(), &MountOptions{MountContext: ctx}, ready, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	select {
	case err := <-ready:
		if err == nil {
			t.Fatal("mount utility failure reported successful readiness")
		}
	case <-ctx.Done():
		t.Fatal("utility was not joined")
	}
}
