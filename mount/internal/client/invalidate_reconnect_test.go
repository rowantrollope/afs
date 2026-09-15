package client

import (
	"context"
	"testing"
	"time"
)

func TestInvalidationReconnectReportsRealResubscription(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	reader := NewWithCache(rdb, "reconnect-callback", time.Hour).(*nativeClient)
	seen := make(chan InvalidateEvent, 8)
	reconnected := make(chan struct{}, 8)
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := reader.SubscribeInvalidationsWithReconnect(subCtx, func(ev InvalidateEvent) { seen <- ev }, func() { reconnected <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	channel := reader.keys.invalidateChannel()
	waitForSubscriber(t, rdb, channel)
	select {
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("initial subscription did not report confirmation")
	}
	verifyMessage := func() {
		t.Helper()
		payload, _ := encodeInvalidate(InvalidateEvent{Origin: "test-peer", Op: InvalidateOpInode, Paths: []string{"/file"}})
		if err := rdb.Publish(ctx, channel, string(payload)).Err(); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-seen:
			if len(ev.Paths) != 1 || ev.Paths[0] != "/file" {
				t.Fatalf("invalid message after subscription: %+v", ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("subscription did not deliver message")
		}
	}
	verifyMessage()
	select {
	case <-reconnected:
		t.Fatal("initial subscription reported more than once")
	default:
	}
	for i := 0; i < 2; i++ {
		// This server belongs only to this test and has one pubsub connection.
		// Killing it exercises go-redis's transparent reconnect inside Channel.
		killed, err := rdb.ClientKillByFilter(ctx, "TYPE", "pubsub").Result()
		if err != nil || killed != 1 {
			t.Fatalf("kill owned subscription: %d %v", killed, err)
		}
		select {
		case <-reconnected:
		case <-time.After(2 * time.Second):
			t.Fatal("Redis subscription recovered without invoking reconnect callback")
		}
		verifyMessage()
		select {
		case <-reconnected:
			t.Fatal("one resubscription reported more than once")
		default:
		}
	}
}
