// Package client provides the Redis-backed filesystem used by folder sync.
package client

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	RenameNoreplace uint32 = 0x1
	RenameExchange  uint32 = 0x2
)

// AttrUpdate is a sparse set of inode attribute changes. A nil pointer in
// any field means "leave that attribute unchanged." It is used by SetAttrs
// to collapse the NFS SETATTR hot path's Chmod + Chown + Utimens triple
// into a single Redis round trip, and to skip the round trip entirely when
// none of the requested fields actually differ from the current state.
type AttrUpdate struct {
	Mode    *uint32
	UID     *uint32
	GID     *uint32
	AtimeMs *int64
	MtimeMs *int64
}

// IsEmpty reports whether every field in the update is nil (i.e. the
// update would change nothing). Callers on the SETATTR hot path should
// skip the Redis round trip entirely when IsEmpty returns true.
func (u AttrUpdate) IsEmpty() bool {
	return u.Mode == nil && u.UID == nil && u.GID == nil &&
		u.AtimeMs == nil && u.MtimeMs == nil
}

// Client provides the filesystem operation surface used by folder sync.
type Client interface {
	Stat(ctx context.Context, path string) (*StatResult, error)
	Cat(ctx context.Context, path string) ([]byte, error)
	Echo(ctx context.Context, path string, data []byte) error
	EchoCreate(ctx context.Context, path string, data []byte, mode uint32) error
	CreateFile(ctx context.Context, path string, mode uint32, exclusive bool) (*StatResult, bool, error)
	EchoAppend(ctx context.Context, path string, data []byte) error
	Touch(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) error
	// MkdirMode uses mode only when creating a directory; existing directories
	// retain their permissions, including when another writer wins creation.
	MkdirMode(ctx context.Context, path string, mode uint32) error
	Rm(ctx context.Context, path string) error
	Ls(ctx context.Context, path string) ([]string, error)
	LsLong(ctx context.Context, path string) ([]LsEntry, error)
	Rename(ctx context.Context, src, dst string, flags uint32) error
	Mv(ctx context.Context, src, dst string) error
	Ln(ctx context.Context, target, linkpath string) error
	Readlink(ctx context.Context, path string) (string, error)
	Chmod(ctx context.Context, path string, mode uint32) error
	Chown(ctx context.Context, path string, uid, gid uint32) error
	Truncate(ctx context.Context, path string, size int64) error
	Utimens(ctx context.Context, path string, atimeMs, mtimeMs int64) error
	// SetAttrs is the batched counterpart of Chmod / Chown / Utimens. It
	// writes all non-nil fields in one partial HSet against the inode
	// hash, collapsing up to three sequential RTTs into a single round
	// trip on the NFS SETATTR hot path. Callers pass nil pointers for
	// fields they do not want to change; an all-nil update is a no-op
	// that returns nil without touching Redis.
	SetAttrs(ctx context.Context, path string, upd AttrUpdate) error
	Info(ctx context.Context) (*InfoResult, error)

	// WriteChunks stages delta chunks and atomically publishes content and
	// metadata if the observed file has not changed.
	// chunks maps chunk-index → data.
	WriteChunks(ctx context.Context, path string, chunks map[int][]byte,
		chunkSize int, newSize int64, hashes []string) error

	// ReadChunks reads specific chunks from a file's content key via pipelined
	// GETRANGE. Returns chunk data by index.
	ReadChunks(ctx context.Context, path string, indices []int,
		chunkSize int) (map[int][]byte, error)

	// ChunkMeta returns the stored chunk_size and chunk_hashes for a file
	// without fetching content. Returns 0/nil for non-chunked files.
	ChunkMeta(ctx context.Context, path string) (int, []string, error)

	// ReadChangeStream reads up to count entries from the per-workspace
	// durable change stream starting strictly after lastID. Pass "0-0" to
	// read from the beginning. Returns ErrStreamTrimmed if lastID has been
	// trimmed (client should fall back to full reconcile).
	ReadChangeStream(ctx context.Context, lastID string, count int64) ([]ChangeStreamEntry, error)

	// SubscribeInvalidationsWithReconnect is like SubscribeInvalidations
	// but calls onReconnect after the initial subscription is confirmed and
	// each time the pub/sub connection is re-established after a drop. This
	// lets callers recover events missed before subscribing or during an outage.
	SubscribeInvalidationsWithReconnect(ctx context.Context, handler func(InvalidateEvent), onReconnect func()) error

	// SubscribeInvalidations runs a goroutine that listens on this FS key's
	// pub/sub channel and dispatches every cross-client invalidation event
	// to handler. Messages originating from this client are filtered out
	// before the handler is invoked. The goroutine runs until ctx is
	// cancelled, transparently reconnecting on Redis outages.
	//
	// The call returns once the subscription has been handed off to the
	// goroutine, not when the first message arrives. Subscribers are also
	// responsible for flushing the local per-client cache; that work happens
	// inside SubscribeInvalidations before handler is called, so handlers
	// only need to worry about cache layers above the client.
	SubscribeInvalidations(ctx context.Context, handler func(InvalidateEvent)) error

	// OriginID returns this client's opaque publisher ID. Primarily exposed
	// for tests that want to verify origin-dedup behavior.
	OriginID() string

	// DisableInvalidationPublishing turns off outgoing PUBLISH calls. Used
	// by the --disable-cross-client-invalidation flag. Local cache state
	// still tracks mutations; other clients just won't be notified.
	DisableInvalidationPublishing()

	// InvalidateCache flushes the entire local attribute/directory-listing
	// cache. Use before full reconciliation scans so stale cached listings
	// don't cause the scanner to miss recently created files.
	InvalidateCache()
}

// PathCacheWarmer is implemented by clients that can prewarm exact-path cache
// entries from backend metadata.
type PathCacheWarmer interface {
	WarmPathCache(ctx context.Context) error
}

// NativeClient adds the inode operations needed by kernel filesystem adapters.
// A native session pins its workspace generation for its entire lifetime.
type NativeClient interface {
	Client
	StatInode(context.Context, uint64) (*StatResult, error)
	InodePath(context.Context, uint64) (string, error)
	ReadInodeAt(context.Context, uint64, int64, int) ([]byte, error)
	WriteInodeAt(context.Context, uint64, []byte, int64) error
	// An offset of -1 appends atomically to the latest committed inode size.
	WriteInodeAtPath(context.Context, uint64, string, []byte, int64) error
	TruncateInode(context.Context, uint64, int64) error
	TruncateInodeAtPath(context.Context, uint64, string, int64) error
	Getlk(context.Context, uint64, string, *FileLock) (*FileLock, error)
	Setlk(context.Context, uint64, string, *FileLock, bool) error
	UnlockAll(context.Context, uint64, string) error
	// Check fences adapter-local cache hits that do not need a data request.
	Check(context.Context) error
	// Barrier waits for requests already admitted by this client and checks
	// its generation and Redis connection. The caller first drains kernel I/O.
	Barrier(context.Context) error
	Close() error
}

// FileLock describes an inclusive advisory byte-range lock. End may be
// math.MaxUint64 to cover the rest of the file.
type FileLock struct {
	Start uint64
	End   uint64
	Type  uint32
	PID   uint32
}

// New creates a filesystem client for the given Redis key.
// It uses the native inode/hash backend plus whichever external content backend
// the connected Redis server supports (`ext` strings everywhere, or `array`
// content keys when Redis Array is available).
func New(rdb *redis.Client, key string) Client {
	return newNativeClient(rdb, key, nil)
}

// NewWithCache creates a filesystem client with an inode cache.
// Repeated path lookups within the TTL window skip Redis round-trips.
// All write operations automatically invalidate affected cache entries.
func NewWithCache(rdb *redis.Client, key string, ttl time.Duration) Client {
	return newNativeClientWithCache(rdb, key, ttl, nil)
}

func NewWithObserver(rdb *redis.Client, key string, observer MutationObserver) Client {
	return newNativeClient(rdb, key, observer)
}

func NewWithCacheAndObserver(rdb *redis.Client, key string, ttl time.Duration, observer MutationObserver) Client {
	return newNativeClientWithCache(rdb, key, ttl, observer)
}
