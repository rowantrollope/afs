package client

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

type journalFailureHook struct{}

func (journalFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (journalFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (journalFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xadd" {
			return errors.New("injected disconnect before notification")
		}
		return next(ctx, cmd)
	}
}

func TestPublishedFileHasDurableEventWhenNotificationFails(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	reader := New(rdb, "journal-cut")
	if err := reader.Echo(ctx, "/file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	stream := newKeyBuilder("journal-cut").changesStream()
	before, err := rdb.XRevRangeN(ctx, stream, "+", "-", 1).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatal("missing initial journal")
	}
	writerRedis := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = writerRedis.Close() })
	writerRedis.AddHook(journalFailureHook{})
	if err := New(writerRedis, "journal-cut").Echo(ctx, "/file", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Cat(ctx, "/file")
	if err != nil || string(got) != "new" {
		t.Fatalf("published %q: %v", got, err)
	}
	entries, err := rdb.XRange(ctx, stream, "("+before[0].ID, "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("file committed without durable event: reconnect cannot discover publication")
	}
}

func TestChangeStreamCatchupDoesNotWaitForAnotherChange(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := New(rdb, "caught-up")
	if err := c.Echo(ctx, "/file", []byte("data")); err != nil {
		t.Fatal(err)
	}
	last, err := rdb.XRevRangeN(ctx, newKeyBuilder("caught-up").changesStream(), "+", "-", 1).Result()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.ReadChangeStream(ctx, last[0].ID, 10); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("caught-up journal read blocked waiting for a future event")
	}
}
