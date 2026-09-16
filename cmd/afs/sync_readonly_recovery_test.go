package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestSyncReadonlyRecoveryNeverPublishesLocalMutations(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	env.writeRemoteFile(t, "canonical", "published bytes")
	env.writeRemoteFile(t, "removed", "remote retained")
	if err := env.fsClient.Mkdir(ctx, "/directory"); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Ln(ctx, "published-target", "/link"); err != nil {
		t.Fatal(err)
	}
	d.reconciler.readonly, d.uploader.readonly, d.downloader.readonly = true, true, true
	if err := d.full.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(env.localRoot, "canonical")
	if err := os.Chmod(canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("local edit preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(env.localRoot, "removed")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(env.localRoot, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(env.localRoot, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("local-target", filepath.Join(env.localRoot, "link")); err != nil {
		t.Fatal(err)
	}
	env.writeLocalFile(t, "new-directory/new-file", "local only")
	if err := os.Symlink("local-only-target", filepath.Join(env.localRoot, "new-link")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"canonical", "removed", "directory", "link", "new-directory", "new-directory/new-file", "new-link"} {
		d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: rel, KindHint: "write"})
	}
	if len(d.reconciler.uploadCh) != 0 {
		t.Fatal("read-only watcher queued outbound mutations")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if env.readRemoteFile(t, "canonical") != "published bytes" || env.readRemoteFile(t, "removed") != "remote retained" {
		t.Fatal("read-only recovery changed remote bytes or deletion")
	}
	stat, err := env.fsClient.Stat(ctx, "/directory")
	if err != nil || stat.Mode != 0o755 {
		t.Fatalf("read-only recovery changed directory mode: %+v, %v", stat, err)
	}
	target, err := env.fsClient.Readlink(ctx, "/link")
	if err != nil || target != "published-target" {
		t.Fatalf("read-only recovery changed symlink: %q, %v", target, err)
	}
	for _, rel := range []string{"new-directory", "new-link"} {
		stat, err := env.fsClient.Stat(ctx, "/"+rel)
		if err != nil || stat != nil {
			t.Fatalf("read-only recovery published %s: %+v, %v", rel, stat, err)
		}
	}
	if env.readLocalFile(t, "new-directory/new-file") != "local only" {
		t.Fatal("local-only content removed")
	}
	copies, err := filepath.Glob(canonical + ".conflict-*")
	if err != nil || len(copies) != 1 {
		t.Fatalf("local edit conflict candidates = %v, %v", copies, err)
	}
	data, err := os.ReadFile(copies[0])
	if err != nil || string(data) != "local edit preserved" {
		t.Fatalf("read-only local edit lost: %q, %v", data, err)
	}
	if env.readLocalFile(t, "removed") != "remote retained" {
		t.Fatal("read-only local deletion was not repaired")
	}
}

func TestSyncReadonlyFileModesPreserveExistingAccess(t *testing.T) {
	for _, sourceMode := range []os.FileMode{0o755, 0o600, 0o000} {
		for _, route := range []string{"cold", "recovery", "event", "chunked"} {
			if sourceMode == 0 && route == "chunked" {
				continue // An unreadable local base cannot supply unchanged delta chunks.
			}
			t.Run(fmt.Sprintf("%o/%s", sourceMode, route), func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				const rel, content = "file", "new!"
				want := sourceMode &^ 0o222
				env.writeRemoteFile(t, rel, content)
				if err := env.fsClient.Chmod(ctx, "/file", uint32(sourceMode)); err != nil {
					t.Fatal(err)
				}
				d.reconciler.readonly, d.downloader.readonly = true, true
				abs := filepath.Join(env.localRoot, rel)
				switch route {
				case "cold":
					if err := d.full.coldStart(ctx, nil); err != nil {
						t.Fatal(err)
					}
					before, err := os.Stat(abs)
					if err != nil {
						t.Fatal(err)
					}
					if err := d.full.warmStart(ctx, nil); err != nil {
						t.Fatal(err)
					}
					after, err := os.Stat(abs)
					if err != nil || !os.SameFile(before, after) {
						t.Fatalf("unchanged reader file was rewritten on recovery: %v", err)
					}
				case "recovery":
					if err := d.full.execDownload(ctx, syncAction{kind: "download", path: rel, absPath: abs, mode: uint32(sourceMode)}); err != nil {
						t.Fatal(err)
					}
				default:
					op := downloadOp{Kind: opDownloadFile, Path: rel, AbsPath: abs}
					if route == "chunked" {
						env.writeLocalFile(t, rel, "old!")
						if err := os.Chmod(abs, want); err != nil {
							t.Fatal(err)
						}
						oldHashes := uploadChunkHashes([]byte("old!"), 4)
						op.HasStored, op.Chunked, op.ChunkSize, op.FileSize = true, true, 4, 4
						op.StoredEntry = SyncEntry{Type: "file", Mode: uint32(want), Size: 4,
							LocalHash: compositeHash(oldHashes), RemoteHash: compositeHash(oldHashes), ChunkSize: 4, ChunkHashes: oldHashes}
						op.ChunkHashes, op.DirtyChunks = uploadChunkHashes([]byte(content), 4), []int{0}
					}
					d.downloader.process(ctx, op)
					res := <-d.reconciler.downloadResCh
					if res.Err != nil || res.Skipped {
						t.Fatalf("readonly %s download = %+v", route, res)
					}
					d.reconciler.handleDownloadResult(ctx, res)
				}
				info, err := os.Stat(abs)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("readonly mode = %v, %v; want %o", info, err, want)
				}
				if mode := d.Snapshot().Entries[rel].Mode; mode != uint32(want) {
					t.Fatalf("readonly baseline mode = %o, want %o", mode, want)
				}
				if want == 0 {
					// Inspection is test-owned; retain the mode assertion above.
					if err := os.Chmod(abs, 0o400); err != nil {
						t.Fatal(err)
					}
				}
				if env.readLocalFile(t, rel) != content {
					t.Fatal("readonly download contents changed")
				}
				remote, err := env.fsClient.Stat(ctx, "/file")
				if err != nil || remote.Mode != uint32(sourceMode) {
					t.Fatalf("readonly mode mask was published: %+v, %v", remote, err)
				}
			})
		}
	}
}

func TestSyncReadonlyConflictsDoNotCreateCheckpoints(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		for _, operation := range []string{"update", "delete"} {
			t.Run(fmt.Sprintf("readonly=%t/%s", readonly, operation), func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				manifest := controlplane.Manifest{Workspace: env.workspace, Savepoint: "initial", Entries: map[string]controlplane.ManifestEntry{"/": {Type: "dir", Mode: 0o755}}}
				if err := env.cp.PutSavepoint(ctx, controlplane.SavepointMeta{Workspace: env.workspace, ID: "initial", CreatedAt: time.Now()}, manifest); err != nil {
					t.Fatal(err)
				}
				if err := env.rdb.Set(ctx, controlplane.WorkspaceGenerationKey(env.workspace), "g_readonly-conflict-test", 0).Err(); err != nil {
					t.Fatal(err)
				}
				d.reconciler.readonly, d.downloader.readonly = readonly, readonly
				env.writeRemoteFile(t, "file", "baseline")
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				abs := filepath.Join(env.localRoot, "file")
				if err := os.Chmod(abs, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs, []byte("local conflict candidate"), 0o600); err != nil {
					t.Fatal(err)
				}
				if operation == "update" {
					env.writeRemoteFile(t, "file", "peer update")
				} else if err := env.fsClient.Rm(ctx, "/file"); err != nil {
					t.Fatal(err)
				}
				d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/file"})
				d.downloader.process(ctx, <-d.reconciler.downloadCh)
				result := <-d.reconciler.downloadResCh
				if result.Err != nil || result.Skipped || result.ConflictPath == "" {
					t.Fatalf("conflict result = %+v", result)
				}
				d.reconciler.handleDownloadResult(ctx, result)
				// The writable controls prove this real fixture reaches automatic
				// checkpoint publication. Readers must remain unchanged throughout
				// the same asynchronous observation window.
				deadline := time.Now().Add(250 * time.Millisecond)
				if !readonly {
					deadline = time.Now().Add(2 * time.Second)
				}
				changed := false
				for {
					meta, err := env.cp.GetWorkspaceMeta(ctx, env.workspace)
					if err != nil {
						t.Fatal(err)
					}
					changed = meta.HeadSavepoint != "initial"
					if changed || time.Now().After(deadline) {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if changed == readonly {
					t.Fatalf("checkpoint changed=%t for readonly=%t", changed, readonly)
				}
				data, err := os.ReadFile(result.ConflictPath)
				if err != nil || string(data) != "local conflict candidate" {
					t.Fatalf("local conflict was not preserved: %q, %v", data, err)
				}
			})
		}
	}
}
