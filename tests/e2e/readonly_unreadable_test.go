//go:build integration

package e2e

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlyUnreadableCopyRecoversLive(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	seed := t.TempDir()
	write(t, filepath.Join(seed, "file"), []byte("old bytes"))
	c.run(nil, "create", "unreadable-reader", "--from", seed)
	ctx, remote, err := c.publishedReader(context.Background(), "unreadable-reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Chmod(ctx, "/file", 0); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "reader")
	mountReadOnly(t, c, "unreadable-reader", root)
	abs := filepath.Join(root, "file")
	if _, err := os.ReadFile(abs); !errors.Is(err, fs.ErrPermission) {
		t.Skip("requires a user subject to local read permissions")
	}
	if err := remote.Echo(ctx, "/file", []byte("new published bytes")); err != nil {
		t.Fatal(err)
	}
	if err := remote.Chmod(ctx, "/file", 0o600); err != nil {
		t.Fatal(err)
	}
	// No checkpoint, manual reconciliation or remount may rescue this update.
	eventually(t, 15*time.Second, "live recovery of unreadable reader copy", func() bool {
		data, err := os.ReadFile(abs)
		info, statErr := os.Stat(abs)
		return err == nil && statErr == nil && string(data) == "new published bytes" && info.Mode().Perm() == 0o400
	})
	unmountReadOnly(t, c, root)
}
