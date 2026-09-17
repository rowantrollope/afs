package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

type mtimeCollisionReadCounter struct {
	client.Client
	reads int
}

func (c *mtimeCollisionReadCounter) Cat(ctx context.Context, path string) ([]byte, error) {
	c.reads++
	return c.Client.Cat(ctx, path)
}

// A rename download can create the old local bytes in the same millisecond as
// a later same-size remote write. Equal clocks are not a shared content version.
func TestSyncRecoveryDoesNotAdoptRemoteMtimeCollision(t *testing.T) {
	for _, hasStored := range []bool{true, false} {
		name := "acknowledged_baseline"
		if !hasStored {
			name = "no_baseline"
		}
		t.Run(name, func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "renamed"
			env.writeRemoteFile(t, rel, "before")
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			baseline := d.Snapshot().Entries[rel]
			env.writeRemoteFile(t, rel, "after!")
			stamp := baseline.RemoteMtimeMs + 10
			if err := env.fsClient.SetAttrs(ctx, "/"+rel, client.AttrUpdate{MtimeMs: &stamp}); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(env.localRoot, rel)
			if err := os.Chtimes(abs, time.UnixMilli(stamp), time.UnixMilli(stamp)); err != nil {
				t.Fatal(err)
			}
			if hasStored {
				// Model the completed old download: its new local inode time is
				// known, while its remote baseline still describes the old bytes.
				baseline.LocalMtimeMs = stamp
				d.reconciler.stageSyncEntry(rel, baseline)
			} else {
				d.reconciler.state.mu.Lock()
				delete(d.reconciler.state.state.Entries, rel)
				d.reconciler.state.mu.Unlock()
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if got := env.readLocalFile(t, rel); got != "after!" {
				t.Fatalf("equal size/mtime hid the remote update: local=%q, baseline=%+v", got, d.Snapshot().Entries[rel])
			}
			if got := d.Snapshot().Entries[rel].RemoteHash; got != sha256Hex([]byte("after!")) {
				t.Fatalf("adopted wrong remote hash: %q", got)
			}
			copies, err := filepath.Glob(abs + ".conflict-*")
			if err != nil {
				t.Fatal(err)
			}
			if hasStored && len(copies) != 0 {
				t.Fatalf("unchanged acknowledged local bytes produced conflicts: %v", copies)
			}
			if !hasStored {
				if len(copies) != 1 {
					t.Fatalf("unacknowledged local bytes were not preserved: %v", copies)
				}
				data, err := os.ReadFile(copies[0])
				if err != nil || string(data) != "before" {
					t.Fatalf("preserved local bytes=%q: %v", data, err)
				}
			} else {
				counter := &mtimeCollisionReadCounter{Client: env.fsClient}
				d.reconciler.fs = counter
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
				if counter.reads != 0 {
					t.Fatalf("stable acknowledged baseline reread %d bodies", counter.reads)
				}
			}
		})
	}
}

func TestSyncSkippedQueuedDownloadRecoversRemoteMtimeCollision(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const rel = "renamed"
	env.writeRemoteFile(t, rel, "before")
	oldTime := time.Unix(10, 0).UnixMilli()
	if err := env.fsClient.SetAttrs(ctx, "/"+rel, client.AttrUpdate{MtimeMs: &oldTime}); err != nil {
		t.Fatal(err)
	}
	// Both notifications are observed while the destination is absent. The
	// second op cannot later overwrite a newly present unacknowledged file.
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
	if len(d.reconciler.downloadCh) != 2 {
		t.Fatal("expected two queued downloads")
	}
	first, second := <-d.reconciler.downloadCh, <-d.reconciler.downloadCh
	if first.HasStored || second.HasStored {
		t.Fatal("test requires both operations to predate the baseline")
	}
	d.downloader.process(ctx, first)
	result := <-d.reconciler.downloadResCh
	if result.Err != nil || result.Skipped {
		t.Fatalf("initial download: %+v", result)
	}
	d.reconciler.handleDownloadResult(ctx, result)
	env.writeRemoteFile(t, rel, "after!")
	// Model the observed millisecond collision between local file creation and
	// the subsequent remote write without depending on scheduler timing.
	stamp := result.LocalMtimeMs
	if err := env.fsClient.SetAttrs(ctx, "/"+rel, client.AttrUpdate{MtimeMs: &stamp}); err != nil {
		t.Fatal(err)
	}
	d.downloader.process(ctx, second)
	result = <-d.reconciler.downloadResCh
	if result.Err != nil || !result.Skipped {
		t.Fatalf("late no-baseline download should defer to recovery: %+v", result)
	}
	d.reconciler.handleDownloadResult(ctx, result)
	select {
	case <-d.reconciler.fullSweepRequests():
	default:
		t.Fatal("skipped download did not request recovery")
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readLocalFile(t, rel); got != "after!" {
		t.Fatalf("recovery lost the last remote notification: %q", got)
	}
	if got := d.Snapshot().Entries[rel].RemoteHash; got != sha256Hex([]byte("after!")) {
		t.Fatalf("wrong recovered baseline: %q", got)
	}
}
