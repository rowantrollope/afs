package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncSavePortableSymlinkMode(t *testing.T) {
	for _, baselineMode := range []uint32{0, 0o755, 0o777} {
		env := newSyncTestEnv(t)
		ctx := context.Background()
		if err := os.Symlink("target", filepath.Join(env.localRoot, "pointer")); err != nil {
			t.Fatal(err)
		}
		if err := env.fsClient.Ln(ctx, "target", "/pointer"); err != nil {
			t.Fatal(err)
		}
		// Simulate a link saved by the other OS, independent of this host.
		if err := env.fsClient.Chmod(ctx, "/pointer", 0o751); err != nil {
			t.Fatal(err)
		}
		r := newSyncSaveTestReconciler(t, env)
		r.state.state.Entries["pointer"] = SyncEntry{Type: "symlink", Target: "target", Mode: baselineMode}
		requireSyncSave(t, r)
		stat, err := env.fsClient.Stat(ctx, "/pointer")
		if err != nil || stat.Mode != 0o751 {
			t.Fatalf("save changed native symlink metadata: %+v, %v", stat, err)
		}
		if r.state.state.Entries["pointer"].Mode != 0 {
			t.Fatal("saved nonportable symlink mode")
		}
		if err := env.fsClient.Rm(ctx, "/pointer"); err != nil {
			t.Fatal(err)
		}
		if err := env.fsClient.Ln(ctx, "peer-target", "/pointer"); err != nil {
			t.Fatal(err)
		}
		if _, err := scanAndSaveSyncTree(ctx, r); err == nil {
			t.Fatal("save overwrote a peer's new symlink target")
		}
	}
}

func TestSyncLocalDirectoryChmodAfterDownloadEcho(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	if err := env.fsClient.Mkdir(ctx, "/directory"); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(env.localRoot, "directory")
	if err := os.Chmod(abs, 0o750); err != nil {
		t.Fatal(err)
	}
	// warmStart left a directory echo; it must not hide the application's chmod.
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
	select {
	case <-d.reconciler.fullSweepRequest:
	default:
		t.Fatal("local directory chmod did not schedule reconciliation")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stat, err := env.fsClient.Stat(ctx, "/directory")
	if err != nil || stat.Mode != 0o750 {
		t.Fatalf("remote mode = %+v, %v", stat, err)
	}
}

func TestSyncSaturatedQueuesDoNotBlockReconciliation(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	old := env.writeLocalFile(t, "old", "renamed bytes")
	env.writeLocalFile(t, "incoming", "peer will delete")
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, filepath.Join(env.localRoot, "new")); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/incoming"); err != nil {
		t.Fatal(err)
	}
	r := d.reconciler
	stop := make(chan struct{})
	r.stopCh = stop
	defer close(stop)
	for i := 0; i < cap(r.uploadCh); i++ {
		r.uploadCh <- uploadOp{}
	}
	for i := 0; i < cap(r.downloadCh); i++ {
		r.downloadCh <- downloadOp{}
	}
	// Workers cannot progress if their results and the work queues are full.
	for i := 0; i < cap(r.uploadResCh); i++ {
		r.uploadResCh <- uploadResult{}
	}
	for i := 0; i < cap(r.downloadResCh); i++ {
		r.downloadResCh <- downloadResult{}
	}
	version := r.installPendingRename("new", "", r.state.state.Entries["old"])
	done := make(chan struct{})
	go func() {
		r.enqueueTrackedUpload(uploadOp{Kind: opUploadRename, Path: "new", PrevPath: "old", RenameVersion: version})
		r.handleRemoteEvent(ctx, remoteEvent{Path: "/incoming"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full queues deadlocked the result consumer")
	}
	if len(r.pendingUploads) != 0 {
		t.Fatalf("deferred work retained pending counts: %+v", r.pendingUploads)
	}
	if _, exists := r.state.state.Entries["new"]; exists {
		t.Fatal("unpublished rename retained a false remote baseline")
	}
	select {
	case <-r.fullSweepRequest:
	default:
		t.Fatal("deferred operations did not request recovery")
	}
	// The overflow retains enough baseline to recover without another event.
	for len(r.uploadCh) > 0 {
		<-r.uploadCh
	}
	for len(r.downloadCh) > 0 {
		<-r.downloadCh
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readRemoteFile(t, "new"); got != "renamed bytes" {
		t.Fatalf("rename lost: %q", got)
	}
	if env.remoteExists(t, "old") {
		t.Fatal("old rename path survived recovery")
	}
	if _, err := os.Lstat(filepath.Join(env.localRoot, "incoming")); !os.IsNotExist(err) {
		t.Fatalf("deferred peer deletion lost: %v", err)
	}
}

func TestSyncPendingUploadCoalescesAndRescansLatestEdit(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	env.writeLocalFile(t, "file", "first version queued")
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "file", KindHint: "create"})
	env.writeLocalFile(t, "file", "newest version before worker runs")
	for i := 0; i < 300; i++ {
		d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "file", KindHint: "write"})
	}
	if len(d.reconciler.uploadCh) != 1 {
		t.Fatalf("duplicate events filled upload queue: %d", len(d.reconciler.uploadCh))
	}
	d.uploader.process(ctx, nextDiagnosticUpload(t, d))
	d.reconciler.handleUploadResult(ctx, <-d.reconciler.uploadResCh)
	select {
	case <-d.reconciler.fullSweepRequest:
	default:
		t.Fatal("coalesced edit did not schedule recovery")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readRemoteFile(t, "file"); got != "newest version before worker runs" {
		t.Fatalf("coalesced edit lost: %q", got)
	}
}

func TestSyncDirectoryPlanRejectsNewerLocalChmod(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	if err := env.fsClient.Mkdir(ctx, "/directory"); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(env.localRoot, "directory")
	if err := os.Chmod(abs, 0o750); err != nil {
		t.Fatal(err)
	}
	local, err := d.full.scanLocalMeta()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := d.full.scanRemoteMeta(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := d.full.buildPlan(ctx, local, remote)
	if len(plan) != 1 || plan[0].kind != "mkdir-remote" {
		t.Fatalf("plan: %+v", plan)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.full.executePlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	stat, err := env.fsClient.Stat(ctx, "/directory")
	if err != nil || stat.Mode != 0o755 {
		t.Fatalf("stale chmod was published: %+v, %v", stat, err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stat, err = env.fsClient.Stat(ctx, "/directory")
	if err != nil || stat.Mode != 0o700 {
		t.Fatalf("newer chmod lost: %+v, %v", stat, err)
	}
}
