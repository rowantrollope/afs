package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

func replaceTestSyncRoot(t *testing.T, root string) {
	t.Helper()
	moved := filepath.Join(t.TempDir(), "original-root")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSyncReplacedRootPreservesPublishedTree(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "protected.txt", "published original")
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	baseline := d.Snapshot().Entries["protected.txt"]
	replaceTestSyncRoot(t, env.localRoot)
	env.writeLocalFile(t, "unrelated.txt", "unrelated local tree")
	if err := d.full.warmStart(context.Background(), nil); err == nil {
		t.Fatal("replacement root was accepted for warm recovery")
	}
	d.reconciler.handleLocalDelete(context.Background(), "protected.txt", "remove")
	if d.Snapshot().Entries["protected.txt"].Deleted {
		t.Fatal("replacement root created a deletion tombstone")
	}
	results := make(chan uploadResult, 1)
	d.uploader.results = results
	d.uploader.processDelete(context.Background(), uploadOp{Kind: opUploadDelete, Path: "protected.txt", StoredEntry: baseline, HasStored: true})
	if result := <-results; result.Err == nil {
		t.Fatal("queued deletion accepted replacement root")
	}
	if _, err := scanSyncSaveLocal(context.Background(), d.reconciler); err == nil {
		t.Fatal("save accepted replacement root")
	}
	if got := env.readRemoteFile(t, "protected.txt"); got != "published original" {
		t.Fatalf("published bytes changed: %q", got)
	}
	if env.remoteExists(t, "unrelated.txt") {
		t.Fatal("replacement root published unrelated contents")
	}
}

func TestSyncReplacedRootRejectsQueuedLocalDeletion(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "protected.txt", "published original")
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	baseline := d.Snapshot().Entries["protected.txt"]
	replaceTestSyncRoot(t, env.localRoot)
	abs := env.writeLocalFile(t, "protected.txt", "published original")
	results := make(chan downloadResult, 1)
	d.downloader.results = results
	d.downloader.processDelete(context.Background(), downloadOp{Kind: opDownloadDelete, Path: "protected.txt", AbsPath: abs, StoredEntry: baseline, HasStored: true})
	if result := <-results; result.Err == nil || result.ConflictPath != "" {
		t.Fatalf("queued local deletion used replacement root: %+v", result)
	}
	if got := env.readLocalFile(t, "protected.txt"); got != "published original" {
		t.Fatalf("replacement local bytes changed: %q", got)
	}
}

// Reproduced against upstream c3897ac: an absent root became an empty scan,
// then detectOfflineDeletes removed the intact published remote tree.
func TestSyncMissingRootDoesNotDeleteRemote(t *testing.T) {
	env := newSyncTestEnv(t)
	env.writeRemoteFile(t, "protected.txt", "original remote content")
	d, err := newSyncDaemon(syncDaemonConfig{Workspace: env.workspace, LocalRoot: env.localRoot, FS: env.fsClient, Store: env.store})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(env.localRoot, env.localRoot+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(env.localRoot+"-moved", env.localRoot) })
	d.reconciler.handleLocalDelete(context.Background(), "protected.txt", "remove")
	if d.Snapshot().Entries["protected.txt"].Deleted {
		t.Fatal("missing root event created a tombstone")
	}
	if err := d.full.warmStart(context.Background(), nil); err == nil {
		t.Fatal("missing root must fail reconciliation")
	}
	if !env.remoteExists(t, "protected.txt") {
		t.Fatal("missing root deleted published content")
	}
	if got := env.readRemoteFile(t, "protected.txt"); got != "original remote content" {
		t.Fatalf("remote content = %q", got)
	}
	if d.Snapshot().Entries["protected.txt"].Deleted {
		t.Fatal("missing root changed sync baseline to a tombstone")
	}
}

func TestSyncInitialHiddenTreeIsUploaded(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeLocalFile(t, ".venv/package.py", "hidden local source")
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readLocalFile(t, ".venv/package.py"); got != "hidden local source" {
		t.Fatalf("local hidden tree = %q", got)
	}
	if got := env.readRemoteFile(t, ".venv/package.py"); got != "hidden local source" {
		t.Fatalf("remote hidden tree = %q", got)
	}
}

func TestSyncColdHydrationPreservesEditsDuringManifestRead(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "remote.txt", "remote source")
	read, release := make(chan struct{}), make(chan struct{})
	var gate, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	result := make(chan error, 1)
	go func() {
		result <- d.full.coldStart(context.Background(), func(_, _ int64) {
			gate.Do(func() { close(read); <-release })
		})
	}()
	select {
	case <-read:
	case <-time.After(3 * time.Second):
		t.Fatal("manifest read did not reach barrier")
	}
	env.writeLocalFile(t, ".venv/new.py", "new local edit")
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cold hydration accepted newly populated local root")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cold hydration did not leave barrier")
	}
	if got := env.readLocalFile(t, ".venv/new.py"); got != "new local edit" {
		t.Fatalf("local edit = %q", got)
	}
	if env.localExists("remote.txt") {
		t.Fatal("cold hydration replaced root after a local edit appeared")
	}
}

func TestSyncManagedRootReplacementAlwaysPreservesUnpublishedLocalEdits(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "protected.txt", "published remote bytes")
	env.writeLocalFile(t, "protected.txt", "unpublished local bytes")
	d.full.requireRemountOnRootReplace = true
	if err := d.full.replaceFromRemote(context.Background(), nil); !errors.Is(err, client.ErrWorkspaceChanged) {
		t.Fatalf("managed root replacement = %v", err)
	}
	if got := env.readLocalFile(t, "protected.txt"); got != "unpublished local bytes" {
		t.Fatalf("local edit overwritten: %q", got)
	}
}

func TestSyncRestoredGenerationPreservesUnpublishedLocalEdits(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "protected.txt", "original")
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	env.writeLocalFile(t, "protected.txt", "unpublished local edit")
	key := controlplane.WorkspaceGenerationKey(env.mountKey)
	if err := env.rdb.Set(context.Background(), key, "after-restore", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx := client.WithWorkspaceGeneration(context.Background(), "before-restore")
	if err := d.full.replaceFromRemote(ctx, nil); !errors.Is(err, client.ErrWorkspaceChanged) {
		t.Fatalf("stale root replacement = %v", err)
	}
	if err := d.full.warmStart(ctx, nil); !errors.Is(err, client.ErrWorkspaceChanged) {
		t.Fatalf("stale warm reconciliation = %v", err)
	}
	if got := env.readLocalFile(t, "protected.txt"); got != "unpublished local edit" {
		t.Fatalf("unpublished local data = %q", got)
	}
	if got := env.readRemoteFile(t, "protected.txt"); got != "original" {
		t.Fatalf("stale client republished local data: %q", got)
	}
}

func TestSyncUnreadableRootDoesNotDeleteRemote(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	env := newSyncTestEnv(t)
	env.writeRemoteFile(t, "protected.txt", "original remote content")
	d, err := newSyncDaemon(syncDaemonConfig{Workspace: env.workspace, LocalRoot: env.localRoot, FS: env.fsClient, Store: env.store})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(env.localRoot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(env.localRoot, 0755) })
	if err := d.full.warmStart(context.Background(), nil); err == nil {
		t.Fatal("unreadable root must fail reconciliation")
	}
	if !env.remoteExists(t, "protected.txt") {
		t.Fatal("unreadable root deleted published content")
	}
	if d.Snapshot().Entries["protected.txt"].Deleted {
		t.Fatal("unreadable root changed sync baseline to a tombstone")
	}
}
