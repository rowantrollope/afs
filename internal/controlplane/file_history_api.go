package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func (s *Service) historyWorkspaceID(ctx context.Context, workspace string) (string, error) {
	meta, err := s.store.GetWorkspaceMeta(ctx, workspace)
	return workspaceStorageID(meta), err
}

func (s *Service) GetFileHistory(ctx context.Context, workspace, rawPath string, newestFirst bool) (FileHistoryResponse, error) {
	return s.GetFileHistoryPage(ctx, workspace, FileHistoryRequest{Path: rawPath, NewestFirst: newestFirst})
}

func (s *Service) GetFileHistoryPage(ctx context.Context, workspace string, req FileHistoryRequest) (FileHistoryResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileHistoryResponse{}, err
	}
	name, err := filehistory.NormalizePath(req.Path)
	if err != nil {
		return FileHistoryResponse{}, err
	}
	if req.Limit < 0 || req.Limit > 1000 {
		return FileHistoryResponse{}, fmt.Errorf("limit must be between 0 and 1000")
	}
	page, err := filehistory.PathPage(ctx, s.store.rdb, id, name, req.Limit, req.Cursor, req.NewestFirst)
	if err != nil {
		return FileHistoryResponse{}, err
	}
	if len(page.Versions) == 0 && req.Cursor == "" {
		return FileHistoryResponse{}, os.ErrNotExist
	}
	result := FileHistoryResponse{WorkspaceID: workspace, Path: name, Order: "asc", Lineages: []FileHistoryLineage{}, NextCursor: page.NextCursor}
	if req.NewestFirst {
		result.Order = "desc"
	}
	indices := map[string]int{}
	for _, record := range page.Versions {
		index, ok := indices[record.FileID]
		if !ok {
			head, err := filehistory.LineageHead(ctx, s.store.rdb, id, record.FileID)
			if err != nil {
				return FileHistoryResponse{}, err
			}
			state, path := FileLineageStateLive, head.Path
			if head.Deleted {
				state = FileLineageStateDeleted
			}
			if livePath, err := filehistory.CurrentPath(ctx, s.store.rdb, id, record.FileID); err == nil && livePath != "" {
				path, state = livePath, FileLineageStateLive
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return FileHistoryResponse{}, err
			}
			index = len(result.Lineages)
			indices[record.FileID] = index
			result.Lineages = append(result.Lineages, FileHistoryLineage{FileID: record.FileID, State: state, CurrentPath: path, Versions: []FileVersion{}})
		}
		result.Lineages[index].Versions = append(result.Lineages[index].Versions, compatibleFileVersion(record))
	}
	return result, nil
}

func (s *Service) selectedHistoryRecord(ctx context.Context, id string, selector FileVersionSelector) (filehistory.Record, error) {
	if selector.VersionID != "" {
		if selector.FileID != "" || selector.Ordinal != 0 {
			return filehistory.Record{}, fmt.Errorf("choose version_id or file_id+ordinal")
		}
		return filehistory.RecordByID(ctx, s.store.rdb, id, selector.VersionID)
	}
	if selector.FileID == "" || selector.Ordinal <= 0 {
		return filehistory.Record{}, fmt.Errorf("version_id or file_id+ordinal is required")
	}
	return filehistory.RecordAtOrdinal(ctx, s.store.rdb, id, selector.FileID, selector.Ordinal)
}

func (s *Service) GetFileVersionContent(ctx context.Context, workspace, versionID string) (FileVersionContentResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	record, err := s.selectedHistoryRecord(ctx, id, FileVersionSelector{VersionID: versionID})
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	return s.versionContentResponse(ctx, workspace, id, record)
}

func (s *Service) GetFileVersionContentAtOrdinal(ctx context.Context, workspace, fileID string, ordinal int64) (FileVersionContentResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	record, err := s.selectedHistoryRecord(ctx, id, FileVersionSelector{FileID: fileID, Ordinal: ordinal})
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	return s.versionContentResponse(ctx, workspace, id, record)
}

func (s *Service) versionContentResponse(ctx context.Context, workspace, id string, record filehistory.Record) (FileVersionContentResponse, error) {
	result := FileVersionContentResponse{WorkspaceID: workspace, FileID: record.FileID, VersionID: record.ID, Ordinal: record.Version,
		Path: record.Path, Kind: record.Type, Source: record.Source, Target: record.Target, Size: record.Size,
		CreatedAt: time.UnixMilli(record.CreatedAt).UTC().Format(time.RFC3339Nano), MetadataOnly: record.MetadataOnly}
	if record.Deleted {
		result.Kind = FileVersionKindTombstone
		return result, nil
	}
	if record.Type == FileVersionKindSymlink {
		result.Content = record.Target
		return result, nil
	}
	if record.MetadataOnly {
		result.Binary = true
		result.ContentType = "application/octet-stream"
		return result, nil
	}
	_, body, err := filehistory.Get(ctx, s.store.rdb, id, record.Path, record.ID, record.FileID)
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	fillVersionContent(&result, body)
	return result, nil
}

func fillVersionContent(result *FileVersionContentResponse, body []byte) {
	result.Binary = bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body)
	result.ContentType = mime.TypeByExtension(filepath.Ext(result.Path))
	if result.ContentType == "" {
		result.ContentType = http.DetectContentType(body)
	}
	if result.Binary {
		result.DataBase64 = base64.StdEncoding.EncodeToString(body)
		return
	}
	result.Content, result.Encoding = string(body), "utf-8"
	result.Language = map[string]string{".go": "go", ".js": "javascript", ".jsx": "javascript", ".ts": "typescript", ".tsx": "typescript", ".py": "python", ".rb": "ruby", ".rs": "rust", ".json": "json", ".yaml": "yaml", ".yml": "yaml", ".md": "markdown", ".html": "html", ".css": "css", ".sql": "sql", ".sh": "shell"}[strings.ToLower(filepath.Ext(result.Path))]
}

// GetFileContent provides the original history drawer's head/working-copy
// operands, plus immutable checkpoint IDs and names.
func (s *Service) GetFileContent(ctx context.Context, workspace, view, rawPath string) (FileVersionContentResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	name, err := filehistory.NormalizePath(rawPath)
	if err != nil {
		return FileVersionContentResponse{}, err
	}
	return s.fileContent(ctx, workspace, id, view, name)
}

func (s *Service) fileContent(ctx context.Context, workspace, id, view, name string) (FileVersionContentResponse, error) {
	result := FileVersionContentResponse{WorkspaceID: workspace, Path: name}
	if view == "working-copy" {
		generation, err := s.WorkspaceGeneration(ctx, id)
		if err != nil {
			return result, err
		}
		ctx = afsclient.WithWorkspaceGeneration(ctx, generation)
		client := afsclient.New(s.store.rdb, id)
		for attempt := 0; attempt < 5; attempt++ {
			before, err := client.Stat(ctx, name)
			if errors.Is(err, redis.Nil) {
				return result, os.ErrNotExist
			}
			if err != nil {
				return result, err
			}
			if before == nil {
				return result, os.ErrNotExist
			}
			result.Kind, result.Size = before.Type, before.Size
			var body []byte
			if before.Type == "symlink" {
				result.Target, err = client.Readlink(ctx, name)
				result.Content = result.Target
			} else if before.Type == "file" {
				body, err = client.Cat(ctx, name)
			} else {
				return result, fmt.Errorf("path is not a file or symlink")
			}
			if err != nil {
				return result, err
			}
			after, err := client.Stat(ctx, name)
			if err != nil || after == nil || after.Inode != before.Inode || after.Revision != before.Revision {
				continue
			}
			if before.Type == "file" {
				fillVersionContent(&result, body)
			}
			return result, nil
		}
		return result, afsclient.ErrWriteConflict
	}
	if view == "" || view == "head" {
		meta, err := s.store.GetWorkspaceMeta(ctx, id)
		if err != nil {
			return result, err
		}
		view = meta.HeadSavepoint
		if view == "" {
			return result, os.ErrNotExist
		}
	}
	_, manifest, err := s.GetCheckpoint(ctx, id, view)
	if err != nil {
		return result, err
	}
	entry, exists := manifest.Entries[name]
	if !exists {
		return result, os.ErrNotExist
	}
	result.Kind, result.Size, result.Target = entry.Type, entry.Size, entry.Target
	if entry.Type == "symlink" {
		result.Content = entry.Target
		return result, nil
	}
	if entry.Type != "file" {
		return result, fmt.Errorf("path is not a file or symlink")
	}
	body, err := ManifestEntryData(entry, func(blob string) ([]byte, error) { return s.store.GetBlob(ctx, id, blob) })
	if err != nil {
		return result, err
	}
	result.Size = int64(len(body))
	fillVersionContent(&result, body)
	return result, nil
}

func (s *Service) DiffFileVersions(ctx context.Context, workspace, rawPath string, from, to FileVersionDiffOperand) (FileVersionDiffResponse, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return FileVersionDiffResponse{}, err
	}
	name, err := filehistory.NormalizePath(rawPath)
	if err != nil {
		return FileVersionDiffResponse{}, err
	}
	resolve := func(operand FileVersionDiffOperand) (FileVersionContentResponse, string, error) {
		if operand.Ref != "" {
			if operand.VersionID != "" || operand.FileID != "" || operand.Ordinal != 0 {
				return FileVersionContentResponse{}, "", fmt.Errorf("ref cannot be combined with version selectors")
			}
			content, err := s.fileContent(ctx, workspace, id, operand.Ref, name)
			if errors.Is(err, os.ErrNotExist) && operand.Ref == "head" {
				content, err = s.fileContent(ctx, workspace, id, "working-copy", name)
			}
			return content, operand.Ref, err
		}
		record, err := s.selectedHistoryRecord(ctx, id, FileVersionSelector{VersionID: operand.VersionID, FileID: operand.FileID, Ordinal: operand.Ordinal})
		if err != nil {
			return FileVersionContentResponse{}, "", err
		}
		if record.Path != name && record.PreviousPath != name {
			current, err := filehistory.CurrentPath(ctx, s.store.rdb, id, record.FileID)
			if err != nil || current != name {
				return FileVersionContentResponse{}, "", fmt.Errorf("version belongs to a different file path")
			}
		}
		content, err := s.versionContentResponse(ctx, workspace, id, record)
		label := "version:" + record.ID
		if operand.VersionID == "" {
			label = record.FileID + "@" + strconv.FormatInt(record.Version, 10)
		}
		return content, label, err
	}
	left, leftLabel, err := resolve(from)
	if err != nil {
		return FileVersionDiffResponse{}, err
	}
	if to == (FileVersionDiffOperand{}) {
		to.Ref = "head"
	}
	right, rightLabel, err := resolve(to)
	if err != nil {
		return FileVersionDiffResponse{}, err
	}
	result := FileVersionDiffResponse{WorkspaceID: workspace, Path: name, From: leftLabel, To: rightLabel, Binary: left.Binary || right.Binary}
	if left.MetadataOnly || right.MetadataOnly {
		return result, fmt.Errorf("version content was excluded by the file history policy")
	}
	if result.Binary {
		return result, nil
	}
	result.Diff, err = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(left.Content), B: difflib.SplitLines(right.Content), FromFile: leftLabel, ToFile: rightLabel, Context: 3})
	return result, err
}
