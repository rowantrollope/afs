package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncRenamePreservesConcurrentSourceEdit(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const original, competing = "original", "competed" // Equal sizes expose a false unchanged baseline.
	oldPath := env.writeLocalFile(t, "old.txt", original)
	stamp := time.Unix(10, 0)
	if err := os.Chtimes(oldPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(env.localRoot, "new.txt")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "old.txt", KindHint: "rename"})
	op := nextDiagnosticUpload(t, d)
	if op.Kind != opUploadRename {
		t.Fatalf("queued kind = %v, want rename", op.Kind)
	}
	// Another client publishes after the local rename was queued, while this
	// client's destination still holds the original bytes and source baseline.
	env.writeRemoteFile(t, "old.txt", competing)
	d.uploader.processRename(ctx, op)
	res := <-d.reconciler.uploadResCh
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	d.reconciler.handleUploadResult(ctx, res)
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// A safe resolution may keep the edit at the old name or carry it along
	// with the rename, but the competing published bytes must remain readable.
	preserved, retainedLocal := false, false
	for _, rel := range []string{"old.txt", "new.txt"} {
		data, err := env.fsClient.Cat(ctx, "/"+rel)
		t.Logf("remote %s = %q, error %v", rel, data, err)
		if err == nil && string(data) == competing {
			preserved = true
		}
		if err == nil && string(data) == original {
			retainedLocal = true
		}
	}
	if !preserved {
		t.Fatal("rename and recovery erased the concurrent remote source edit")
	}
	if !retainedLocal {
		t.Fatal("rename and recovery failed to publish the local destination")
	}
}
