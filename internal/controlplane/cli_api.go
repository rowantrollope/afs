package controlplane

// This transport exposes a fixed set of management operations backed by the
// same service as direct Redis mode. Mounted file I/O remains direct to Redis.
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

type ConnectionInfo struct {
	RedisURL string `json:"redis_url"`
}

// FileHistoryPolicyPatch changes only supplied fields, including explicit zero
// limits and empty lists. Applying the patch inside UpdatePolicy preserves CAS.
type FileHistoryPolicyPatch struct {
	Mode         *string   `json:"mode,omitempty"`
	Include      *[]string `json:"include,omitempty"`
	Exclude      *[]string `json:"exclude,omitempty"`
	MaxVersions  *int      `json:"max_versions,omitempty"`
	MaxAgeDays   *int      `json:"max_age_days,omitempty"`
	MaxBytes     *int64    `json:"max_bytes,omitempty"`
	MaxFileBytes *int64    `json:"max_file_bytes,omitempty"`
}

func (s *Service) GetFileHistoryPolicy(ctx context.Context, workspace string) (filehistory.Policy, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return filehistory.Policy{}, err
	}
	return filehistory.GetPolicy(ctx, s.store.rdb, id)
}
func (s *Service) UpdateFileHistoryPolicy(ctx context.Context, workspace string, patch FileHistoryPolicyPatch) (filehistory.Policy, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return filehistory.Policy{}, err
	}
	result, err := filehistory.UpdatePolicy(ctx, s.store.rdb, id, func(p filehistory.Policy) (filehistory.Policy, error) {
		if patch.Mode != nil {
			p.Mode = *patch.Mode
		}
		if patch.Include != nil {
			p.Include = *patch.Include
		}
		if patch.Exclude != nil {
			p.Exclude = *patch.Exclude
		}
		if patch.MaxVersions != nil {
			p.MaxVersions = *patch.MaxVersions
		}
		if patch.MaxAgeDays != nil {
			p.MaxAgeDays = *patch.MaxAgeDays
		}
		if patch.MaxBytes != nil {
			p.MaxBytes = *patch.MaxBytes
		}
		if patch.MaxFileBytes != nil {
			p.MaxFileBytes = *patch.MaxFileBytes
		}
		return p, nil
	})
	if err != nil {
		return filehistory.Policy{}, err
	}
	meta, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return filehistory.Policy{}, err
	}
	return result, s.recordLifecycle(ctx, meta, "workspace", "versioning", nil)
}
func (s *Service) PruneFileHistory(ctx context.Context, workspace string, limit int) (filehistory.PruneResult, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return filehistory.PruneResult{}, err
	}
	return filehistory.PrunePage(ctx, s.store.rdb, id, limit)
}
func (s *Service) GetFileHistoryExport(ctx context.Context, workspace, path, selector, fileID string) (filehistory.Record, []byte, error) {
	id, err := s.historyWorkspaceID(ctx, workspace)
	if err != nil {
		return filehistory.Record{}, nil, err
	}
	return filehistory.Get(ctx, s.store.rdb, id, path, selector, fileID)
}

type cliRequest struct {
	Operation         string                 `json:"operation"`
	Workspace         string                 `json:"workspace,omitempty"`
	Name              string                 `json:"name,omitempty"`
	Description       string                 `json:"description,omitempty"`
	Ref               string                 `json:"ref,omitempty"`
	Path              string                 `json:"path,omitempty"`
	FileID            string                 `json:"file_id,omitempty"`
	Ordinal           int64                  `json:"ordinal,omitempty"`
	Limit             int                    `json:"limit,omitempty"`
	History           FileHistoryRequest     `json:"history,omitempty"`
	Selector          FileVersionSelector    `json:"selector,omitempty"`
	From              FileVersionDiffOperand `json:"from,omitempty"`
	To                FileVersionDiffOperand `json:"to,omitempty"`
	CheckpointOptions CheckpointOptions      `json:"checkpoint_options,omitempty"`
	PolicyPatch       FileHistoryPolicyPatch `json:"policy_patch,omitempty"`
}

type cliCheckpoint struct {
	Meta     SavepointMeta `json:"meta"`
	Manifest Manifest      `json:"manifest"`
}
type cliExport struct {
	Record filehistory.Record `json:"record"`
	Data   []byte             `json:"data"`
}

func (h *serverHandler) connectionRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serverMethod(w, "GET")
		return
	}
	if h.options.RedisURL == "" {
		serverJSON(w, http.StatusNotFound, map[string]string{"error": "Redis connection bootstrap is not configured"})
		return
	}
	serverJSON(w, http.StatusOK, ConnectionInfo{RedisURL: h.options.RedisURL})
}
func (h *serverHandler) cliRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serverMethod(w, "POST")
		return
	}
	var request cliRequest
	if err := serverDecode(w, r, &request); err != nil {
		h.cliError(w, err)
		return
	}
	result, err := h.dispatchCLI(r.Context(), request)
	if err != nil {
		h.cliError(w, err)
		return
	}
	serverJSON(w, http.StatusOK, result)
}
func (h *serverHandler) dispatchCLI(ctx context.Context, r cliRequest) (any, error) {
	s := h.service
	switch r.Operation {
	case "workspace.list":
		return s.ListWorkspaces(ctx)
	case "workspace.get":
		return s.GetWorkspace(ctx, r.Workspace)
	case "workspace.create":
		return s.CreateWorkspace(ctx, r.Name)
	case "workspace.update":
		return s.UpdateWorkspaceSettings(ctx, r.Workspace, r.Name, r.Description)
	case "workspace.delete":
		return nil, s.DeleteWorkspace(ctx, r.Workspace)
	case "workspace.fork":
		return nil, s.ForkWorkspace(ctx, r.Workspace, r.Name, r.Ref)
	case "workspace.generation":
		return s.WorkspaceGeneration(ctx, r.Workspace)
	case "checkpoint.list":
		return s.ListCheckpoints(ctx, r.Workspace)
	case "checkpoint.get":
		meta, manifest, err := s.GetCheckpoint(ctx, r.Workspace, r.Ref)
		return cliCheckpoint{Meta: meta, Manifest: manifest}, err
	case "checkpoint.save":
		r.CheckpointOptions.Source = CheckpointSourceCLI
		return s.SaveCheckpointFromLiveWithOptions(ctx, r.Workspace, r.Name, r.CheckpointOptions)
	case "checkpoint.restore":
		return s.RestoreCheckpoint(ctx, r.Workspace, r.Ref)
	case "checkpoint.delete":
		return nil, s.DeleteCheckpoint(ctx, r.Workspace, r.Ref)
	case "history.list":
		return s.GetFileHistoryPage(ctx, r.Workspace, r.History)
	case "history.content":
		return s.GetFileContent(ctx, r.Workspace, r.Ref, r.Path)
	case "history.version":
		return s.GetFileVersionContent(ctx, r.Workspace, r.Ref)
	case "history.ordinal":
		return s.GetFileVersionContentAtOrdinal(ctx, r.Workspace, r.FileID, r.Ordinal)
	case "history.diff":
		return s.DiffFileVersions(ctx, r.Workspace, r.Path, r.From, r.To)
	case "history.restore":
		return s.RestoreFileVersion(ctx, r.Workspace, r.Path, r.Selector)
	case "history.undelete":
		return s.UndeleteFileVersion(ctx, r.Workspace, r.Path, r.Selector)
	case "history.policy":
		return s.GetFileHistoryPolicy(ctx, r.Workspace)
	case "history.policy.update":
		return s.UpdateFileHistoryPolicy(ctx, r.Workspace, r.PolicyPatch)
	case "history.prune":
		return s.PruneFileHistory(ctx, r.Workspace, r.Limit)
	case "history.export":
		record, data, err := s.GetFileHistoryExport(ctx, r.Workspace, r.Path, r.Ref, r.FileID)
		return cliExport{Record: record, Data: data}, err
	default:
		return nil, fmt.Errorf("unknown CLI operation")
	}
}
func (h *serverHandler) cliError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "invalid_request"
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, redis.Nil):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrImportInProgress):
		status, code = http.StatusConflict, "import_in_progress"
	case errors.Is(err, afsclient.ErrWorkspaceChanged):
		status, code = http.StatusConflict, "workspace_changed"
	case errors.Is(err, afsclient.ErrWriteConflict):
		status, code = http.StatusConflict, "write_conflict"
	case errors.Is(err, ErrWorkspaceConflict), errors.Is(err, redis.TxFailedErr):
		status, code = http.StatusConflict, "conflict"
	}
	message := err.Error()
	for _, secret := range []string{h.options.AuthToken, h.options.RedisURL} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if u, e := url.Parse(h.options.RedisURL); e == nil && u.User != nil {
		if secret, _ := u.User.Password(); secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	serverJSON(w, status, map[string]string{"error": message, "code": code})
}
