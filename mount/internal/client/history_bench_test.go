package client

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/rowantrollope/afs/internal/filehistory"
)

// This measures local Redis publication cost, including bounded retention. It
// starts owned disposable servers; use an Array-capable redis-server on PATH to
// compare backends. History owns complete snapshots even for small range writes.
func BenchmarkFileHistoryCapture(b *testing.B) {
	for _, size := range []int{4096, 1 << 20} {
		for _, mutation := range []string{"whole", "range4096"} {
			for _, mode := range []string{"off", "all"} {
				b.Run(fmt.Sprintf("%d/%s/%s", size, mutation, mode), func(b *testing.B) {
					rdb, ctx := setupBenchRedis(b)
					const id = "history-benchmark"
					c := New(rdb, id).(*nativeClient)
					if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: mode, MaxVersions: 3}); err != nil {
						b.Fatal(err)
					}
					body := bytes.Repeat([]byte("x"), size)
					if err := c.Echo(ctx, "/file", body); err != nil {
						b.Fatal(err)
					}
					stat, err := c.Stat(ctx, "/file")
					if err != nil {
						b.Fatal(err)
					}
					if err := rdb.ConfigSet(ctx, "slowlog-log-slower-than", "0").Err(); err != nil {
						b.Fatal(err)
					}
					if err := rdb.ConfigSet(ctx, "slowlog-max-len", "4096").Err(); err != nil {
						b.Fatal(err)
					}
					if err := rdb.SlowLogReset(ctx).Err(); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						body[0] = byte(i)
						var err error
						if mutation == "whole" {
							err = c.Echo(ctx, "/file", body)
						} else {
							err = c.WriteInodeAt(ctx, stat.Inode, body[:4096], 0)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					logs, err := rdb.SlowLogGet(ctx, 4096).Result()
					if err != nil {
						b.Fatal(err)
					}
					var publicationUS, publicationMaxUS, publications float64
					for _, entry := range logs {
						if len(entry.Args) > 1 && entry.Args[0] == "evalsha" && entry.Args[1] == publishFileScript.Hash() {
							us := float64(entry.Duration.Microseconds())
							publicationUS += us
							publications++
							if us > publicationMaxUS {
								publicationMaxUS = us
							}
						}
					}
					if publications > 0 {
						b.ReportMetric(publicationUS/publications, "server-publish-us")
						b.ReportMetric(publicationMaxUS, "max-server-publish-us")
					}
					var cursor uint64
					var memory int64
					for {
						keys, next, err := rdb.Scan(ctx, cursor, filehistory.Prefix(id)+"*", 128).Result()
						if err != nil {
							b.Fatal(err)
						}
						for _, key := range keys {
							memory += rdb.MemoryUsage(ctx, key).Val()
						}
						cursor = next
						if cursor == 0 {
							break
						}
					}
					b.ReportMetric(float64(memory), "history-B")
				})
			}
		}
	}
}
