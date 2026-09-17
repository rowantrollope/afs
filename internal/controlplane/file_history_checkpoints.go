package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"

	"github.com/rowantrollope/afs/internal/filehistory"
)

type FileVersionCheckpointsResponse struct {
	WorkspaceID string          `json:"workspace_id"`
	FileID      string          `json:"file_id"`
	VersionID   string          `json:"version_id"`
	Checkpoints []SavepointMeta `json:"checkpoints"`
}

// GetFileVersionCheckpoints verifies snapshot membership against retained
// manifests. A version's source checkpoint annotation alone is not membership.
func (s *Service) GetFileVersionCheckpoints(ctx context.Context, workspace, versionID string) (FileVersionCheckpointsResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionCheckpointsResponse{}, err
	}
	record, err := filehistory.RecordByID(ctx, s.store.rdb, id, versionID)
	if err != nil {
		return FileVersionCheckpointsResponse{}, err
	}
	result := FileVersionCheckpointsResponse{WorkspaceID: workspace, FileID: record.FileID, VersionID: record.ID, Checkpoints: []SavepointMeta{}}
	if record.Deleted {
		return result, nil
	}
	hash := record.ContentHash
	if record.Type == "file" && hash == "" {
		if record.MetadataOnly {
			return result, nil
		}
		_, body, err := filehistory.Get(ctx, s.store.rdb, id, record.Path, record.ID, record.FileID)
		if err != nil {
			return result, err
		}
		sum := sha256.Sum256(body)
		hash = hex.EncodeToString(sum[:])
	}
	checkpoints, err := s.store.ListSavepoints(ctx, id, 0)
	if err != nil {
		return result, err
	}
	for _, checkpoint := range checkpoints {
		manifest, err := s.store.GetManifest(ctx, id, checkpoint.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		entry, exists := manifest.Entries[record.Path]
		if !exists || entry.Type != record.Type || entry.Mode != record.Mode {
			continue
		}
		if entry.Type == "symlink" {
			if entry.Target == record.Target {
				result.Checkpoints = append(result.Checkpoints, checkpoint)
			}
			continue
		}
		if entry.Size != record.Size {
			continue
		}
		entryHash := entry.BlobID
		if entryHash == "" {
			body, err := base64.StdEncoding.DecodeString(entry.Inline)
			if err != nil {
				return result, err
			}
			sum := sha256.Sum256(body)
			entryHash = hex.EncodeToString(sum[:])
		}
		if hash == entryHash {
			result.Checkpoints = append(result.Checkpoints, checkpoint)
		}
	}
	return result, nil
}
