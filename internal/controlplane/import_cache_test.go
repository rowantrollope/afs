package controlplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func TestStreamingImportReusesBoundedBlobCache(t *testing.T) {
	for _, test := range []struct {
		name     string
		limit    int64
		blobs    map[string][]byte
		fileIDs  []string
		wantGets map[string]int
	}{
		{
			name: "duplicate blobs reuse cached content after flush", limit: BlobWriterMaxBytes,
			blobs:   map[string][]byte{"shared": bytes.Repeat([]byte{0, 1, 255}, 24*1024)},
			fileIDs: makeRepeatedIDs("shared", 64), wantGets: map[string]int{},
		},
		{
			name: "overflow and oversized blobs use Redis without evicting cached entries", limit: 16 * 1024,
			blobs: map[string][]byte{
				"first": bytes.Repeat([]byte("a"), 8*1024), "overflow": bytes.Repeat([]byte("b"), 10*1024),
				"fits": bytes.Repeat([]byte("c"), 8*1024), "oversized": bytes.Repeat([]byte("d"), 20*1024),
			},
			fileIDs:  []string{"first", "overflow", "first", "fits", "oversized", "oversized"},
			wantGets: map[string]int{"overflow": 1, "oversized": 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, rdb := serviceFixture(t)
			ctx := context.Background()
			var counting atomic.Bool
			counting.Store(true)
			var mu sync.Mutex
			gets := map[string]int{}
			writes := map[string]int{}
			scans := 0
			count := func(cmd redis.Cmder) {
				if !counting.Load() || len(cmd.Args()) < 2 {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				key := fmt.Sprint(cmd.Args()[1])
				if cmd.Name() == "scan" {
					scans++
				}
				_, id, blob := strings.Cut(key, ":blob:")
				if blob && cmd.Name() == "get" {
					gets[id]++
				}
				if blob && cmd.Name() == "set" {
					writes[id]++
				}
			}
			rdb.AddHook(faultHook{
				process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
					count(cmd)
					return next(ctx, cmd)
				},
				pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
					for _, cmd := range cmds {
						count(cmd)
					}
					return next(ctx, cmds)
				},
			})
			var writer *BlobWriter
			meta, err := s.CreateWorkspaceStreaming(ctx, "import", func(id string, w *BlobWriter) (Manifest, error) {
				writer = w
				if w.cacheLimit != BlobWriterMaxBytes {
					t.Fatalf("service cache limit = %d, want %d", w.cacheLimit, BlobWriterMaxBytes)
				}
				w.enableImportCache(test.limit)
				// Force several pipeline boundaries without changing the cache budget.
				w.FlushMaxBytes = 1
				m := Manifest{Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0o750}}}
				for i, blobID := range test.fileIDs {
					data := test.blobs[blobID]
					if err := w.Submit(ctx, blobID, data, int64(len(data))); err != nil {
						return Manifest{}, err
					}
					if i == 0 {
						if err := w.Flush(ctx); err != nil {
							return Manifest{}, err
						}
					}
					m.Entries[fmt.Sprintf("/file-%03d", i)] = ManifestEntry{Type: "file", Mode: 0o640, Size: int64(len(data)), BlobID: blobID}
				}
				if w.cacheBytes > test.limit {
					t.Fatalf("cache grew beyond budget: %d > %d", w.cacheBytes, test.limit)
				}
				return m, nil
			})
			counting.Store(false)
			if err != nil {
				t.Fatal(err)
			}
			if scans != 0 {
				t.Fatalf("fresh namespace was scanned %d times", scans)
			}
			if len(gets) != len(test.wantGets) {
				t.Fatalf("blob reads = %v, want %v", gets, test.wantGets)
			}
			for id, data := range test.blobs {
				if gets[id] != test.wantGets[id] || writes[id] != 1 {
					t.Fatalf("blob %s: reads=%d want=%d, writes=%d want=1", id, gets[id], test.wantGets[id], writes[id])
				}
				stored, err := rdb.Get(ctx, BlobKey(meta.ID, id)).Bytes()
				if err != nil || !bytes.Equal(stored, data) {
					t.Fatalf("immutable blob %s differs: %v", id, err)
				}
			}
			client := afsclient.New(rdb, meta.ID)
			for i, id := range test.fileIDs {
				got, err := client.Cat(ctx, fmt.Sprintf("/file-%03d", i))
				if err != nil || !bytes.Equal(got, test.blobs[id]) {
					t.Fatalf("materialized file %d differs: %v", i, err)
				}
			}
			assertImportCacheReleased(t, writer)
		})
	}
}

func makeRepeatedIDs(id string, count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = id
	}
	return ids
}

func assertImportCacheReleased(t *testing.T, writer *BlobWriter) {
	t.Helper()
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.cachedBlobs != nil || writer.cacheBytes != 0 || writer.cacheLimit != 0 {
		t.Fatal("import retained cache after completion")
	}
}

func TestStreamingImportCachePreservesSubmittedBytes(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	want := bytes.Repeat([]byte("original"), 1024)
	meta, err := s.CreateWorkspaceStreaming(ctx, "immutable", func(id string, w *BlobWriter) (Manifest, error) {
		// A small slice of a larger read buffer must not retain the whole buffer,
		// or change after the producer reuses its buffer following a flush.
		buffer := make([]byte, len(want)*8)
		copy(buffer, want)
		data := buffer[:len(want)]
		if err := w.Submit(ctx, "blob", data, int64(len(data))); err != nil {
			return Manifest{}, err
		}
		if err := w.Flush(ctx); err != nil {
			return Manifest{}, err
		}
		cached, ok := w.cachedBlob("blob")
		if !ok || cap(cached) > len(want)+1024 {
			t.Fatalf("cache retained original backing allocation: cached=%v cap=%d", ok, cap(cached))
		}
		for i := range buffer {
			buffer[i] = 'x'
		}
		return Manifest{Entries: map[string]ManifestEntry{
			"/": {Type: "dir", Mode: 0o755}, "/file": {Type: "file", Mode: 0o644, Size: int64(len(want)), BlobID: "blob"},
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := afsclient.New(rdb, meta.ID).Cat(ctx, "/file")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("materialization changed submitted bytes: %v", err)
	}
}

func TestStreamingImportReleasesCacheOnFailure(t *testing.T) {
	for _, phase := range []string{"build", "flush", "materialize"} {
		t.Run(phase, func(t *testing.T) {
			s, rdb := serviceFixture(t)
			ctx := context.Background()
			injected := errors.New("injected " + phase + " failure")
			if phase != "build" {
				rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
					for _, cmd := range cmds {
						if len(cmd.Args()) < 2 {
							continue
						}
						key := fmt.Sprint(cmd.Args()[1])
						if phase == "flush" && strings.Contains(key, ":blob:") || phase == "materialize" && strings.Contains(key, ":inode:") {
							return injected
						}
					}
					return next(ctx, cmds)
				}})
			}
			var writer *BlobWriter
			_, err := s.CreateWorkspaceStreaming(ctx, "failed", func(id string, w *BlobWriter) (Manifest, error) {
				writer = w
				data := bytes.Repeat([]byte("x"), 8192)
				if err := w.Submit(ctx, "blob", data, int64(len(data))); err != nil {
					return Manifest{}, err
				}
				if _, ok := w.cachedBlob("blob"); !ok {
					t.Fatal("failure fixture never cached its blob")
				}
				if phase == "build" {
					return Manifest{}, injected
				}
				return Manifest{Entries: map[string]ManifestEntry{
					"/": {Type: "dir", Mode: 0o755}, "/file": {Type: "file", Mode: 0o644, Size: int64(len(data)), BlobID: "blob"},
				}}, nil
			})
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want %v", err, injected)
			}
			assertImportCacheReleased(t, writer)
			if exists, err := s.store.WorkspaceExists(ctx, "failed"); err != nil || exists {
				t.Fatalf("failed import was published: %v %v", exists, err)
			}
		})
	}
}

func TestImportBlobCacheBoundsConcurrentEmptyEntries(t *testing.T) {
	store, _ := newTestStore(t)
	writer := NewBlobWriter(store.rdb, "empty", time.Unix(100, 0))
	writer.enableImportCache(0)
	defer writer.discardImportCache()
	var producers sync.WaitGroup
	for producer := 0; producer < 8; producer++ {
		producers.Add(1)
		go func(producer int) {
			defer producers.Done()
			for i := 0; i < importBlobCacheMaxEntries/4; i++ {
				if err := writer.Submit(context.Background(), fmt.Sprintf("%d-%d", producer, i), nil, 0); err != nil {
					t.Error(err)
					return
				}
			}
		}(producer)
	}
	producers.Wait()
	if err := writer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(writer.cachedBlobs) != importBlobCacheMaxEntries || writer.cacheBytes != 0 {
		t.Fatalf("cache entries=%d bytes=%d", len(writer.cachedBlobs), writer.cacheBytes)
	}
}
