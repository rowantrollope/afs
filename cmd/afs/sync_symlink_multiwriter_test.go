package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/mount/client"
)

func TestSyncConcurrentSymlinkTargetsPreserved(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "retarget"}[baseline], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			d.reconciler.store = nil // The conflict-copy assertion does not need an asynchronous checkpoint.
			ctx := context.Background()
			const rel = "current"
			abs := filepath.Join(env.localRoot, rel)
			if baseline {
				if err := env.fsClient.Ln(ctx, "initial", "/"+rel); err != nil {
					t.Fatal(err)
				}
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(abs); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("local-target", abs); err != nil {
				t.Fatal(err)
			}
			if err := env.fsClient.Ln(ctx, "remote-target", "/"+rel); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleLocalSymlink(rel, abs)
			d.uploader.processSymlink(ctx, nextDiagnosticUpload(t, d))
			result := <-d.reconciler.uploadResCh
			if result.Err != nil || !result.Conflict {
				t.Fatalf("concurrent symlink upload = %+v", result)
			}
			d.reconciler.handleUploadResult(ctx, result)
			select {
			case op := <-d.reconciler.downloadCh:
				d.downloader.process(ctx, op)
				result := <-d.reconciler.downloadResCh
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				d.reconciler.handleDownloadResult(ctx, result)
			default:
				t.Fatal("no conflict resolution queued")
			}
			assertSymlinkCandidates(t, env, abs)
		})
	}
}

func TestSyncWarmRecoveryPreservesSymlinkConflict(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	if err := env.fsClient.Ln(ctx, "initial", "/current"); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(env.localRoot, "current")
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("local-target", abs); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/current"); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Ln(ctx, "remote-target", "/current"); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertSymlinkCandidates(t, env, abs)
}

type symlinkDeleteRaceClient struct {
	client.Client
	beforeDelete func()
}

func (c *symlinkDeleteRaceClient) Rm(ctx context.Context, path string) error {
	if c.beforeDelete != nil {
		fn := c.beforeDelete
		c.beforeDelete = nil
		fn()
	}
	return c.Client.Rm(ctx, path)
}

func TestSyncSymlinkReplacementRejectsInterveningRemoteTarget(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "event", true: "recovery"}[recovery], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			if err := env.fsClient.Ln(ctx, "initial", "/current"); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(env.localRoot, "current")
			if err := os.Symlink("local-target", abs); err != nil {
				t.Fatal(err)
			}
			racingClient := &symlinkDeleteRaceClient{Client: env.fsClient, beforeDelete: func() {
				if err := env.fsClient.Rm(ctx, "/current"); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Ln(ctx, "remote-target", "/current"); err != nil {
					t.Fatal(err)
				}
			}}
			if recovery {
				d.reconciler.fs = racingClient
				err := d.full.execSymlinkUpload(ctx, syncAction{path: "current", absPath: abs, target: "local-target",
					hasStored: true, storedEntry: SyncEntry{Type: "symlink", Target: "initial"}})
				if !errors.Is(err, client.ErrWriteConflict) {
					t.Fatalf("racing recovery symlink replacement = %v", err)
				}
			} else {
				d.uploader.fs = racingClient
				d.uploader.processSymlink(ctx, uploadOp{Kind: opUploadSymlink, Path: "current", AbsPath: abs,
					Symlink: "local-target", HasStored: true, StoredEntry: SyncEntry{Type: "symlink", Target: "initial"}})
				result := <-d.reconciler.uploadResCh
				if result.Err != nil || !result.Conflict {
					t.Fatalf("racing symlink replacement = %+v", result)
				}
			}
			if target, err := env.fsClient.Readlink(ctx, "/current"); err != nil || target != "remote-target" {
				t.Fatalf("remote candidate = %q, error %v", target, err)
			}
		})
	}
}

func assertSymlinkCandidates(t *testing.T, env *syncTestEnv, abs string) {
	t.Helper()
	if target, err := env.fsClient.Readlink(context.Background(), "/current"); err != nil || target != "remote-target" {
		t.Fatalf("remote candidate = %q, error %v", target, err)
	}
	if target, err := os.Readlink(abs); err != nil || target != "remote-target" {
		t.Fatalf("local published candidate = %q, error %v", target, err)
	}
	copies, err := filepath.Glob(abs + ".conflict-*")
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range copies {
		if target, err := os.Readlink(copy); err == nil && target == "local-target" {
			return
		}
	}
	t.Fatalf("local target was lost; conflict symlinks = %v", copies)
}
