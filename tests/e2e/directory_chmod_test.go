//go:build integration

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveDirectoryChmodAfterRename(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	a.run(nil, "create", "directory-modes")
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	a.mount("directory-modes", left)
	b.mount("directory-modes", right)
	write(t, filepath.Join(left, "original", "file"), []byte("rename preserves child bytes"))
	awaitFile(t, filepath.Join(right, "original", "file"), []byte("rename preserves child bytes"))
	if err := os.Rename(filepath.Join(left, "original"), filepath.Join(left, "moved")); err != nil {
		t.Fatal(err)
	}
	awaitFile(t, filepath.Join(right, "moved", "file"), []byte("rename preserves child bytes"))
	eventually(t, 30*time.Second, "old directory removed from peer", func() bool {
		_, err := os.Lstat(filepath.Join(right, "original"))
		return os.IsNotExist(err)
	})
	// No checkpoint, explicit save or remount can conceal missing live chmod
	// propagation. Alternate writers to exercise inbound directory echoes too.
	for _, step := range []struct {
		root string
		mode os.FileMode
	}{{left, 0o750}, {right, 0o700}, {left, 0o755}, {right, 0o710}} {
		if err := os.Chmod(filepath.Join(step.root, "moved"), step.mode); err != nil {
			t.Fatal(err)
		}
		eventually(t, 30*time.Second, "live directory permission convergence", func() bool {
			for _, root := range []string{left, right} {
				info, err := os.Stat(filepath.Join(root, "moved"))
				if err != nil || info.Mode().Perm() != step.mode {
					return false
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx, reader, err := a.publishedReader(ctx, "directory-modes")
			if err != nil {
				return false
			}
			stat, err := reader.Stat(ctx, "/moved")
			return err == nil && stat != nil && stat.Mode == uint32(step.mode)
		})
	}
	awaitFile(t, filepath.Join(left, "moved", "file"), []byte("rename preserves child bytes"))
	awaitFile(t, filepath.Join(right, "moved", "file"), []byte("rename preserves child bytes"))
	a.unmount(left)
	b.unmount(right)
}
