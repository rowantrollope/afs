package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func (s *Service) RestoreFileVersion(ctx context.Context, workspace, rawPath string, selector FileVersionSelector) (FileVersionRestoreResponse, error) {
	name, err := filehistory.NormalizePath(rawPath)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	generation, err := s.WorkspaceGeneration(ctx, id)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	ctx = afsclient.WithWorkspaceGeneration(ctx, generation)
	selected, err := s.selectedHistoryRecord(ctx, id, selector)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	if selected.Deleted {
		return FileVersionRestoreResponse{}, fmt.Errorf("version is a tombstone; use undelete instead")
	}
	if selected.MetadataOnly {
		return FileVersionRestoreResponse{}, fmt.Errorf("version content was excluded by the file history policy")
	}
	client := afsclient.New(s.store.rdb, id)
	expected, err := client.Stat(ctx, name)
	if errors.Is(err, redis.Nil) || errors.Is(err, os.ErrNotExist) {
		expected, err = nil, nil
	}
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	if expected != nil && expected.Type == "dir" {
		return FileVersionRestoreResponse{}, fmt.Errorf("path is a directory")
	}
	if name != selected.Path {
		if expected == nil {
			return FileVersionRestoreResponse{}, fmt.Errorf("version belongs to a different file path")
		}
		lineage, err := s.store.rdb.HGet(ctx, workspaceFSInodeKey(id, strconv.FormatUint(expected.Inode, 10)), "history_id").Result()
		if err != nil || lineage != selected.FileID {
			return FileVersionRestoreResponse{}, fmt.Errorf("version belongs to a different file path")
		}
	}
	_, content, err := filehistory.Get(ctx, s.store.rdb, id, selected.Path, selected.ID, selected.FileID)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	ctx, err = s.fileVersionRestoreContext(ctx, id)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	record, err := afsclient.RestoreFileVersion(ctx, s.store.rdb, id, name, selected, content, expected, false)
	if err != nil {
		return FileVersionRestoreResponse{}, err
	}
	return FileVersionRestoreResponse{WorkspaceID: workspace, Path: name, Dirty: true, FileID: record.FileID, VersionID: record.ID,
		RestoredFromVersionID: selected.ID, RestoredFromFileID: selected.FileID, RestoredFromOrdinal: selected.Version}, nil
}

func (s *Service) fileVersionRestoreContext(ctx context.Context, id string) (context.Context, error) {
	value, _ := ctx.Value(fileVersionAttributionKey{}).(FileVersionAttribution)
	metadata := afsclient.RestoreMetadata{SessionID: value.SessionID, AgentID: value.AgentID, User: value.User}
	workspace, err := s.store.GetWorkspaceMeta(ctx, id)
	if err != nil {
		return ctx, err
	}
	if workspace.HeadSavepoint != "" {
		metadata.CheckpointIDs = []string{workspace.HeadSavepoint}
	}
	return afsclient.WithFileVersionRestoreMetadata(ctx, metadata), nil
}

func (s *Service) undeleteSelection(ctx context.Context, id, name string, selector FileVersionSelector) (filehistory.Record, error) {
	if selector != (FileVersionSelector{}) {
		selected, err := s.selectedHistoryRecord(ctx, id, selector)
		if err != nil {
			return filehistory.Record{}, err
		}
		if selected.Path != name && selected.PreviousPath != name {
			return filehistory.Record{}, fmt.Errorf("version belongs to a different file path")
		}
		head, err := filehistory.LineageHead(ctx, s.store.rdb, id, selected.FileID)
		if err != nil {
			return filehistory.Record{}, err
		}
		if !head.Deleted {
			return filehistory.Record{}, fmt.Errorf("file lineage is not deleted")
		}
		if selected.Deleted {
			selected, _, err = filehistory.Get(ctx, s.store.rdb, id, name, "latest", selected.FileID)
			return selected, err
		}
		return selected, nil
	}
	var before int64
	for {
		page, err := filehistory.Lineages(ctx, s.store.rdb, id, name, 100, before)
		if err != nil {
			return filehistory.Record{}, err
		}
		for _, lineage := range page.Files {
			head, err := filehistory.LineageHead(ctx, s.store.rdb, id, lineage.FileID)
			if err != nil {
				return filehistory.Record{}, err
			}
			if head.Deleted {
				selected, _, err := filehistory.Get(ctx, s.store.rdb, id, name, "latest", lineage.FileID)
				return selected, err
			}
		}
		if page.NextBefore == 0 {
			return filehistory.Record{}, os.ErrNotExist
		}
		before = page.NextBefore
	}
}

func (s *Service) UndeleteFileVersion(ctx context.Context, workspace, rawPath string, selector FileVersionSelector) (FileVersionUndeleteResponse, error) {
	name, err := filehistory.NormalizePath(rawPath)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	generation, err := s.WorkspaceGeneration(ctx, id)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	ctx = afsclient.WithWorkspaceGeneration(ctx, generation)
	selected, err := s.undeleteSelection(ctx, id, name, selector)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	if selected.MetadataOnly {
		return FileVersionUndeleteResponse{}, fmt.Errorf("version content was excluded by the file history policy")
	}
	client := afsclient.New(s.store.rdb, id)
	if stat, err := client.Stat(ctx, name); err == nil && stat != nil {
		return FileVersionUndeleteResponse{}, ErrWorkspaceConflict
	} else if err != nil && !errors.Is(err, redis.Nil) && !errors.Is(err, os.ErrNotExist) {
		return FileVersionUndeleteResponse{}, err
	}
	_, content, err := filehistory.Get(ctx, s.store.rdb, id, selected.Path, selected.ID, selected.FileID)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	ctx, err = s.fileVersionRestoreContext(ctx, id)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	record, err := afsclient.RestoreFileVersion(ctx, s.store.rdb, id, name, selected, content, nil, true)
	if err != nil {
		return FileVersionUndeleteResponse{}, err
	}
	return FileVersionUndeleteResponse{WorkspaceID: workspace, Path: name, Dirty: true, FileID: record.FileID, VersionID: record.ID,
		UndeletedFromVersionID: selected.ID, UndeletedFromFileID: selected.FileID, UndeletedFromOrdinal: selected.Version}, nil
}
