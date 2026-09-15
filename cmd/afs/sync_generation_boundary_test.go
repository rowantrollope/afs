package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

func TestSyncRootReplacementRejectsGenerationChangedDuringSnapshot(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	env.writeRemoteFile(t, "file", "remote published bytes")
	env.writeLocalFile(t, "file", "unpublished local bytes")
	generationKey := controlplane.WorkspaceGenerationKey(env.mountKey)
	if err := env.rdb.Set(context.Background(), generationKey, "g_before", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx := client.WithWorkspaceGeneration(context.Background(), "g_before")
	var once sync.Once
	progress := func(_, _ int64) {
		once.Do(func() {
			if err := env.rdb.Set(context.Background(), generationKey, "g_after", 0).Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
	err := d.full.replaceFromRemote(ctx, progress)
	if !errors.Is(err, client.ErrWorkspaceChanged) {
		t.Fatalf("replacement error=%v; local now=%q", err, env.readLocalFile(t, "file"))
	}
	if got := env.readLocalFile(t, "file"); got != "unpublished local bytes" {
		t.Fatalf("stale replacement overwrote pending bytes: %q", got)
	}
}
