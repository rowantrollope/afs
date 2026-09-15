package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncDirectoryDeleteRetriesAfterChildren(t *testing.T) {
	for _, peerEdit := range []bool{false, true} {
		name := "unchanged children"
		if peerEdit {
			name = "newer remote child preserved"
		}
		t.Run(name, func(t *testing.T) {
			env := newSyncTestEnv(t)
			env.writeLocalFile(t, "parent/child", "baseline bytes")
			d, err := newSyncDaemon(syncDaemonConfig{Workspace: env.workspace, LocalRoot: env.localRoot, FS: env.fsClient, Store: env.store})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := d.full.run(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(env.localRoot, "parent")); err != nil {
				t.Fatal(err)
			}
			// Deliver the directory event before child events, as fsnotify can
			// do when a tree is removed. Redis correctly rejects this first Rm.
			d.reconciler.handleLocalDelete(ctx, "parent", "remove")
			op := <-d.reconciler.uploadCh
			d.uploader.processDelete(ctx, op)
			result := <-d.reconciler.uploadResCh
			if result.Err == nil || !strings.Contains(result.Err.Error(), "directory not empty") {
				t.Fatalf("parent-first deletion did not reach expected dependency: %+v", result)
			}
			d.reconciler.handleUploadResult(ctx, result)
			select {
			case <-d.reconciler.fullSweepRequests():
			default:
				t.Fatal("failed parent deletion was abandoned without scheduling reconciliation")
			}
			if peerEdit {
				env.writeRemoteFile(t, "parent/child", "newer peer bytes")
			}
			// The retained recovery planner detects missing children and applies
			// deletions in dependency order, with its existing conflict checks.
			for pass := 0; pass < 4; pass++ {
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if peerEdit || !env.remoteExists(t, "parent") {
					break
				}
				// Removing a child changes the parent's Redis metadata. The
				// existing stale-observation guard may schedule another pass.
				select {
				case <-d.reconciler.fullSweepRequests():
				default:
					t.Fatal("parent remained and recovery stopped scheduling work")
				}
			}
			if peerEdit {
				if got := env.readRemoteFile(t, "parent/child"); got != "newer peer bytes" {
					t.Fatalf("retry deleted competing remote content: %q", got)
				}
				if got := env.readLocalFile(t, "parent/child"); got != "newer peer bytes" {
					t.Fatalf("retry did not restore competing remote content: %q", got)
				}
			} else if env.remoteExists(t, "parent") {
				t.Fatal("directory remained after dependency-ordered recovery")
			}
		})
	}
}
