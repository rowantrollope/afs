//go:build linux

package fuse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	_, err := callFusermountWithHelper(dir, &MountOptions{MountContext: ctx}, utility)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mount utility: %v", err)
	}
	if time.Since(before) > time.Second {
		t.Fatal("mount utility was not joined promptly")
	}
}
