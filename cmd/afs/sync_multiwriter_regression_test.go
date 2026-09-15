package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Upstream only checked divergence when a prior baseline existed. Two new
// same-path files could silently overwrite one another before their first sync.
func TestSyncConcurrentCreatePreservesBothCandidates(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "chunked"}[chunked], func(t *testing.T) {
			env := newSyncTestEnv(t)
			const mine, theirs = "second client content", "first client content"
			abs := env.writeLocalFile(t, "shared.txt", mine)
			env.writeRemoteFile(t, "shared.txt", theirs)
			results := make(chan uploadResult, 1)
			u := newUploader(env.fsClient, results, 0, false, newSyncLogger(false))
			op := uploadOp{Kind: opUploadFile, Path: "shared.txt", AbsPath: abs, Content: []byte(mine), LocalHash: sha256Hex([]byte(mine)), Mode: 0644}
			if chunked {
				op.Chunked, op.ChunkSize, op.FileSize = true, 4, int64(len(mine))
				op.ChunkHashes = uploadChunkHashes([]byte(mine), 4)
				op.LocalHash = compositeHash(op.ChunkHashes)
				for i := range op.ChunkHashes {
					op.DirtyChunks = append(op.DirtyChunks, i)
				}
			}
			u.processFile(context.Background(), op)
			result := <-results
			if !result.Conflict || result.Err != nil {
				t.Fatalf("concurrent create = %+v", result)
			}
			if got := env.readRemoteFile(t, "shared.txt"); got != theirs {
				t.Fatalf("remote competing bytes lost: %q", got)
			}
			if got := env.readLocalFile(t, "shared.txt"); got != mine {
				t.Fatalf("local competing bytes lost: %q", got)
			}
		})
	}
}

func TestSyncRemoteDeletePreservesOfflineEdits(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "subtree"}[nested], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			rel := "shared.txt"
			if nested {
				rel = "dir/shared.txt"
				if err := env.fsClient.Mkdir(context.Background(), "/dir"); err != nil {
					t.Fatal(err)
				}
			}
			env.writeRemoteFile(t, rel, "original")
			if err := d.full.run(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			env.writeLocalFile(t, rel, "offline local edit")
			if err := env.fsClient.Rm(context.Background(), absoluteRemotePath(rel)); err != nil {
				t.Fatal(err)
			}
			if nested {
				if err := env.fsClient.Rm(context.Background(), "/dir"); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.full.warmStart(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			assertPreservedDeleteConflict(t, env.localRoot, "offline local edit")
		})
	}
}

func TestSyncQueuedRemoteDeletePreservesNewLocalEdit(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "shared.txt", "original")
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	stored := d.Snapshot().Entries["shared.txt"]
	if err := env.fsClient.Rm(context.Background(), "/shared.txt"); err != nil {
		t.Fatal(err)
	}
	env.writeLocalFile(t, "shared.txt", "edited after remote deletion queued")
	results := make(chan downloadResult, 1)
	d.downloader.results = results
	d.downloader.processDelete(context.Background(), downloadOp{Kind: opDownloadDelete, Path: "shared.txt", AbsPath: filepath.Join(env.localRoot, "shared.txt"), StoredEntry: stored, HasStored: true})
	result := <-results
	if result.Err != nil || result.ConflictPath == "" {
		t.Fatalf("delete result = %+v", result)
	}
	assertPreservedDeleteConflict(t, env.localRoot, "edited after remote deletion queued")
}

func TestSyncLocalDeletePreservesConcurrentRemoteEdit(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "uploader", true: "warm_recovery"}[warm], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			abs := env.writeLocalFile(t, "shared.txt", "original")
			if err := d.full.warmStart(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			stored := d.Snapshot().Entries["shared.txt"]
			if err := os.Remove(abs); err != nil {
				t.Fatal(err)
			}
			env.writeRemoteFile(t, "shared.txt", "concurrent remote edit")
			if warm {
				if err := d.full.warmStart(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			} else {
				tombstone := stored
				tombstone.Deleted = true
				d.reconciler.stageSyncEntry("shared.txt", tombstone)
				d.uploader.processDelete(context.Background(), uploadOp{Kind: opUploadDelete, Path: "shared.txt", AbsPath: abs, StoredEntry: stored, HasStored: true})
				result := <-d.reconciler.uploadResCh
				if !result.Conflict || result.Err != nil {
					t.Fatalf("delete upload = %+v", result)
				}
				d.reconciler.handleUploadResult(context.Background(), result)
				select {
				case op := <-d.reconciler.downloadCh:
					d.downloader.process(context.Background(), op)
					d.reconciler.handleDownloadResult(context.Background(), <-d.reconciler.downloadResCh)
				default:
					t.Fatal("remote competing edit was not scheduled for download")
				}
			}
			if got := env.readRemoteFile(t, "shared.txt"); got != "concurrent remote edit" {
				t.Fatalf("remote competing edit lost: %q", got)
			}
			if got := env.readLocalFile(t, "shared.txt"); got != "concurrent remote edit" {
				t.Fatalf("remote competing edit did not return locally: %q", got)
			}
		})
	}
}

func TestSyncRenamePreservesCompetingDestination(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	abs := env.writeLocalFile(t, "new.txt", "local renamed content")
	env.writeRemoteFile(t, "old.txt", "local renamed content")
	env.writeRemoteFile(t, "new.txt", "another client's destination")
	d.uploader.processRename(context.Background(), uploadOp{Kind: opUploadRename, Path: "new.txt", PrevPath: "old.txt", AbsPath: abs})
	result := <-d.reconciler.uploadResCh
	if result.Err != nil || !result.Skipped {
		t.Fatalf("rename collision = %+v", result)
	}
	if got := env.readRemoteFile(t, "new.txt"); got != "another client's destination" {
		t.Fatalf("competing destination lost: %q", got)
	}
	if got := env.readRemoteFile(t, "old.txt"); got != "local renamed content" {
		t.Fatalf("source lost: %q", got)
	}
	if got := env.readLocalFile(t, "new.txt"); got != "local renamed content" {
		t.Fatalf("local rename lost: %q", got)
	}
}

func assertPreservedDeleteConflict(t *testing.T, root, content string) {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() || !strings.Contains(path, ".conflict-") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) == content {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("modified local data was not preserved as a conflict copy")
	}
}
