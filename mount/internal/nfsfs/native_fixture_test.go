package nfsfs

import (
	"context"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/client"
	"testing"
	"time"
)

func nativeTestClient(t *testing.T, rdb *redis.Client, key string, ttl ...time.Duration) client.NativeClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := client.New(rdb, key).Mkdir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.SetNX(ctx, "afs-lite:{"+key+"}:generation", "test-generation", 0).Err(); err != nil {
		t.Fatal(err)
	}
	cacheTTL := time.Duration(0)
	if len(ttl) > 0 {
		cacheTTL = ttl[0]
	}
	c, err := client.NewNativeWithCache(ctx, rdb, key, "test-generation", cacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
