package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/mount/client"
)

type remoteDeleteObservationClient struct {
	client.Client
	path            string
	reads           int
	afterSecondRead func()
	secondError     error
}

func (c *remoteDeleteObservationClient) Stat(ctx context.Context, path string) (*client.StatResult, error) {
	st, err := c.Client.Stat(ctx, path)
	if path == c.path {
		c.reads++
		if c.reads == 2 {
			if c.afterSecondRead != nil {
				c.afterSecondRead()
			}
			if c.secondError != nil {
				return nil, c.secondError
			}
		}
	}
	return st, err
}

func TestSyncRemoteDeleteCannotRetargetNewerBaseline(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const rel = "entry"
	const newerBytes = "newly synchronized local contents"
	env.writeRemoteFile(t, rel, "original")
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
		t.Fatal(err)
	}
	var newer SyncEntry
	wrapped := &remoteDeleteObservationClient{Client: env.fsClient, path: "/" + rel, afterSecondRead: func() {
		// A concurrently completed scan or upload installs a new acknowledged
		// baseline after the two remote reads observed absence.
		env.writeRemoteFile(t, rel, newerBytes)
		env.writeLocalFile(t, rel, newerBytes)
		d.reconciler.stageSyncEntry(rel, SyncEntry{Type: "file", Mode: 0644, Size: int64(len(newerBytes)), LocalHash: sha256Hex([]byte(newerBytes)), RemoteHash: sha256Hex([]byte(newerBytes))})
		newer = d.Snapshot().Entries[rel]
	}}
	d.reconciler.fs = wrapped
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
	for len(d.reconciler.downloadCh) > 0 {
		d.downloader.process(ctx, <-d.reconciler.downloadCh)
		d.reconciler.handleDownloadResult(ctx, <-d.reconciler.downloadResCh)
	}
	if wrapped.reads != 2 {
		t.Fatalf("remote observation reads=%d", wrapped.reads)
	}
	got, err := os.ReadFile(filepath.Join(env.localRoot, rel))
	if err != nil || string(got) != newerBytes {
		t.Fatalf("old remote absence deleted new synchronized contents: %q %v", got, err)
	}
	if after := d.Snapshot().Entries[rel]; after.Version != newer.Version {
		t.Fatalf("old observation changed newer baseline: before=%+v after=%+v", newer, after)
	}
}

func TestSyncRemoteDeleteRetryFailureDoesNotConfirmAbsence(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	env.writeRemoteFile(t, "entry", "original")
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	before := d.Snapshot().Entries["entry"]
	if err := env.fsClient.Rm(ctx, "/entry"); err != nil {
		t.Fatal(err)
	}
	d.reconciler.fs = &remoteDeleteObservationClient{Client: env.fsClient, path: "/entry", secondError: errors.New("test connection failed")}
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/entry"})
	if len(d.reconciler.downloadCh) > 0 {
		t.Fatal("failed confirming read queued destructive remote delete")
	}
	if after := d.Snapshot().Entries["entry"]; after.Version != before.Version {
		t.Fatalf("failed read advanced deletion baseline: before=%+v after=%+v", before, after)
	}
}

func TestSyncQueuedRemoteDeleteSkipsRecreatedRemoteFile(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	env.writeRemoteFile(t, "entry", "original")
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/entry"); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/entry"})
	if len(d.reconciler.downloadCh) == 0 {
		t.Fatal("missing queued delete")
	}
	op := <-d.reconciler.downloadCh
	env.writeRemoteFile(t, "entry", "peer recreation")
	d.downloader.process(ctx, op)
	result := <-d.reconciler.downloadResCh
	if result.Err != nil || !result.Skipped {
		t.Fatalf("obsolete remote absence applied after recreation: %+v", result)
	}
	if got, err := os.ReadFile(filepath.Join(env.localRoot, "entry")); err != nil || string(got) != "original" {
		t.Fatalf("queued delete removed local tree after remote recreation: %q %v", got, err)
	}
}

func TestSyncRecoveryDeleteSkipsRecreatedRemoteFile(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	env.writeRemoteFile(t, "entry", "original")
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stored := d.Snapshot().Entries["entry"]
	if err := env.fsClient.Rm(ctx, "/entry"); err != nil {
		t.Fatal(err)
	}
	// The full scan's missing observation is now obsolete before execution.
	action := syncAction{path: "entry", absPath: filepath.Join(env.localRoot, "entry"), storedEntry: stored, hasStored: true}
	env.writeRemoteFile(t, "entry", "peer recreation")
	if err := d.full.execDeleteLocal(ctx, action); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(action.absPath); err != nil || string(got) != "original" {
		t.Fatalf("stale recovery delete removed local file: %q %v", got, err)
	}
	select {
	case <-d.reconciler.fullSweepRequest:
	default:
		t.Fatal("recreated remote file did not request a fresh recovery scan")
	}
}
