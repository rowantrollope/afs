package controlplane

import (
	"context"
	"time"

	"github.com/rowantrollope/afs/internal/filehistory"
)

const (
	FileLineageStateLive     = "live"
	FileLineageStateDeleted  = "deleted"
	FileVersionKindFile      = "file"
	FileVersionKindSymlink   = "symlink"
	FileVersionKindTombstone = "tombstone"
)

// FileVersion preserves the original web UI's wire contract. The underlying
// immutable Record remains shared with mounted writers and the compact CLI.
type FileVersion struct {
	VersionID     string    `json:"version_id"`
	FileID        string    `json:"file_id"`
	Ordinal       int64     `json:"ordinal"`
	Path          string    `json:"path"`
	PrevPath      string    `json:"prev_path,omitempty"`
	Op            string    `json:"op"`
	Kind          string    `json:"kind"`
	BlobID        string    `json:"blob_id,omitempty"`
	ContentHash   string    `json:"content_hash,omitempty"`
	PrevHash      string    `json:"prev_hash,omitempty"`
	SizeBytes     int64     `json:"size_bytes,omitempty"`
	DeltaBytes    int64     `json:"delta_bytes,omitempty"`
	Mode          uint32    `json:"mode,omitempty"`
	Target        string    `json:"target,omitempty"`
	Source        string    `json:"source,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	User          string    `json:"user,omitempty"`
	CheckpointIDs []string  `json:"checkpoint_ids,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	MetadataOnly  bool      `json:"metadata_only,omitempty"`
	Sequence      int64     `json:"sequence,omitempty"`
}

type FileHistoryRequest struct {
	Path        string `json:"path"`
	NewestFirst bool   `json:"-"`
	Limit       int    `json:"limit,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
}

type FileHistoryLineage struct {
	FileID      string        `json:"file_id"`
	State       string        `json:"state"`
	CurrentPath string        `json:"current_path"`
	Versions    []FileVersion `json:"versions"`
}

type FileHistoryResponse struct {
	WorkspaceID string               `json:"workspace_id"`
	Path        string               `json:"path"`
	Order       string               `json:"order"`
	Lineages    []FileHistoryLineage `json:"lineages"`
	NextCursor  string               `json:"next_cursor,omitempty"`
}

type FileVersionContentResponse struct {
	WorkspaceID  string `json:"workspace_id"`
	FileID       string `json:"file_id"`
	VersionID    string `json:"version_id"`
	Ordinal      int64  `json:"ordinal"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Source       string `json:"source,omitempty"`
	Content      string `json:"content,omitempty"`
	Target       string `json:"target,omitempty"`
	Binary       bool   `json:"binary,omitempty"`
	Encoding     string `json:"encoding,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Language     string `json:"language,omitempty"`
	Size         int64  `json:"size"`
	CreatedAt    string `json:"created_at"`
	DataBase64   string `json:"data_base64,omitempty"`
	MetadataOnly bool   `json:"metadata_only,omitempty"`
}

type FileVersionSelector struct {
	VersionID string `json:"version_id,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Ordinal   int64  `json:"ordinal,omitempty"`
}

type FileVersionDiffOperand struct {
	Ref       string `json:"ref,omitempty"`
	VersionID string `json:"version_id,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Ordinal   int64  `json:"ordinal,omitempty"`
}

type FileVersionDiffResponse struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	From        string `json:"from"`
	To          string `json:"to"`
	Binary      bool   `json:"binary"`
	Diff        string `json:"diff,omitempty"`
}

type FileVersionRestoreResponse struct {
	WorkspaceID           string `json:"workspace_id"`
	Path                  string `json:"path"`
	Dirty                 bool   `json:"dirty"`
	FileID                string `json:"file_id,omitempty"`
	VersionID             string `json:"version_id,omitempty"`
	RestoredFromVersionID string `json:"restored_from_version_id,omitempty"`
	RestoredFromFileID    string `json:"restored_from_file_id,omitempty"`
	RestoredFromOrdinal   int64  `json:"restored_from_ordinal,omitempty"`
}

type FileVersionUndeleteResponse struct {
	WorkspaceID            string `json:"workspace_id"`
	Path                   string `json:"path"`
	Dirty                  bool   `json:"dirty"`
	FileID                 string `json:"file_id,omitempty"`
	VersionID              string `json:"version_id,omitempty"`
	UndeletedFromVersionID string `json:"undeleted_from_version_id,omitempty"`
	UndeletedFromFileID    string `json:"undeleted_from_file_id,omitempty"`
	UndeletedFromOrdinal   int64  `json:"undeleted_from_ordinal,omitempty"`
}

// Attribution describes the caller of an explicit restore. These labels are
// provenance, not authentication credentials.
type FileVersionAttribution struct {
	SessionID string
	AgentID   string
	User      string
}
type fileVersionAttributionKey struct{}

func WithFileVersionAttribution(ctx context.Context, value FileVersionAttribution) context.Context {
	return context.WithValue(ctx, fileVersionAttributionKey{}, value)
}

func compatibleFileVersion(record filehistory.Record) FileVersion {
	kind, op := record.Type, "put"
	switch {
	case record.Deleted:
		kind, op = FileVersionKindTombstone, "delete"
	case record.Operation == "rename":
		op = "rename"
	case record.Type == FileVersionKindSymlink:
		op = "symlink"
	case record.Operation == "chmod" || record.Operation == "metadata":
		op = "chmod"
	}
	return FileVersion{
		VersionID: record.ID, FileID: record.FileID, Ordinal: record.Version,
		Path: record.Path, PrevPath: record.PreviousPath, Op: op, Kind: kind,
		BlobID: record.BlobID, ContentHash: record.ContentHash, PrevHash: record.PrevHash,
		SizeBytes: record.Size, DeltaBytes: record.DeltaBytes, Mode: record.Mode, Target: record.Target,
		Source: record.Source, SessionID: record.SessionID, AgentID: record.AgentID, User: record.User,
		CheckpointIDs: record.CheckpointIDs, CreatedAt: time.UnixMilli(record.CreatedAt).UTC(), MetadataOnly: record.MetadataOnly, Sequence: record.Sequence,
	}
}
