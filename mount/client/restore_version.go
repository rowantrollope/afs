package client

import (
	"context"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	internal "github.com/rowantrollope/afs/mount/internal/client"
)

type RestoreMetadata = internal.RestoreMetadata

func WithFileVersionRestoreMetadata(ctx context.Context, metadata RestoreMetadata) context.Context {
	return internal.WithFileVersionRestoreMetadata(ctx, metadata)
}

func RestoreFileVersion(ctx context.Context, rdb *redis.Client, workspaceID, path string, selected filehistory.Record, content []byte, expected *StatResult, undelete bool) (filehistory.Record, error) {
	return internal.RestoreFileVersion(ctx, rdb, workspaceID, path, selected, content, expected, undelete)
}
