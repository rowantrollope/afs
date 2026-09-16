package main

import (
	"context"
	"os"
	"testing"
)

func TestSyncDelayedDirectoryChmodReceiptPreservesNewerBaseline(t *testing.T) {
	env, d, abs := directoryChmodDiagnostic(t)
	ctx := context.Background()
	if err := os.Chmod(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
	d.uploader.process(ctx, nextDiagnosticUpload(t, d))
	oldReceipt := <-d.reconciler.uploadResCh
	if oldReceipt.Err != nil || oldReceipt.Skipped || oldReceipt.Conflict {
		t.Fatalf("upload: %+v", oldReceipt)
	}
	if err := env.fsClient.Chmod(ctx, "/directory", 0o750); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/directory"})
	d.downloader.process(ctx, <-d.reconciler.downloadCh)
	newReceipt := <-d.reconciler.downloadResCh
	if newReceipt.Err != nil || newReceipt.Skipped {
		t.Fatalf("download: %+v", newReceipt)
	}
	d.reconciler.handleDownloadResult(ctx, newReceipt)
	if mode := d.Snapshot().Entries["directory"].Mode; mode != 0o750 {
		t.Fatalf("download baseline %o", mode)
	}
	d.reconciler.handleUploadResult(ctx, oldReceipt)
	if mode := d.Snapshot().Entries["directory"].Mode; mode != 0o750 {
		t.Fatalf("delayed receipt replaced inbound baseline: %o", mode)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
	if len(d.reconciler.uploadCh) != 0 {
		d.uploader.process(ctx, nextDiagnosticUpload(t, d))
		d.reconciler.handleUploadResult(ctx, <-d.reconciler.uploadResCh)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	remote, err := env.fsClient.Stat(ctx, "/directory")
	if err != nil {
		t.Fatal(err)
	}
	if remote.Mode != 0o700 {
		t.Fatalf("later explicit local chmod was lost: remote=%o, want 700", remote.Mode)
	}
}

func TestSyncDuplicateDirectoryChmodReceiptKeepsBaselineVersion(t *testing.T) {
	_, d, abs := directoryChmodDiagnostic(t)
	ctx := context.Background()
	if err := os.Chmod(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
	d.uploader.process(ctx, nextDiagnosticUpload(t, d))
	receipt := <-d.reconciler.uploadResCh
	if receipt.Err != nil || receipt.Skipped || receipt.Conflict {
		t.Fatalf("chmod upload: %+v", receipt)
	}
	d.reconciler.handleUploadResult(ctx, receipt)
	before := d.Snapshot().Entries["directory"]
	d.reconciler.handleUploadResult(ctx, receipt)
	after := d.Snapshot().Entries["directory"]
	if after.Mode != 0o700 || after.Version != before.Version {
		t.Fatalf("duplicate receipt changed baseline: before=%+v after=%+v", before, after)
	}
}
