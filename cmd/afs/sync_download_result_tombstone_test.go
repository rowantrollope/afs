package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncLateDownloadTombstoneKeepsNewLocalCandidate(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		for _, change := range []string{"unchanged_worker", "recreated_same", "recreated_different", "edited"} {
			if kind == "symlink" && change == "edited" {
				continue
			}
			t.Run(kind+"/"+change, func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				const rel = "entry"
				const baseline = "published baseline"
				abs := filepath.Join(env.localRoot, rel)
				if kind == "file" {
					env.writeRemoteFile(t, rel, baseline)
				} else if err := env.fsClient.Ln(ctx, baseline, "/"+rel); err != nil {
					t.Fatal(err)
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				stored := d.Snapshot().Entries[rel]
				op := downloadOp{Kind: opDownloadFile, Path: rel, AbsPath: abs, StoredEntry: stored, HasStored: true}
				if kind == "symlink" {
					op.Kind, op.Symlink = opDownloadSymlink, baseline
				}
				d.downloader.process(ctx, op)
				result := <-d.reconciler.downloadResCh
				if result.Err != nil || result.Skipped {
					t.Fatalf("download = %+v", result)
				}
				// Hold the successful result while a local removal is recorded.
				// A later application recreation has not reached the watcher yet.
				d.reconciler.handleLocalDelete(ctx, rel, "remove")
				want := baseline
				if change == "recreated_same" || change == "recreated_different" {
					// Keep the old inode alive outside the mount so this test does
					// not depend on a filesystem's inode-reuse timing.
					if err := os.Rename(abs, filepath.Join(t.TempDir(), "retired")); err != nil {
						t.Fatal(err)
					}
				}
				if change == "recreated_different" || change == "edited" {
					want = "new application candidate after deletion"
				}
				if change != "unchanged_worker" {
					if kind == "file" {
						if err := os.WriteFile(abs, []byte(want), 0o644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Symlink(want, abs); err != nil {
						t.Fatal(err)
					}
				}
				d.reconciler.handleDownloadResult(ctx, result)
				if change == "unchanged_worker" {
					if _, err := os.Lstat(abs); !os.IsNotExist(err) {
						t.Fatalf("matching worker candidate survived deletion: %v", err)
					}
					return
				}
				var got string
				var err error
				if kind == "file" {
					var data []byte
					data, err = os.ReadFile(abs)
					got = string(data)
				} else {
					got, err = os.Readlink(abs)
				}
				if err != nil || got != want {
					t.Fatalf("late result deleted current local candidate: got %q, error %v, want %q", got, err, want)
				}
				select {
				case <-d.reconciler.fullSweepRequest:
				default:
					t.Fatal("preserved local candidate was not scheduled for recovery")
				}
			})
		}
	}
}
