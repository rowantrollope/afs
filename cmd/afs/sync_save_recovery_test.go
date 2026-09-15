package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
