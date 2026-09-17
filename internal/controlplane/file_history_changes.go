package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
)

type FileHistoryChange struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	WorkspaceID  string    `json:"workspace_id"`
	SessionID    string    `json:"session_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	User         string    `json:"user,omitempty"`
	Op           string    `json:"op"`
	Path         string    `json:"path"`
	PrevPath     string    `json:"prev_path,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	DeltaBytes   int64     `json:"delta_bytes,omitempty"`
	ContentHash  string    `json:"content_hash,omitempty"`
	PrevHash     string    `json:"prev_hash,omitempty"`
	Mode         uint32    `json:"mode,omitempty"`
	FileID       string    `json:"file_id,omitempty"`
	VersionID    string    `json:"version_id,omitempty"`
	CheckpointID string    `json:"checkpoint_id,omitempty"`
	Source       string    `json:"source,omitempty"`
	Origin       string    `json:"origin,omitempty"`
}

type FileHistoryChangesRequest struct {
	Path, SessionID      string
	Cursor, Since, Until string
	NewestFirst          bool
	Limit                int
}

type FileHistoryChangesResponse struct {
	Entries    []FileHistoryChange `json:"entries"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// GetFileHistoryChanges reads activity independently of version capture and
// retention. The existing stream also drives reconnect recovery for mounts.
// Filtering scans bounded batches and advances past nonmatching rows, so a
// sparse path cannot permanently hide older matching activity.
func (s *Service) GetFileHistoryChanges(ctx context.Context, workspace string, req FileHistoryChangesRequest) (FileHistoryChangesResponse, error) {
	result := FileHistoryChangesResponse{Entries: []FileHistoryChange{}}
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return result, err
	}
	if req.Path != "" {
		req.Path, err = filehistory.NormalizePath(req.Path)
		if err != nil {
			return result, err
		}
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit < 1 || req.Limit > 1000 {
		return result, fmt.Errorf("limit must be between 1 and 1000")
	}
	if req.Cursor != "" {
		if req.Since != "" || req.Until != "" {
			return result, fmt.Errorf("choose cursor or since/until")
		}
		if req.NewestFirst {
			req.Until = req.Cursor
		} else {
			req.Since = req.Cursor
		}
	}
	start, end := "-", "+"
	for _, bound := range []string{req.Since, req.Until} {
		if bound != "" && !validHistoryStreamID(bound) {
			return result, fmt.Errorf("activity cursor must be a Redis stream ID")
		}
	}
	if req.Since != "" {
		start = "(" + req.Since
	}
	if req.Until != "" {
		end = "(" + req.Until
	}
	stream := "afs-lite:{" + id + "}:changes"
	const batchSize, maxScanned = 128, 4096
	for scanned := 0; scanned < maxScanned; {
		var messages []redis.XMessage
		if req.NewestFirst {
			messages, err = s.store.rdb.XRevRangeN(ctx, stream, end, start, batchSize).Result()
		} else {
			messages, err = s.store.rdb.XRangeN(ctx, stream, start, end, batchSize).Result()
		}
		if err != nil {
			return result, err
		}
		for index, message := range messages {
			scanned++
			result.NextCursor = message.ID
			row, ok := historyChangeFromMessage(workspace, message)
			if ok && (req.Path == "" || row.Path == req.Path || row.PrevPath == req.Path) && (req.SessionID == "" || row.SessionID == req.SessionID) {
				result.Entries = append(result.Entries, row)
			}
			if len(result.Entries) == req.Limit {
				if index == len(messages)-1 && len(messages) < batchSize {
					result.NextCursor = ""
				}
				return result, nil
			}
		}
		if len(messages) < batchSize {
			result.NextCursor = ""
			return result, nil
		}
		if req.NewestFirst {
			end = "(" + result.NextCursor
		} else {
			start = "(" + result.NextCursor
		}
	}
	return result, nil
}

func validHistoryStreamID(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func historyChangeFromMessage(workspace string, message redis.XMessage) (FileHistoryChange, bool) {
	var payload struct {
		Origin   string             `json:"origin"`
		Op       string             `json:"op"`
		Paths    []string           `json:"paths"`
		Activity *bool              `json:"activity"`
		Change   *FileHistoryChange `json:"change"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(message.Values["payload"])), &payload); err != nil {
		return FileHistoryChange{}, false
	}
	row := payload.Change
	if row == nil {
		if (payload.Activity != nil && !*payload.Activity) || len(payload.Paths) == 0 || payload.Op == "" {
			return FileHistoryChange{}, false
		}
		// Legacy invalidations did not identify the exact file operation. Keep
		// their original operation name instead of inventing put/delete data.
		row = &FileHistoryChange{Op: payload.Op, Path: payload.Paths[len(payload.Paths)-1], Origin: payload.Origin}
		if len(payload.Paths) > 1 {
			row.PrevPath = payload.Paths[0]
		}
	}
	if row.Path == "" || row.Op == "" {
		return FileHistoryChange{}, false
	}
	row.ID, row.WorkspaceID = message.ID, workspace
	if millis, _, ok := strings.Cut(message.ID, "-"); ok {
		if value, err := strconv.ParseInt(millis, 10, 64); err == nil {
			row.OccurredAt = time.UnixMilli(value).UTC()
		}
	}
	if row.Origin == "" {
		row.Origin = payload.Origin
	}
	return *row, true
}
