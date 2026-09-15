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

func TestDeleteEmptyFilePublishesCountersAndChange(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	const fsKey = "delete-empty-journal"
	c := New(rdb, fsKey)
	if err := c.Echo(ctx, "/keep", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CreateFile(ctx, "/empty", 0o644, true); err != nil {
		t.Fatal(err)
	}
	keys := newKeyBuilder(fsKey)
	if err := rdb.Del(ctx, keys.changesStream(), keys.rootDirty()).Err(); err != nil {
		t.Fatal(err)
	}
	writerRedis := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = writerRedis.Close() })
	// Supplemental notifications cannot hide an error partway through the
	// atomic deletion script: that script must mark dirty and append the event.
	writerRedis.AddHook(journalFailureHook{})
	if err := New(writerRedis, fsKey).Rm(ctx, "/empty"); err != nil {
		t.Errorf("delete empty file: %v", err)
	}
	if stat, err := New(rdb, fsKey).Stat(ctx, "/empty"); err != nil || stat != nil {
		t.Errorf("deleted file stat = %+v, error = %v", stat, err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Files != 1 || info.TotalDataBytes != 4 {
		t.Errorf("counters after deleting empty file: %+v", info)
	}
	if dirty, err := rdb.Get(ctx, keys.rootDirty()).Result(); err != nil || dirty != "1" {
		t.Errorf("root dirty = %q, error = %v", dirty, err)
	}
	entries, err := rdb.XRange(ctx, keys.changesStream(), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		payload, _ := entry.Values["payload"].(string)
		event, err := decodeInvalidate([]byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range event.Paths {
			if event.Op == InvalidateOpInode && path == "/empty" {
				found = true
			}
		}
	}
	if !found {
		t.Error("empty file was deleted without its durable change event")
	}
	if err := c.Rm(ctx, "/keep"); err != nil {
		t.Fatal(err)
	}
	info, err = c.Info(ctx)
	if err != nil || info.Files != 0 || info.TotalDataBytes != 0 {
		t.Fatalf("counters after deleting nonempty file: %+v, %v", info, err)
	}
}
