package controlplane

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
	"strings"
	"time"
)

type SaveCheckpointRequest struct {
	ExpectedChangesID                                                                   string
	ImportLockToken                                                                     string
	ExpectedGeneration                                                                  string
	Workspace, ExpectedHead, CheckpointID, Description, Kind, Source, Author, CreatedBy string
	Manifest                                                                            Manifest
	Blobs                                                                               map[string][]byte
	FileCount, DirCount                                                                 int
	TotalBytes                                                                          int64
	SkipWorkspaceRootSync, AllowUnchanged                                               bool
}
type manifestStatTotals struct {
	FileCount, DirCount int
	TotalBytes          int64
}

func (s *Service) saveCheckpoint(ctx context.Context, input SaveCheckpointRequest) (bool, error) {
	if err := ValidateName("workspace", input.Workspace); err != nil {
		return false, err
	}
	if err := ValidateName("checkpoint", input.CheckpointID); err != nil {
		return false, err
	}
	if err := ValidateName("checkpoint", input.ExpectedHead); err != nil {
		return false, err
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, input.Workspace)
	if err != nil {
		return false, err
	}
	storageID := workspaceStorageID(meta)
	if input.ImportLockToken != "" {
		token, err := s.store.rdb.Get(ctx, ImportLockKey(storageID)).Result()
		if err != nil {
			return false, err
		}
		if token != input.ImportLockToken {
			return false, ErrImportInProgress
		}
	} else if err := CheckImportLock(ctx, s.store, storageID); err != nil {
		return false, err
	}

	headManifest, err := s.store.GetManifest(ctx, storageID, input.ExpectedHead)
	if err != nil {
		return false, err
	}
	if manifestEquivalent(headManifest, input.Manifest) && !input.AllowUnchanged {
		return false, nil
	}

	now := time.Now().UTC()
	manifestHash, err := HashManifest(input.Manifest)
	if err != nil {
		return false, err
	}
	if err := s.store.SaveBlobs(ctx, storageID, input.Blobs); err != nil {
		return false, err
	}

	savepointMeta := SavepointMeta{
		Version:         formatVersion,
		ID:              input.CheckpointID,
		Name:            input.CheckpointID,
		Description:     strings.TrimSpace(input.Description),
		Kind:            input.Kind,
		Source:          input.Source,
		Author:          input.Author,
		CreatedBy:       input.CreatedBy,
		Workspace:       storageID,
		ParentSavepoint: input.ExpectedHead,
		ManifestHash:    manifestHash,
		CreatedAt:       now,
		FileCount:       input.FileCount,
		DirCount:        input.DirCount,
		TotalBytes:      input.TotalBytes,
	}

	err = s.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(storageID))
		if err != nil {
			return err
		}
		if input.ExpectedGeneration != "" {
			generation, err := tx.Get(ctx, WorkspaceGenerationKey(storageID)).Result()
			if err != nil {
				return err
			}
			if generation != input.ExpectedGeneration {
				return ErrWorkspaceConflict
			}
		}
		if input.ExpectedChangesID != "" {
			latest, err := latestServerChange(ctx, tx, storageID)
			if err != nil {
				return err
			}
			if latest != input.ExpectedChangesID {
				return ErrWorkspaceConflict
			}
		}
		if input.ImportLockToken != "" {
			token, err := tx.Get(ctx, ImportLockKey(storageID)).Result()
			if err != nil {
				return err
			}
			if token != input.ImportLockToken {
				return ErrImportInProgress
			}
		} else {
			exists, err := tx.Exists(ctx, ImportLockKey(storageID)).Result()
			if err != nil {
				return err
			}
			if exists != 0 {
				return ErrImportInProgress
			}
		}
		if current.HeadSavepoint != input.ExpectedHead {
			return ErrWorkspaceConflict
		}
		exists, err := tx.Exists(ctx, savepointMetaKey(storageID, input.CheckpointID)).Result()
		if err != nil {
			return err
		}
		if exists > 0 {
			return fmt.Errorf("savepoint %q already exists", input.CheckpointID)
		}

		updatedRefs := map[string]blobRef{}
		for blobID, size := range manifestBlobRefs(input.Manifest) {
			ref, err := getJSON[blobRef](ctx, tx, blobRefKey(storageID, blobID))
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				ref = blobRef{
					BlobID:    blobID,
					Size:      size,
					CreatedAt: now,
				}
			}
			ref.RefCount++
			if ref.Size == 0 {
				ref.Size = size
			}
			updatedRefs[blobID] = ref
		}

		current.HeadSavepoint = input.CheckpointID
		current.UpdatedAt = now
		current.DirtyHint = input.ExpectedChangesID == ""

		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if err := setJSON(ctx, pipe, savepointMetaKey(storageID, input.CheckpointID), savepointMeta); err != nil {
				return err
			}
			if err := setJSON(ctx, pipe, savepointManifestKey(storageID, input.CheckpointID), input.Manifest); err != nil {
				return err
			}
			if err := setJSON(ctx, pipe, workspaceMetaKey(storageID), current); err != nil {
				return err
			}
			pipe.ZAdd(ctx, workspaceSavepointsKey(storageID), redis.Z{
				Score:  float64(now.UnixMilli()),
				Member: input.CheckpointID,
			})
			if input.ExpectedChangesID != "" {
				pipe.Set(ctx, workspaceRootHeadKey(storageID), input.CheckpointID, 0)
				pipe.Set(ctx, workspaceRootDirtyKey(storageID), "0", 0)
			} else {
				pipe.Set(ctx, workspaceRootDirtyKey(storageID), "1", 0)
			}
			for blobID, ref := range updatedRefs {
				if err := setJSON(ctx, pipe, blobRefKey(storageID, blobID), ref); err != nil {
					return err
				}
			}
			return nil
		})
		return err
	}, workspaceMetaKey(storageID), WorkspaceGenerationKey(storageID), "afs:{"+storageID+"}:changes", ImportLockKey(storageID))
	if err != nil {
		if errors.Is(err, ErrWorkspaceConflict) || err == redis.TxFailedErr {
			return false, ErrWorkspaceConflict
		}
		return false, err
	}
	if !input.SkipWorkspaceRootSync {
		if err := SyncWorkspaceRoot(ctx, s.store, storageID, input.Manifest); err != nil {
			return false, err
		}
	}

	if err := s.store.Audit(ctx, storageID, "save", map[string]any{
		"savepoint": input.CheckpointID,
		"parent":    savepointMeta.ParentSavepoint,
		"files":     input.FileCount,
		"dirs":      input.DirCount,
		"bytes":     input.TotalBytes,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func cloneManifest(source Manifest) Manifest {
	cloned := Manifest{
		Version:   source.Version,
		Workspace: source.Workspace,
		Savepoint: source.Savepoint,
		Entries:   make(map[string]ManifestEntry, len(source.Entries)),
	}
	for p, entry := range source.Entries {
		cloned.Entries[p] = entry
	}
	return cloned
}

func manifestEquivalent(a, b Manifest) bool {
	if len(a.Entries) != len(b.Entries) {
		return false
	}
	for p, left := range a.Entries {
		right, ok := b.Entries[p]
		if !ok {
			return false
		}
		if !manifestEntryEquivalent(left, right) {
			return false
		}
	}
	return true
}

func manifestEntryEquivalent(a, b ManifestEntry) bool {
	if a.Type != b.Type || a.Mode != b.Mode || a.Size != b.Size || a.BlobID != b.BlobID || a.Inline != b.Inline || a.Target != b.Target {
		return false
	}
	if a.Type == "symlink" || a.Type == "dir" {
		return true
	}
	return a.MtimeMs == b.MtimeMs
}

func manifestBlobRefs(m Manifest) map[string]int64 {
	refs := map[string]int64{}
	for _, entry := range m.Entries {
		if entry.BlobID == "" {
			continue
		}
		refs[entry.BlobID] = entry.Size
	}
	return refs
}

func manifestStats(m Manifest) manifestStatTotals {
	var stats manifestStatTotals
	for p, entry := range m.Entries {
		if p == "/" {
			continue
		}
		switch entry.Type {
		case "file":
			stats.FileCount++
			stats.TotalBytes += entry.Size
		case "dir":
			stats.DirCount++
		}
	}
	return stats
}
