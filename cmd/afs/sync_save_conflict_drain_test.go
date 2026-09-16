package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSyncSaveDrainsProvenConflictMove(t *testing.T) {
	for _, change := range []string{"sync_move", "application_rename", "edited_copy", "replaced_copy", "chmod_copy"} {
		t.Run(change, func(t *testing.T) {
			env := newSyncTestEnv(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var enteredOnce, releaseOnce, moveOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			var d *syncDaemon
			var copyPath string
			gate := &syncSaveDrainClient{Client: env.fsClient}
			const candidate = "local candidate preserved by conflict rename"
			gate.echo = func(ctx context.Context, path string, data []byte) error {
				if path == "/hold" {
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					var moveErr error
					moveOnce.Do(func() {
						source := filepath.Join(env.localRoot, "rename")
						if change == "application_rename" {
							copyPath = d.reconciler.conflict.conflictPath(source)
							moveErr = os.Rename(source, copyPath)
						} else {
							copyPath, moveErr = moveLocalToConflict(d.reconciler.conflict, source)
						}
						if moveErr != nil {
							return
						}
						switch change {
						case "edited_copy":
							moveErr = os.WriteFile(copyPath, []byte("application edit during drain"), 0o644)
						case "replaced_copy":
							moveErr = os.Rename(copyPath, filepath.Join(t.TempDir(), "retired"))
							if moveErr == nil {
								moveErr = os.WriteFile(copyPath, []byte(candidate), 0o644)
							}
						case "chmod_copy":
							moveErr = os.Chmod(copyPath, 0o600)
						}
					})
					if moveErr != nil {
						return moveErr
					}
				}
				return gate.Client.Echo(ctx, path, data)
			}
			d = env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.FS = gate })
			if err := d.watcher.Close(); err != nil {
				t.Fatal(err)
			}
			service := &syncSaveService{ctx: context.Background(), active: d}
			t.Cleanup(func() {
				if service.active != nil {
					service.active.Stop()
				}
			})
			env.writeLocalFile(t, "rename", candidate)
			abs := env.writeLocalFile(t, "hold", "active publication")
			d.reconciler.queueUpload(uploadOp{Kind: opUploadFile, Path: "hold", AbsPath: abs,
				Content: []byte("active publication"), LocalHash: sha256Hex([]byte("active publication")), Mode: 0o644})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("upload never entered drain gate")
			}
			done := make(chan syncControlResult, 1)
			go func() { done <- service.saveWithResume(saveRequestForDaemon(d, 5*time.Second), false) }()
			select {
			case <-d.uploader.stopCh:
			case <-time.After(5 * time.Second):
				t.Fatal("save did not finish its initial tree scan")
			}
			// Complete a real sync-owned conflict move while StopForSave joins
			// an already active mutation. Application controls use the same
			// filename shape and preserve bytes, but have no sync provenance.
			releaseOnce.Do(func() { close(release) })
			var result syncControlResult
			select {
			case result = <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("save did not join the conflict move")
			}
			if change != "sync_move" {
				if result.Success || result.Save != nil || !strings.Contains(result.Error, "local tree changed while pausing") {
					t.Fatalf("application change acknowledged: %+v", result)
				}
				return
			}
			if !result.Success || result.Save == nil {
				t.Fatalf("sync-owned preserved move prevented save: %+v", result)
			}
			rel, err := filepath.Rel(env.localRoot, copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := env.readRemoteFile(t, filepath.ToSlash(rel)); got != candidate {
				t.Fatalf("saved conflict copy = %q", got)
			}
			if env.remoteExists(t, "rename") {
				t.Fatal("save restored the old conflict source")
			}
		})
	}
}

func TestSyncSaveConflictCaptureEndsAfterScanFailure(t *testing.T) {
	env := newSyncTestEnv(t)
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.MaxFileBytes = 1 })
	if err := d.watcher.Close(); err != nil {
		t.Fatal(err)
	}
	service := &syncSaveService{ctx: context.Background(), active: d}
	t.Cleanup(func() {
		if service.active != nil {
			service.active.Stop()
		}
	})
	env.writeLocalFile(t, "file", "too large")
	result := service.save(saveRequestForDaemon(d, 5*time.Second))
	if result.Success || !strings.Contains(result.Error, "scan local tree before save") {
		t.Fatalf("expected initial scan failure: %+v", result)
	}
	if service.active != d {
		t.Fatal("initial scan failure stopped the generation")
	}
	env.writeLocalFile(t, "file", "x")
	result = service.saveWithResume(saveRequestForDaemon(d, 5*time.Second), false)
	if !result.Success || result.Save == nil {
		t.Fatalf("capture remained active after failed initial scan: %+v", result)
	}
}

func TestSyncConflictCaptureDeadlineCleanup(t *testing.T) {
	namer := newConflictNamer()
	finishOld, err := namer.captureMoves(func() error { return context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) || finishOld == nil {
		t.Fatalf("failed capture = %v", err)
	}
	finishOld()
	finishNew, err := namer.captureMoves(func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer finishNew()
	// Repeated cleanup of the old save cannot detach the current observer.
	finishOld()
	source := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(source, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := moveLocalToConflict(namer, source); err != nil {
		t.Fatal(err)
	}
	if len(finishNew()) != 1 || len(finishOld()) != 0 {
		t.Fatal("capture leaked across saves or lost a completed move")
	}
}

func TestSyncSaveDrainsChainedDirectoryConflictMoves(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeLocalFile(t, "directory/child", "nested candidate")
	var before syncSaveTree
	finish, err := d.reconciler.conflict.captureMoves(func() error {
		var err error
		before, err = scanSyncSaveLocal(context.Background(), d.reconciler)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if _, err := moveLocalToConflict(d.reconciler.conflict, filepath.Join(env.localRoot, "missing")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(env.localRoot, "directory")
	for range 2 {
		target, err = moveLocalToConflict(d.reconciler.conflict, target)
		if err != nil {
			t.Fatal(err)
		}
	}
	moves := finish()
	if len(moves) != 2 {
		t.Fatalf("recorded moves = %d, want only two completed renames", len(moves))
	}
	after, err := scanSyncSaveLocal(context.Background(), d.reconciler)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareSyncSaveDrainTrees(env.localRoot, before, after, moves); err != nil {
		t.Fatalf("unchanged nested candidates rejected: %v", err)
	}
}
