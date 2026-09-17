package client

import (
	"context"

	internal "github.com/rowantrollope/afs/mount/internal/client"
)

type FileVersionMutationMetadata = internal.FileVersionMutationMetadata

// WithFileVersionMutationMetadata attaches optional caller provenance to
// ordinary file publications and their activity records.
func WithFileVersionMutationMetadata(ctx context.Context, metadata FileVersionMutationMetadata) context.Context {
	return internal.WithFileVersionMutationMetadata(ctx, metadata)
}
