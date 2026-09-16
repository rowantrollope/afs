package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/mount/client"
)

// A conflict copy is new local work even when the canonical download's result
// is obsolete. The watcher may observe only the source rename, or its event may
// already have been consumed while reconciliation was handling the old path.
// Drive the real downloader and its result in a fixed order without a watcher:
// publication must follow the preserved copy, not another filesystem event.
func TestSyncDownloadConflictSchedulesCopyPublication(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		for _, deleted := range []bool{false, true} {
			name := map[bool]string{false: "file", true: "symlink"}[symlink]
			if deleted {
				name += "_canonical_deleted_before_result"
			}
			t.Run(name, func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				const rel, local, remote = "shared", "local candidate", "remote candidate"
				abs := filepath.Join(env.localRoot, rel)
				if symlink {
					if err := env.fsClient.Ln(ctx, "initial", "/"+rel); err != nil {
						t.Fatal(err)
					}
				} else {
					env.writeRemoteFile(t, rel, "initial")
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				stored := d.Snapshot().Entries[rel]
				op := downloadOp{Kind: opDownloadFile, Path: rel, AbsPath: abs,
					StoredEntry: stored, HasStored: true, Conflict: true}
				if symlink {
					if err := os.Remove(abs); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(local, abs); err != nil {
						t.Fatal(err)
					}
					if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
						t.Fatal(err)
					}
					if err := env.fsClient.Ln(ctx, remote, "/"+rel); err != nil {
						t.Fatal(err)
					}
					op.Kind = opDownloadSymlink
				} else {
					env.writeLocalFile(t, rel, local)
					env.writeRemoteFile(t, rel, remote)
				}
				d.downloader.process(ctx, op)
				res := <-d.reconciler.downloadResCh
				if res.Err != nil || res.Skipped || res.ConflictPath == "" {
					t.Fatalf("expected a preserved local candidate: %+v", res)
				}
				copyRel := filepath.Base(res.ConflictPath)
				if _, known := d.Snapshot().Entries[copyRel]; known {
					t.Fatal("copy already has a publication baseline")
				}
				if deleted {
					// Model the independent local-delete event winning the race
					// before this completed inbound result reaches the reconciler.
					if err := os.Remove(abs); err != nil {
						t.Fatal(err)
					}
					stored.Deleted = true
					d.reconciler.stageSyncEntry(rel, stored)
				}
				d.reconciler.store = nil // Auto-checkpointing is not the publication path.
				d.reconciler.handleDownloadResult(ctx, res)
				select {
				case <-d.reconciler.fullSweepRequests():
				default:
					t.Fatalf("preserved %s has no queued publication or recovery", copyRel)
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if target, err := env.fsClient.Readlink(ctx, "/"+copyRel); err != nil || target != local {
						t.Fatalf("conflict target not published: %q %v", target, err)
					}
				} else if got := env.readRemoteFile(t, copyRel); got != local {
					t.Fatalf("conflict bytes not published: %q", got)
				}
				if _, known := d.Snapshot().Entries[copyRel]; !known {
					t.Fatal("published conflict copy has no synchronized baseline")
				}
			})
		}
	}
}

// Another inbound operation or local delete can remove the canonical file
// before its post-write stat. That error must retain the independently created
// conflict path so result handling can still publish it.
func TestSyncDownloadMetadataFailureRetainsConflictPublication(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const local, remote = "local candidate", "remote candidate"
	abs := env.writeLocalFile(t, "shared", local)
	env.writeRemoteFile(t, "shared", remote)
	op := downloadOp{Kind: opDownloadFile, Path: "shared", AbsPath: abs, Conflict: true}
	copyPath, err := d.downloader.writeLocalFile(ctx, op, 0o644, func(file *os.File) error {
		_, err := io.WriteString(file, remote)
		return err
	})
	if err != nil || copyPath == "" {
		t.Fatalf("preserve candidate: %q %v", copyPath, err)
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	d.downloader.sendFileResult(op, &client.StatResult{}, sha256Hex([]byte(remote)), copyPath, 0o644, int64(len(remote)))
	res := <-d.reconciler.downloadResCh
	if res.Err == nil || res.ConflictPath != copyPath {
		t.Fatalf("post-write metadata error lost conflict publication: %+v; preserved path=%s", res, copyPath)
	}
	d.reconciler.handleDownloadResult(ctx, res)
	select {
	case <-d.reconciler.fullSweepRequests():
	default:
		t.Fatal("failed canonical result abandoned the preserved conflict copy")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readRemoteFile(t, filepath.Base(copyPath)); got != local {
		t.Fatalf("conflict bytes not published after metadata failure: %q", got)
	}
}
