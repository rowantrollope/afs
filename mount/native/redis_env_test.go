package native

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/client"
)

func TestNativeRedisPasswordEnvironment(t *testing.T) {
	server := miniredis.RunT(t)
	server.RequireAuth("environment-secret")
	t.Setenv("AFS_REDIS_PASSWORD", "environment-secret")
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), Password: "environment-secret"})
	defer rdb.Close()
	ctx := context.Background()
	if err := client.New(rdb, "env-test").Mkdir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs-lite:{env-test}:generation", "generation-1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	session, err := StartExport(ctx, Config{Backend: "nfs", RedisURL: "redis://:wrong@" + server.Addr(), RedisKey: "env-test", Generation: "generation-1"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := session.Unmount(cleanup, true); err != nil {
		t.Fatal(err)
	}
}
