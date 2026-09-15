package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/mount/client"
)

// A symlink created under a platform's umask can have different permissions
// from Redis's default 0777. Saving it must not publish a transient deletion
// that another writer could observe and propagate as a local removal.
func TestSyncSaveSymlinkModeChangeRetainsRemoteEntry(t *testing.T) {
	env := newSyncTestEnv(t)
	ctx := context.Background()
	const rel = "pointer"
	const target = "missing-target"
	if err := os.Symlink(target, filepath.Join(env.localRoot, rel)); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Ln(ctx, target, "/"+rel); err != nil {
		t.Fatal(err)
	}
	r := newSyncSaveTestReconciler(t, env)
	local, err := scanSyncSaveLocal(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// Force a mode difference independently of the host's symlink semantics.
	remoteMode := local[rel].Mode ^ 0o020
	if err := env.fsClient.Chmod(ctx, "/"+rel, remoteMode); err != nil {
		t.Fatal(err)
	}
	r.state.state.Entries[rel] = SyncEntry{Type: "symlink", Mode: remoteMode, Target: target}
	r.fs = &saveSymlinkNoRemoveClient{Client: env.fsClient}
	requireSyncSave(t, r)
	if got, err := env.fsClient.Readlink(ctx, "/"+rel); err != nil || got != target {
		t.Fatalf("remote target = %q, error %v", got, err)
	}
	stat, err := env.fsClient.Stat(ctx, "/"+rel)
	if err != nil || stat == nil || stat.Mode != local[rel].Mode {
		t.Fatalf("remote stat = %+v, error %v; want mode %o", stat, err, local[rel].Mode)
	}
}

type saveSymlinkNoRemoveClient struct{ client.Client }

func (c *saveSymlinkNoRemoveClient) Rm(context.Context, string) error {
	return fmt.Errorf("mode-only save removed the remote symlink")
}
