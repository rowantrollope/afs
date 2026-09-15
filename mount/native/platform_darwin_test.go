//go:build darwin

package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinFlushIncludesDirectoriesAndSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "file"), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent-flush-target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := flushMount(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := flushMount(ctx, dir); err == nil {
		t.Fatal("cancelled flush succeeded")
	}
}
