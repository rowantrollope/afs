package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRenameIdentityOnlyAppliesToUntrackedDestination(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			r := &reconciler{renameCandidates: make(map[string]renameCandidate)}
			entry := SyncEntry{Type: kind, LocalIdentity: "device:recycled-inode", LocalHash: "old", Target: "old"}
			r.rememberRenameCandidate("old", entry)
			choose := func(hasStored bool) (renameCandidate, bool) {
				if kind == "symlink" {
					return r.takeRenameCandidateForLocalSymlink("target", entry.LocalIdentity, "new", hasStored)
				}
				return r.takeRenameCandidateForLocalFile("target", entry.LocalIdentity, "new", 256, hasStored)
			}
			if candidate, ok := choose(true); ok {
				t.Fatalf("tracked destination consumed candidate: %+v", candidate)
			}
			if candidate, ok := choose(false); !ok || candidate.path != "old" {
				t.Fatalf("new destination lost its rename candidate: %+v,%t", candidate, ok)
			}
		})
	}
}

// A deleted file's inode can be recycled for an editor's temporary file.
// Its identity is not proof that replacing an already tracked destination
// renamed that other file, and must not replace the destination's baseline.
func TestSyncAtomicEditRejectsRecycledRenameIdentity(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const rel = "agent-1/file-0-2.bin"
	old := strings.Repeat("b", 32768)
	want := strings.Repeat("e", 256)
	abs := env.writeLocalFile(t, rel, old)
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	baseline := d.Snapshot().Entries[rel]
	tmp := filepath.Join(filepath.Dir(abs), ".afs-sync.tmp.editor")
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		t.Fatal(err)
	}
	// Model the filesystem reusing the earlier deleted file's inode for tmp.
	// Keeping this identity explicit makes the race deterministic on APFS
	// and Linux filesystems with different allocation policies.
	deleted := SyncEntry{Type: "file", Mode: 0o644, Size: 12,
		LocalIdentity: localFileIdentity(info),
		LocalHash:     sha256Hex([]byte("deleted file")), RemoteHash: sha256Hex([]byte("deleted file"))}
	d.reconciler.rememberRenameCandidate("agent-1/file-0-0.bin", deleted)
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: rel, KindHint: "create"})
	op := nextDiagnosticUpload(t, d)
	if op.Kind != opUploadFile || !op.HasStored || op.StoredEntry.RemoteHash != baseline.RemoteHash {
		t.Errorf("atomic edit took another file's rename baseline: kind=%v previous=%q baseline=%q want=%q",
			op.Kind, op.PrevPath, op.StoredEntry.RemoteHash, baseline.RemoteHash)
	}
	d.uploader.process(ctx, op)
	res := <-d.reconciler.uploadResCh
	if res.Err != nil || res.Conflict || res.Skipped {
		t.Errorf("single-writer edit did not upload cleanly: error=%v conflict=%t skipped=%t", res.Err, res.Conflict, res.Skipped)
	}
	d.reconciler.handleUploadResult(ctx, res)
	for len(d.reconciler.uploadCh) > 0 {
		d.uploader.process(ctx, nextDiagnosticUpload(t, d))
		d.reconciler.handleUploadResult(ctx, <-d.reconciler.uploadResCh)
	}
	// Let recovery run before the queued download, as it can in the live
	// daemon. A wrong rename baseline otherwise creates the conflict here
	// without an event-downloader conflict log, matching the CI artifacts.
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for len(d.reconciler.downloadCh) > 0 {
		d.downloader.process(ctx, <-d.reconciler.downloadCh)
		d.reconciler.handleDownloadResult(ctx, <-d.reconciler.downloadResCh)
	}
	if got := env.readRemoteFile(t, rel); got != want {
		t.Errorf("canonical edit was not published: got %d bytes", len(got))
	}
	if got, err := os.ReadFile(abs); err != nil || string(got) != want {
		t.Errorf("canonical local edit changed: %d bytes, %v", len(got), err)
	}
	if copies, err := filepath.Glob(abs + ".conflict-*"); err != nil || len(copies) != 0 {
		t.Fatalf("single-writer edit created conflict copies: %v, %v", copies, err)
	}
}
