package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

// Gate only the replacement generation. The old workers and strict save keep
// their existing client, so tests can inspect the save boundary before ordinary
// recovery changes either tree.
type syncSaveRecoveryGate struct {
	client.Client
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func gateSyncSaveRecovery(t *testing.T, d *syncDaemon) *syncSaveRecoveryGate {
	t.Helper()
	g := &syncSaveRecoveryGate{Client: d.cfg.FS, entered: make(chan struct{}), release: make(chan struct{})}
	d.cfg.FS = g
	t.Cleanup(g.resume)
	return g
}

func (g *syncSaveRecoveryGate) wait(ctx context.Context) error {
	g.enteredOnce.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *syncSaveRecoveryGate) resume() { g.releaseOnce.Do(func() { close(g.release) }) }

func (g *syncSaveRecoveryGate) awaitRecovery(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement generation did not begin recovery")
	}
}

func (g *syncSaveRecoveryGate) Stat(ctx context.Context, path string) (*client.StatResult, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.Client.Stat(ctx, path)
}

func (g *syncSaveRecoveryGate) LsLong(ctx context.Context, path string) ([]client.LsEntry, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.Client.LsLong(ctx, path)
}

func (g *syncSaveRecoveryGate) Cat(ctx context.Context, path string) ([]byte, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.Client.Cat(ctx, path)
}

func TestSyncSaveFailureResumesUnsavedWork(t *testing.T) {
	env := newSyncTestEnv(t)
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.WatcherDebounce = 300 * time.Millisecond })
	s := &syncSaveService{ctx: context.Background(), active: d}
	t.Cleanup(func() {
		if s.active != nil {
			s.active.Stop()
		}
	})
	env.writeLocalFile(t, "pending.txt", "must eventually sync")
	s.active.reconciler.fs = &syncSaveFaultClient{Client: env.fsClient,
		write: func(context.Context, string, []byte, uint32) error { return errors.New("transient write failure") },
	}
	result := s.save(saveRequestForDaemon(s.active, 5*time.Second))
	if result.Success || s.active == nil {
		t.Fatalf("expected failed save and resumed daemon: %+v", result)
	}
	// The pending watcher timer is cancelled by save. The replacement generation
	// uses the original healthy client. No further
	// application write or second explicit save should be needed to resume sync.
	assertEventually(t, 2*time.Second, "pending file after save failure", func() bool {
		got, err := env.fsClient.Cat(context.Background(), "/pending.txt")
		return err == nil && string(got) == "must eventually sync"
	})
}
