package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// WorkspaceGenerationKey is deliberately outside root inode/content cleanup.
// Deletion retains a tombstone so an old daemon cannot recreate this ID.
func WorkspaceGenerationKey(id string) string { return "afs-lite:{" + id + "}:generation" }

func newWorkspaceGeneration() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "g_" + hex.EncodeToString(token[:]), nil
}

func (s *Store) WorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	meta, err := s.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return "", err
	}
	generation, err := s.rdb.Get(ctx, WorkspaceGenerationKey(workspaceStorageID(meta))).Result()
	if err != nil {
		return "", err
	}
	if generation == "deleted" {
		return "", os.ErrNotExist
	}
	if !strings.HasPrefix(generation, "g_") {
		return "", fmt.Errorf("workspace is being restored; wait for restore to complete")
	}
	return generation, nil
}

func (s *Service) WorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	return s.store.WorkspaceGeneration(ctx, workspace)
}
