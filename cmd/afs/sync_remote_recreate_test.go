package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncInboundDeleteThenIdenticalRecreate(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		for _, delivery := range []string{"event", "recovery"} {
			t.Run(kind+"/"+delivery, func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				const rel = "entry"
				abs := filepath.Join(env.localRoot, rel)
				create := func() {
					if kind == "file" {
						env.writeRemoteFile(t, rel, "identical content")
					} else if err := env.fsClient.Ln(ctx, "identical-target", "/"+rel); err != nil {
						t.Fatal(err)
					}
				}
				create()
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
					t.Fatal(err)
				}
				if delivery == "event" {
					d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
					select {
					case op := <-d.reconciler.downloadCh:
						d.downloader.process(ctx, op)
						result := <-d.reconciler.downloadResCh
						if result.Err != nil || result.Skipped {
							t.Fatalf("delete result: %+v", result)
						}
						d.reconciler.handleDownloadResult(ctx, result)
					default:
						t.Fatal("missing inbound delete operation")
					}
				} else if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(abs); !os.IsNotExist(err) {
					t.Fatalf("inbound deletion not applied: %v", err)
				}
				create()
				if !env.remoteExists(t, rel) {
					t.Fatal("fixture failed to recreate the remote entry")
				}
				d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
				for len(d.reconciler.downloadCh) > 0 {
					op := <-d.reconciler.downloadCh
					d.downloader.process(ctx, op)
					d.reconciler.handleDownloadResult(ctx, <-d.reconciler.downloadResCh)
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if !env.remoteExists(t, rel) {
					t.Error("inbound deletion tombstone removed another writer's identical recreation from Redis")
				}
				if _, err := os.Lstat(abs); err != nil {
					t.Errorf("identical recreated entry was not restored locally: %v", err)
				}
			})
		}
	}
}

func TestSyncOutboundDeleteThenIdenticalRecreate(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		for _, delivery := range []string{"event", "recovery"} {
			t.Run(kind+"/"+delivery, func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				const rel = "entry"
				abs := filepath.Join(env.localRoot, rel)
				create := func() {
					if kind == "file" {
						env.writeRemoteFile(t, rel, "identical content")
					} else if err := env.fsClient.Ln(ctx, "identical-target", "/"+rel); err != nil {
						t.Fatal(err)
					}
				}
				create()
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(abs); err != nil {
					t.Fatal(err)
				}
				if delivery == "event" {
					d.reconciler.handleLocalDelete(ctx, rel, "remove")
					select {
					case op := <-d.reconciler.uploadCh:
						d.uploader.processDelete(ctx, op)
						result := <-d.reconciler.uploadResCh
						if result.Err != nil || result.Skipped || result.Conflict {
							t.Fatalf("delete result: %+v", result)
						}
						d.reconciler.handleUploadResult(ctx, result)
					case <-time.After(5 * time.Second):
						t.Fatal("missing outbound delete operation")
					}
				} else if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if env.remoteExists(t, rel) {
					t.Fatal("outbound deletion did not reach Redis")
				}
				create()
				d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
				for len(d.reconciler.downloadCh) > 0 {
					op := <-d.reconciler.downloadCh
					d.downloader.process(ctx, op)
					d.reconciler.handleDownloadResult(ctx, <-d.reconciler.downloadResCh)
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if !env.remoteExists(t, rel) {
					t.Error("completed outbound tombstone removed another writer's identical recreation from Redis")
				}
				if _, err := os.Lstat(abs); err != nil {
					t.Errorf("identical recreated entry was not restored locally: %v", err)
				}
			})
		}
	}
}

func TestSyncOutboundDeleteAcknowledgmentPreservesNewerState(t *testing.T) {
	for _, next := range []string{"remote_recreation", "local_write", "local_tombstone"} {
		t.Run(next, func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "entry"
			env.writeRemoteFile(t, rel, "original")
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(env.localRoot, rel)); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleLocalDelete(ctx, rel, "remove")
			var op uploadOp
			select {
			case op = <-d.reconciler.uploadCh:
			case <-time.After(5 * time.Second):
				t.Fatal("missing outbound delete operation")
			}
			d.uploader.processDelete(ctx, op)
			result := <-d.reconciler.uploadResCh
			if result.Err != nil || result.Skipped || result.Conflict {
				t.Fatalf("delete result: %+v", result)
			}
			var newer SyncEntry
			if next == "remote_recreation" {
				env.writeRemoteFile(t, rel, "original")
				// The still-outstanding tombstone suppresses this event.
				d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
				if len(d.reconciler.downloadCh) != 0 {
					t.Fatal("fixture expected recreation before acknowledgment")
				}
			} else {
				d.stateWriter.mu.Lock()
				newer = SyncEntry{Type: "file", Mode: 0o644, LocalHash: "new baseline", Deleted: next == "local_tombstone", Version: d.stateWriter.nextVersion()}
				d.stateWriter.state.Entries[rel] = newer
				d.stateWriter.mu.Unlock()
				if next == "local_write" {
					env.writeLocalFile(t, rel, "newer local contents")
				}
			}
			d.reconciler.handleUploadResult(ctx, result)
			if next == "remote_recreation" {
				select {
				case <-d.reconciler.fullSweepRequest:
				default:
					t.Fatal("acknowledgment did not request recovery of suppressed recreation")
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if got := env.readLocalFile(t, rel); got != "original" {
					t.Fatalf("recovered contents = %q", got)
				}
			} else if got := d.Snapshot().Entries[rel]; got.Version != newer.Version || got.LocalHash != newer.LocalHash || got.Deleted != newer.Deleted {
				t.Fatalf("acknowledgment changed newer state: before=%+v, after=%+v", newer, got)
			}
		})
	}
}

func TestSyncDuplicateLocalDeleteDoesNotStrandTombstone(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "entry"
			create := func() {
				if kind == "file" {
					env.writeRemoteFile(t, rel, "identical content")
				} else if err := env.fsClient.Ln(ctx, "identical-target", "/"+rel); err != nil {
					t.Fatal(err)
				}
			}
			create()
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(env.localRoot, rel)); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleLocalDelete(ctx, rel, "remove")
			var op uploadOp
			select {
			case op = <-d.reconciler.uploadCh:
			case <-time.After(5 * time.Second):
				t.Fatal("missing outbound delete")
			}
			d.uploader.processDelete(ctx, op)
			result := <-d.reconciler.uploadResCh
			if result.Err != nil || result.Skipped || result.Conflict {
				t.Fatalf("delete result: %+v", result)
			}
			// A watcher event and the symlink sweep can observe the same
			// removal before its queued acknowledgment reaches the reconciler.
			d.reconciler.handleLocalDelete(ctx, rel, "remove")
			d.reconciler.handleUploadResult(ctx, result)
			if entry, exists := d.Snapshot().Entries[rel]; exists && entry.Deleted {
				t.Fatalf("duplicate notification stranded an acknowledged deletion: %+v", entry)
			}
			create()
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
			for len(d.reconciler.downloadCh) > 0 {
				d.downloader.process(ctx, <-d.reconciler.downloadCh)
				d.reconciler.handleDownloadResult(ctx, <-d.reconciler.downloadResCh)
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if !env.remoteExists(t, rel) {
				t.Fatal("duplicate deletion erased the peer's identical recreation")
			}
			if _, err := os.Lstat(filepath.Join(env.localRoot, rel)); err != nil {
				t.Fatalf("peer recreation did not return locally: %v", err)
			}
			if kind == "file" {
				if got := env.readLocalFile(t, rel); got != "identical content" {
					t.Fatalf("recreated file contents = %q", got)
				}
			} else if target, err := os.Readlink(filepath.Join(env.localRoot, rel)); err != nil || target != "identical-target" {
				t.Fatalf("recreated symlink = %q, %v", target, err)
			}
		})
	}
}

func TestSyncInboundDeleteResultPreservesNewerLocalWrite(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued_delete", true: "delayed_result"}[completed], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "entry"
			env.writeRemoteFile(t, rel, "original")
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
			op := <-d.reconciler.downloadCh
			var result downloadResult
			if completed {
				d.downloader.process(ctx, op)
				result = <-d.reconciler.downloadResCh
				if result.Err != nil || result.Skipped {
					t.Fatalf("delete result = %+v", result)
				}
				// A full scan runs before the event worker accepts the result.
				// It must not infer an outbound deletion from our local removal.
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if entry, exists := d.Snapshot().Entries[rel]; exists {
					t.Fatalf("completed inbound delete retained baseline: %+v", entry)
				}
			}
			env.writeLocalFile(t, rel, "newer local write")
			d.reconciler.stageSyncEntry(rel, SyncEntry{Type: "file", Mode: 0o644, LocalHash: sha256Hex([]byte("newer local write"))})
			newer := d.Snapshot().Entries[rel]
			if !completed {
				d.downloader.process(ctx, op)
				result = <-d.reconciler.downloadResCh
				if result.Err != nil || !result.Skipped {
					t.Fatalf("obsolete delete result = %+v", result)
				}
			}
			d.reconciler.handleDownloadResult(ctx, result)
			if got := env.readLocalFile(t, rel); got != "newer local write" {
				t.Fatalf("new local write = %q", got)
			}
			if entry := d.Snapshot().Entries[rel]; entry.Version != newer.Version || entry.LocalHash != newer.LocalHash || entry.Deleted {
				t.Fatalf("new baseline changed: before %+v, after %+v", newer, entry)
			}
		})
	}
}

func TestSyncInboundDeleteRechecksRootBeforeMutation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "conflict_move"}[conflict], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "entry"
			env.writeRemoteFile(t, rel, "original")
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			stored := d.Snapshot().Entries[rel]
			if conflict {
				env.writeLocalFile(t, rel, "local conflict")
			}
			checks := 0
			check := func() error {
				checks++
				if checks == 2 {
					replaceTestSyncRoot(t, env.localRoot)
					env.writeLocalFile(t, rel, "replacement-root contents")
				}
				return d.downloader.checkLocalRoot()
			}
			copy, skipped, err := applySyncRemoteDelete(ctx, env.localRoot, filepath.Join(env.localRoot, rel), stored, true, d.stateWriter, d.reconciler.conflict, d.reconciler.echo, check)
			if err == nil || skipped || copy != "" {
				t.Fatalf("replacement-root deletion result: copy=%q, skipped=%v, error=%v", copy, skipped, err)
			}
			if got := env.readLocalFile(t, rel); got != "replacement-root contents" {
				t.Fatalf("replacement-root contents = %q", got)
			}
			if entry := d.Snapshot().Entries[rel]; entry.Version != stored.Version || entry.Deleted {
				t.Fatalf("rejected deletion changed baseline: %+v", entry)
			}
		})
	}
}
