package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncReadonlyPermissionFailureRequestsRecovery(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(fmt.Sprint(readonly), func(t *testing.T) {
			_, d := recoveryBaselineDiagnostic(t)
			d.reconciler.readonly = readonly
			d.reconciler.handleDownloadResult(context.Background(), downloadResult{
				Op: downloadOp{Kind: opDownloadFile, Path: "file"}, Err: &os.PathError{Op: "open", Path: "file", Err: fs.ErrPermission},
			})
			select {
			case <-d.reconciler.fullSweepRequests():
				if !readonly {
					t.Fatal("writable permissions unexpectedly triggered reader recovery")
				}
			default:
				if readonly {
					t.Fatal("unreadable reader file did not schedule recovery")
				}
			}
		})
	}
}

func TestSyncReadonlyUnreadableFileReceivesRemoteUpdate(t *testing.T) {
	for _, mode := range []uint32{0, 0o200, 0o100} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			env.writeRemoteFile(t, "file", "old")
			if err := env.fsClient.Chmod(ctx, "/file", mode); err != nil {
				t.Fatal(err)
			}
			d.reconciler.readonly, d.uploader.readonly, d.downloader.readonly = true, true, true
			if err := d.full.coldStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(env.localRoot, "file")
			if _, err := os.ReadFile(abs); !errors.Is(err, fs.ErrPermission) {
				t.Skip("requires a user subject to local read permissions")
			}
			env.writeRemoteFile(t, "file", "new remote bytes")
			if err := env.fsClient.Chmod(ctx, "/file", 0o600); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/file"})
			d.downloader.process(ctx, <-d.reconciler.downloadCh)
			result := <-d.reconciler.downloadResCh
			if !errors.Is(result.Err, fs.ErrPermission) {
				t.Fatalf("expected unreadable snapshot, got %+v", result)
			}
			d.reconciler.handleDownloadResult(ctx, result)
			select {
			case <-d.reconciler.fullSweepRequests():
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("live unreadable file update did not schedule recovery")
			}
			if got := env.readLocalFile(t, "file"); got != "new remote bytes" {
				t.Fatalf("stale reader contents: %q", got)
			}
			info, err := os.Stat(abs)
			if err != nil || info.Mode().Perm() != 0o400 {
				t.Fatalf("recovered access = %v, %v", info, err)
			}
			if got := env.readRemoteFile(t, "file"); got != "new remote bytes" {
				t.Fatalf("reader changed publication: %q", got)
			}
		})
	}
}
