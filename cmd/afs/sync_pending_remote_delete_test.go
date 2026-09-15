package main

import (
	"context"
	"testing"
)

func TestSyncFullReconcileDoesNotReplayPendingRemoteDeletion(t *testing.T) {
	env := newSyncTestEnv(t)
	env.writeLocalFile(t, "shared.txt", "previously synchronized bytes")
	d, err := newSyncDaemon(syncDaemonConfig{Workspace: env.workspace, LocalRoot: env.localRoot, FS: env.fsClient, Store: env.store})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.full.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/shared.txt"); err != nil {
		t.Fatal(err)
	}
	dirents, err := loadWorkspaceRootDirents(ctx, env.rdb, env.mountKey, "1")
	if err != nil || dirents["shared.txt"] != "" {
		t.Fatalf("initial deletion was not published: dirents=%v error=%v", dirents, err)
	}
	// Hold the downloader queue: the remote delete has been observed, but
	// its local application has not started when reconnect reconciliation runs.
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/shared.txt"})
	if len(d.reconciler.downloadCh) == 0 {
		t.Fatal("remote deletion was not queued")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	dirents, err = loadWorkspaceRootDirents(ctx, env.rdb, env.mountKey, "1")
	if err != nil {
		t.Fatal(err)
	}
	if inode := dirents["shared.txt"]; inode != "" {
		data, _ := env.rdb.Get(ctx, workspaceRootContentKey(env.mountKey, inode)).Result()
		t.Fatalf("pending inbound delete was re-uploaded: raw Redis inode=%s content=%q", inode, data)
	}
	if env.localExists("shared.txt") {
		t.Fatal("full reconciliation did not apply the remote deletion locally")
	}
}
