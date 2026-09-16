package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareMountpointPreservesExistingContents(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	created := false
	if err := prepareMountpoint(dir, &created); err == nil {
		t.Fatal("accepted populated mountpoint")
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "keep" || created {
		t.Fatalf("local file changed=%q, %v", got, err)
	}
}
func TestDetachWithoutMountPreservesDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := Detach(context.Background(), "fuse", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("detach deleted unowned directory: %v", err)
	}
}
