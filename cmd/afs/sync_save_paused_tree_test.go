package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncSavePausedTreeClient struct {
	*syncSaveInflightClient
	recoveryStarted chan struct{}
	recoveryRelease chan struct{}
	recoveryOnce    sync.Once
}

func (c *syncSavePausedTreeClient) Echo(ctx context.Context, path string, data []byte) error {
	if path == "/old" {
		select {
		case <-c.stored:
			// Keep resumed sync from publishing the edit until the test has
			// inspected the failed save's remote tree.
			c.recoveryOnce.Do(func() { close(c.recoveryStarted) })
			select {
			case <-c.recoveryRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
		}
	}
	return c.syncSaveInflightClient.Echo(ctx, path, data)
}

func TestSyncSaveRejectsLocalChangesDuringDrain(t *testing.T) {
	env := newSyncTestEnv(t)
	gate := &syncSavePausedTreeClient{
		syncSaveInflightClient: &syncSaveInflightClient{Client: env.fsClient, stored: make(chan struct{}), release: make(chan struct{}), postStat: make(chan error, 1)},
		recoveryStarted:        make(chan struct{}),
		recoveryRelease:        make(chan struct{}),
	}
	var release, releaseRecovery sync.Once
	defer release.Do(func() { close(gate.release) })
	defer releaseRecovery.Do(func() { close(gate.recoveryRelease) })
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.FS = gate })
	if err := d.watcher.Close(); err != nil {
		t.Fatal(err)
	}
	service := &syncSaveService{ctx: context.Background(), active: d}
	t.Cleanup(func() {
		if service.active != nil {
			service.active.Stop()
		}
	})
	abs := env.writeLocalFile(t, "old", "before pause")
	d.reconciler.queueUpload(uploadOp{Kind: opUploadFile, Path: "old", AbsPath: abs, Content: []byte("before pause"), LocalHash: sha256Hex([]byte("before pause")), Mode: 0o644})
	select {
	case <-gate.stored:
	case <-time.After(5 * time.Second):
		t.Fatal("upload never started")
	}
	done := make(chan syncControlResult, 1)
	go func() { done <- service.save(saveRequestForDaemon(d, 5*time.Second)) }()
	select {
	case <-d.uploader.stopCh:
	case <-time.After(5 * time.Second):
		t.Fatal("save did not pause sync")
	}
	env.writeLocalFile(t, "old", "changed during pause")
	release.Do(func() { close(gate.release) })
	select {
	case result := <-done:
		if result.Success || result.Save != nil || !strings.Contains(result.Error, "local tree changed while pausing") {
			t.Fatalf("changed tree acknowledged: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("save did not finish")
	}
	if service.active == nil || service.active == d {
		t.Fatal("failed save did not resume a fresh sync generation")
	}
	select {
	case <-gate.recoveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed sync did not recover the local change")
	}
	if env.readLocalFile(t, "old") != "changed during pause" {
		t.Fatal("local change was overwritten")
	}
	if env.readRemoteFile(t, "old") != "before pause" {
		t.Fatal("failed save published a different local tree")
	}
	// A failed save still resumes ordinary sync. Its later publication does
	// not constitute a save receipt and must not race the assertion above.
	releaseRecovery.Do(func() { close(gate.recoveryRelease) })
	assertEventually(t, 3*time.Second, "resumed sync to publish the preserved local change", func() bool {
		return env.readRemoteFile(t, "old") == "changed during pause"
	})
}
