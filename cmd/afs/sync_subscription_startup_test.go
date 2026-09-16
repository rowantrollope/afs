package main

import (
	"context"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

type delayedInitialSubscriptionClient struct {
	client.Client
	entered chan struct{}
	release chan struct{}
}

func (c *delayedInitialSubscriptionClient) SubscribeInvalidationsWithReconnect(ctx context.Context, handler func(client.InvalidateEvent), onReconnect func()) error {
	close(c.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return c.Client.SubscribeInvalidationsWithReconnect(ctx, handler, onReconnect)
	}
}

func TestSyncInitialSubscriptionRecoversRemoteDelete(t *testing.T) {
	env := newSyncTestEnv(t)
	env.writeLocalFile(t, "doomed.txt", "previously synchronized bytes")
	delayed := &delayedInitialSubscriptionClient{entered: make(chan struct{}), release: make(chan struct{})}
	env.startDaemon(t, func(cfg *syncDaemonConfig) {
		delayed.Client = cfg.FS
		cfg.FS = delayed
	})
	defer env.stopDaemon()

	select {
	case <-delayed.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription pump did not start")
	}
	// Initial reconciliation has finished, but Redis has not confirmed the
	// first live subscription.
	// A deletion in this gap has no later publication to wake the daemon.
	if err := env.fsClient.Rm(context.Background(), "/doomed.txt"); err != nil {
		t.Fatal(err)
	}
	close(delayed.release)
	assertEventually(t, 3*time.Second, "remote deletion missed before first subscription", func() bool {
		return !env.localExists("doomed.txt")
	})
}
