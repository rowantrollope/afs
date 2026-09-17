package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

type FileHistoryHTTPOptions struct {
	// DatabaseID names the one configured Redis database in scoped UI routes.
	DatabaseID string
	// AllowedOrigins permits explicitly selected local UI origins. Requests
	// without Origin (CLI clients and local tests) do not require CORS.
	AllowedOrigins []string
}

// NewFileHistoryHandler exposes the retained original UI history contracts.
// It deliberately serves one already configured Redis connection.
func NewFileHistoryHandler(service *Service, options FileHistoryHTTPOptions) http.Handler {
	if options.DatabaseID == "" {
		options.DatabaseID = "local"
	}
	return &fileHistoryHTTP{service: service, options: options}
}

type fileHistoryHTTP struct {
	service *Service
	options FileHistoryHTTPOptions
}

func (h *fileHistoryHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		allowed := false
		for _, candidate := range h.options.AllowedOrigins {
			if origin == candidate {
				allowed = true
				break
			}
		}
		parsed, _ := url.Parse(origin)
		if parsed != nil && parsed.Host == r.Host && (parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback()) && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			allowed = true
		}
		if !allowed {
			historyHTTPJSON(w, http.StatusForbidden, map[string]string{"error": "UI origin is not allowed"})
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-AFS-Session-ID, X-AFS-Agent-ID, X-AFS-User")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if strings.HasPrefix(path, "/databases/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/databases/"), "/", 2)
		if len(parts) != 2 || parts[0] != h.options.DatabaseID {
			http.NotFound(w, r)
			return
		}
		path = "/" + parts[1]
	}
	if !strings.HasPrefix(path, "/workspaces/") {
		http.NotFound(w, r)
		return
	}
	path = strings.TrimPrefix(path, "/workspaces/")
	boundary := strings.IndexAny(path, "/:")
	if boundary <= 0 {
		http.NotFound(w, r)
		return
	}
	workspace, route := path[:boundary], path[boundary:]
	if err := ValidateName("workspace", workspace); err != nil {
		historyHTTPError(w, err)
		return
	}
	ctx := WithFileVersionAttribution(r.Context(), FileVersionAttribution{
		SessionID: r.Header.Get("X-AFS-Session-ID"), AgentID: r.Header.Get("X-AFS-Agent-ID"), User: r.Header.Get("X-AFS-User"),
	})
	query := r.URL.Query()
	var result any
	var err error
	require := func(method string) bool {
		if r.Method == method {
			return true
		}
		w.Header().Set("Allow", method)
		historyHTTPJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return false
	}
	switch route {
	case "/files/history", "/changes":
		if !require(http.MethodGet) {
			return
		}
		limit := 50
		if raw := query.Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 1000 {
				historyHTTPError(w, fmt.Errorf("limit must be between 1 and 1000"))
				return
			}
		}
		direction := query.Get("direction")
		if direction != "" && direction != "asc" && direction != "desc" {
			historyHTTPError(w, fmt.Errorf("direction must be asc or desc"))
			return
		}
		if route == "/changes" {
			result, err = h.service.GetFileHistoryChanges(ctx, workspace, FileHistoryChangesRequest{
				Path: query.Get("path"), SessionID: query.Get("session_id"), NewestFirst: direction != "asc", Limit: limit,
				Cursor: query.Get("cursor"), Since: query.Get("since"), Until: query.Get("until"),
			})
		} else {
			result, err = h.service.GetFileHistoryPage(ctx, workspace, FileHistoryRequest{Path: query.Get("path"), NewestFirst: direction != "asc", Limit: limit, Cursor: query.Get("cursor")})
		}
	case "/files/version-content":
		if !require(http.MethodGet) {
			return
		}
		if version := query.Get("version_id"); version != "" {
			if query.Get("file_id") != "" || query.Get("ordinal") != "" {
				historyHTTPError(w, fmt.Errorf("choose version_id or file_id+ordinal"))
				return
			}
			result, err = h.service.GetFileVersionContent(ctx, workspace, version)
		} else {
			ordinal, parseErr := strconv.ParseInt(query.Get("ordinal"), 10, 64)
			if parseErr != nil || ordinal <= 0 {
				historyHTTPError(w, fmt.Errorf("a positive ordinal is required"))
				return
			}
			result, err = h.service.GetFileVersionContentAtOrdinal(ctx, workspace, query.Get("file_id"), ordinal)
		}
	case "/files/content":
		if !require(http.MethodGet) {
			return
		}
		result, err = h.service.GetFileContent(ctx, workspace, query.Get("view"), query.Get("path"))
	case "/files/diff":
		if !require(http.MethodPost) {
			return
		}
		var input struct {
			Path string                 `json:"path"`
			From FileVersionDiffOperand `json:"from"`
			To   FileVersionDiffOperand `json:"to"`
		}
		if err := decodeHistoryRequest(w, r, &input); err != nil {
			historyHTTPError(w, err)
			return
		}
		result, err = h.service.DiffFileVersions(ctx, workspace, input.Path, input.From, input.To)
	case ":restore-version", ":undelete":
		if !require(http.MethodPost) {
			return
		}
		var input struct {
			Path string `json:"path"`
			FileVersionSelector
		}
		if err := decodeHistoryRequest(w, r, &input); err != nil {
			historyHTTPError(w, err)
			return
		}
		if route == ":restore-version" {
			result, err = h.service.RestoreFileVersion(ctx, workspace, input.Path, input.FileVersionSelector)
		} else {
			result, err = h.service.UndeleteFileVersion(ctx, workspace, input.Path, input.FileVersionSelector)
		}
	case "/versioning":
		switch r.Method {
		case http.MethodGet:
			result, err = h.service.GetWorkspaceVersioningPolicy(ctx, workspace)
		case http.MethodPut:
			var input WorkspaceVersioningPolicy
			if err := decodeHistoryRequest(w, r, &input); err != nil {
				historyHTTPError(w, err)
				return
			}
			result, err = h.service.UpdateWorkspaceVersioningPolicy(ctx, workspace, input)
		default:
			w.Header().Set("Allow", "GET, PUT")
			historyHTTPJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
	case "/files/version-checkpoints":
		if !require(http.MethodGet) {
			return
		}
		result, err = h.service.GetFileVersionCheckpoints(ctx, workspace, query.Get("version_id"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		historyHTTPError(w, err)
		return
	}
	historyHTTPJSON(w, http.StatusOK, result)
}

func decodeHistoryRequest(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}

func historyHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, redis.Nil) {
		status = http.StatusNotFound
	}
	if errors.Is(err, ErrWorkspaceConflict) || errors.Is(err, afsclient.ErrWriteConflict) || errors.Is(err, afsclient.ErrWorkspaceChanged) {
		status = http.StatusConflict
	}
	historyHTTPJSON(w, status, map[string]string{"error": err.Error()})
}

func historyHTTPJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
