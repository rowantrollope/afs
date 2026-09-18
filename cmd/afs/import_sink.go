package main

import (
	"context"
	"github.com/rowantrollope/afs/internal/controlplane"
)

// Adapts the retained parallel scanner to the retained pipelined blob writer.
type importSink struct {
	ctx    context.Context
	writer controlplane.BlobSink
}

func (s importSink) Submit(id string, data []byte, size int64) error {
	return s.writer.Submit(s.ctx, id, data, size)
}
