package client

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	internal "github.com/rowantrollope/afs/mount/internal/client"
)

type Client = internal.Client
type StatResult = internal.StatResult
type LsEntry = internal.LsEntry
type InfoResult = internal.InfoResult
type InvalidateEvent = internal.InvalidateEvent
type AttrUpdate = internal.AttrUpdate
type ChangeStreamEntry = internal.ChangeStreamEntry
type VersionedSnapshot = internal.VersionedSnapshot
type MutationObserver = internal.MutationObserver

var ErrStreamTrimmed = internal.ErrStreamTrimmed

const (
	RenameNoreplace         = internal.RenameNoreplace
	InvalidateOpInode       = internal.InvalidateOpInode
	InvalidateOpDir         = internal.InvalidateOpDir
	InvalidateOpPrefix      = internal.InvalidateOpPrefix
	InvalidateOpContent     = internal.InvalidateOpContent
	InvalidateOpRootReplace = internal.InvalidateOpRootReplace
)

func New(rdb *redis.Client, key string) Client {
	return internal.New(rdb, key)
}

func NewWithCache(rdb *redis.Client, key string, ttl time.Duration) Client {
	return internal.NewWithCache(rdb, key, ttl)
}

func PublishInvalidation(ctx context.Context, rdb *redis.Client, key string, ev InvalidateEvent) error {
	return internal.PublishInvalidation(ctx, rdb, key, ev)
}

func NewWithObserver(rdb *redis.Client, key string, observer MutationObserver) Client {
	return internal.NewWithObserver(rdb, key, observer)
}

func NewWithCacheAndObserver(rdb *redis.Client, key string, ttl time.Duration, observer MutationObserver) Client {
	return internal.NewWithCacheAndObserver(rdb, key, ttl, observer)
}

var ErrWriteConflict = internal.ErrWriteConflict
var ErrWorkspaceChanged = internal.ErrWorkspaceChanged

func WithExpectedStat(ctx context.Context, stat *StatResult) context.Context {
	return internal.WithExpectedStat(ctx, stat)
}
func WithWorkspaceGeneration(ctx context.Context, generation string) context.Context {
	return internal.WithWorkspaceGeneration(ctx, generation)
}
