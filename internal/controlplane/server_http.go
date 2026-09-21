package controlplane

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

// HandlerOptions configures the separate server. The engine never requires it.
type HandlerOptions struct {
	DatabaseID          string
	DatabaseName        string
	DatabaseDescription string
	DatabaseRevision    string
	AdditionalDatabase  bool
	AuthToken           string
	RedisURL            string // Effective connection, disclosed only by authenticated bootstrap.
	Version             string
	UI                  fs.FS
	AllowedOrigins      []string
}

type serverHandler struct {
	service  *Service
	options  HandlerOptions
	history  http.Handler
	registry *DatabaseHandler
}

func NewHandler(service *Service, options HandlerOptions) http.Handler {
	if options.DatabaseID == "" {
		options.DatabaseID = "local"
	}
	if options.DatabaseName == "" {
		options.DatabaseName = "Redis"
	}
	return &serverHandler{service: service, options: options, history: NewFileHistoryHandler(service, FileHistoryHTTPOptions{DatabaseID: options.DatabaseID, AllowedOrigins: options.AllowedOrigins})}
}

func (h *serverHandler) authenticated(r *http.Request) bool {
	if h.options.AuthToken == "" {
		return true
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if supplied == r.Header.Get("Authorization") {
		return false
	}
	expectedHash, suppliedHash := sha256.Sum256([]byte(h.options.AuthToken)), sha256.Sum256([]byte(supplied))
	return subtle.ConstantTimeCompare(expectedHash[:], suppliedHash[:]) == 1
}

func (h *serverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/healthz" {
		if h.options.UI == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(h.options.UI, name); err != nil {
			if strings.Contains(name, ".") {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			u := *r.URL
			u.Path = "/"
			r.URL = &u
		}
		http.FileServer(http.FS(h.options.UI)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		allowed := err == nil && parsed.Host == r.Host && (parsed.Scheme == "http" || parsed.Scheme == "https")
		for _, candidate := range h.options.AllowedOrigins {
			if origin == candidate {
				allowed = true
			}
		}
		if !allowed {
			serverJSON(w, 403, map[string]string{"error": "UI origin is not allowed"})
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	authenticated := h.authenticated(r)
	if r.URL.Path == "/v1/auth/config" {
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		mode, provider := "none", "none"
		if h.options.AuthToken != "" {
			mode, provider = "token", "token"
		}
		result := map[string]any{"mode": mode, "provider": provider, "enabled": h.options.AuthToken != "", "sign_in_required": !authenticated, "authenticated": authenticated, "product_mode": "self-hosted"}
		if authenticated {
			result["user"] = map[string]any{"subject": "operator", "name": "Operator", "is_admin": true}
		}
		serverJSON(w, 200, result)
		return
	}
	if r.URL.Path == "/v1/version" {
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		serverJSON(w, 200, map[string]string{"version": h.options.Version})
		return
	}
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		service := h.service
		release := func() {}
		if h.registry != nil {
			target, done := h.registry.acquire(h.options.DatabaseID)
			if target == nil {
				serverJSON(w, 503, map[string]bool{"ok": false})
				return
			}
			service, release = target.service, done
		}
		defer release()
		if err := service.store.rdb.Ping(ctx).Err(); err != nil {
			serverJSON(w, 503, map[string]bool{"ok": false})
			return
		}
		serverJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !authenticated {
		w.Header().Set("WWW-Authenticate", "Bearer")
		serverJSON(w, 401, map[string]string{"error": "authentication required"})
		return
	}
	ctx := WithFileVersionAttribution(r.Context(), FileVersionAttribution{User: "operator"})
	r = r.WithContext(ctx)
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if strings.HasPrefix(path, "/databases/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/databases/"), "/", 2)
		if h.registry != nil {
			if len(parts) == 1 && r.Method == http.MethodPut {
				h.registry.updateDatabaseRoute(w, r, parts[0])
				return
			}
			if target, release := h.registry.acquire(parts[0]); target != nil {
				defer release()
				target.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if parts[0] != h.options.DatabaseID {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 {
			path = "/database"
		} else {
			path = "/" + parts[1]
		}
	}
	// Root collection views aggregate databases. Everything else without an
	// explicit database targets the current default's immutable handler snapshot.
	if h.registry != nil && !(path == "/databases" || r.Method == http.MethodGet && (path == "/workspaces" || path == "/agents" || path == "/events" || path == "/activity" || path == "/changes" || path == "/monitor/stream")) {
		target, release := h.registry.acquire(h.options.DatabaseID)
		if target == nil {
			serverJSON(w, 503, map[string]string{"error": "control plane is shutting down"})
			return
		}
		defer release()
		target.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(path, "/sessions/") {
		h.sessionRoute(w, r, strings.TrimPrefix(path, "/sessions/"))
		return
	}
	switch path {
	case "/connection":
		h.connectionRoute(w, r)
		return
	case "/cli":
		h.cliRoute(w, r)
		return
	case "/cli/import":
		h.cliImportRoute(w, r)
		return
	case "/databases", "/database":
		if path == "/databases" && h.registry != nil {
			h.registry.databasesRoute(w, r)
			return
		}
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		record, err := h.database(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		if path == "/database" {
			serverJSON(w, 200, record)
		} else {
			serverJSON(w, 200, map[string]any{"items": []any{record}, "default_database_id": h.options.DatabaseID})
		}
		return
	case "/workspaces":
		switch r.Method {
		case http.MethodGet:
			if h.registry != nil {
				h.registry.workspacesRoute(w, r)
				return
			}
			metas, err := h.service.ListWorkspaces(ctx)
			if err != nil {
				serverError(w, err)
				return
			}
			items := make([]any, 0, len(metas))
			for _, meta := range metas {
				item, err := h.workspace(ctx, meta, false)
				if err != nil {
					serverError(w, err)
					return
				}
				items = append(items, item)
			}
			serverJSON(w, 200, map[string]any{"items": items})
		case http.MethodPost:
			var input struct {
				Name         string          `json:"name"`
				Description  string          `json:"description"`
				DatabaseID   string          `json:"database_id"`
				DatabaseName string          `json:"database_name"`
				CloudAccount string          `json:"cloud_account"`
				Region       string          `json:"region"`
				Source       json.RawMessage `json:"source"`
				TemplateSlug string          `json:"template_slug"`
			}
			if err := serverDecode(w, r, &input); err != nil {
				serverError(w, err)
				return
			}
			if input.DatabaseID != "" && input.DatabaseID != h.options.DatabaseID {
				serverError(w, os.ErrNotExist)
				return
			}
			if input.TemplateSlug != "" {
				serverError(w, fmt.Errorf("templates are not supported"))
				return
			}
			meta, err := h.service.CreateWorkspace(ctx, input.Name)
			if err == nil && input.Description != "" {
				meta, err = h.service.UpdateWorkspaceSettings(ctx, meta.ID, input.Name, input.Description)
			}
			if err != nil {
				serverError(w, err)
				return
			}
			result, err := h.workspace(ctx, meta, true)
			serverResult(w, result, err)
		default:
			serverMethod(w, "GET, POST")
		}
		return
	case "/agents":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		if h.registry != nil {
			h.registry.agentsRoute(w, r)
			return
		}
		items, err := h.sessions(ctx, "")
		serverResult(w, map[string]any{"items": items}, err)
		return
	case "/events", "/activity", "/changes":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		h.eventRoute(w, r, "", path)
		return
	case "/monitor/stream":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		h.monitor(w, r)
		return
	}
	if !strings.HasPrefix(path, "/workspaces/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/workspaces/")
	boundary := strings.IndexAny(rest, "/:")
	workspace, suffix := rest, ""
	if boundary >= 0 {
		workspace, suffix = rest[:boundary], rest[boundary:]
	}
	meta, err := h.service.GetWorkspace(ctx, workspace)
	if err != nil {
		serverError(w, err)
		return
	}
	id := workspaceStorageID(meta)
	if strings.HasPrefix(suffix, "/sessions/") {
		h.sessionRoute(w, r, strings.TrimPrefix(suffix, "/sessions/"), id)
		return
	}
	switch suffix {
	case "":
		switch r.Method {
		case http.MethodGet:
			result, err := h.workspace(ctx, meta, true)
			serverResult(w, result, err)
		case http.MethodPut:
			var input struct {
				Name         string `json:"name"`
				Description  string `json:"description"`
				DatabaseName string `json:"database_name"`
				CloudAccount string `json:"cloud_account"`
				Region       string `json:"region"`
			}
			if err := serverDecode(w, r, &input); err != nil {
				serverError(w, err)
				return
			}
			updated, err := h.service.UpdateWorkspaceSettings(ctx, id, input.Name, input.Description)
			if err != nil {
				serverError(w, err)
				return
			}
			result, err := h.workspace(ctx, updated, true)
			serverResult(w, result, err)
		case http.MethodDelete:
			if err := h.service.DeleteWorkspace(ctx, id); err != nil {
				serverError(w, err)
				return
			}
			serverJSON(w, 200, map[string]bool{"deleted": true})
		default:
			serverMethod(w, "GET, PUT, DELETE")
		}
	case ":fork":
		if r.Method != http.MethodPost {
			serverMethod(w, "POST")
			return
		}
		var input struct {
			NewWorkspace string `json:"new_workspace"`
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := serverDecode(w, r, &input); err != nil {
			serverError(w, err)
			return
		}
		ref := input.CheckpointID
		if ref == "" {
			ref = meta.HeadSavepoint
		}
		err := h.service.ForkWorkspace(ctx, id, input.NewWorkspace, ref)
		serverResult(w, map[string]bool{"forked": err == nil}, err)
	case ":save-from-live":
		if r.Method != http.MethodPost {
			serverMethod(w, "POST")
			return
		}
		var input struct {
			CheckpointID   string `json:"checkpoint_id"`
			Description    string `json:"description"`
			Source         string `json:"source"`
			Author         string `json:"author"`
			Kind           string `json:"kind"`
			AllowUnchanged bool   `json:"allow_unchanged"`
		}
		if err := serverDecode(w, r, &input); err != nil {
			serverError(w, err)
			return
		}
		cp, err := h.service.SaveCheckpointFromLiveWithOptions(ctx, id, input.CheckpointID, CheckpointOptions{Description: input.Description, Source: "web", Author: "operator", AllowUnchanged: input.AllowUnchanged})
		serverResult(w, map[string]any{"saved": cp.ID != "", "checkpoint_id": cp.ID}, err)
	case ":restore":
		if r.Method != http.MethodPost {
			serverMethod(w, "POST")
			return
		}
		var input struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		if err := serverDecode(w, r, &input); err != nil {
			serverError(w, err)
			return
		}
		result, err := h.service.RestoreCheckpoint(ctx, id, input.CheckpointID)
		serverResult(w, result, err)
	case "/checkpoints":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		cps, err := h.service.ListCheckpoints(ctx, id)
		items := []any{}
		for _, cp := range cps {
			items = append(items, serverCheckpoint(cp, meta.HeadSavepoint))
		}
		serverResult(w, map[string]any{"items": items}, err)
	case "/tree":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		depth := 1
		if raw := r.URL.Query().Get("depth"); raw != "" {
			depth, err = strconv.Atoi(raw)
		}
		if err != nil || depth < 1 || depth > 16 {
			serverError(w, fmt.Errorf("depth must be between 1 and 16"))
			return
		}
		result, err := h.tree(ctx, meta, r.URL.Query().Get("view"), r.URL.Query().Get("path"), depth)
		serverResult(w, result, err)
	case "/files/content":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		result, err := h.content(ctx, meta, r.URL.Query().Get("view"), r.URL.Query().Get("path"))
		serverResult(w, result, err)
	case "/diff":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		result, err := h.service.DiffWorkspace(ctx, id, r.URL.Query().Get("base"), r.URL.Query().Get("head"))
		serverResult(w, result, err)
	case "/sessions":
		switch r.Method {
		case http.MethodGet:
			items, err := h.sessions(ctx, id)
			serverResult(w, map[string]any{"items": items}, err)
		case http.MethodPost:
			h.registerSession(w, r, meta)
		default:
			serverMethod(w, "GET, POST")
		}
	case "/events", "/activity", "/changes":
		if r.Method != http.MethodGet {
			serverMethod(w, "GET")
			return
		}
		h.eventRoute(w, r, id, suffix)
	case "/config":
		switch r.Method {
		case http.MethodGet:
			policy, err := h.service.GetWorkspaceVersioningPolicy(ctx, id)
			serverResult(w, map[string]any{"versioning": policy}, err)
		case http.MethodPut:
			var input struct {
				Versioning WorkspaceVersioningPolicy `json:"versioning"`
			}
			if err := serverDecode(w, r, &input); err != nil {
				serverError(w, err)
				return
			}
			policy, err := h.service.UpdateWorkspaceVersioningPolicy(ctx, id, input.Versioning)
			serverResult(w, map[string]any{"versioning": policy}, err)
		default:
			serverMethod(w, "GET, PUT")
		}
	default:
		if strings.HasPrefix(suffix, "/checkpoints/") {
			ref := strings.TrimPrefix(suffix, "/checkpoints/")
			switch r.Method {
			case http.MethodGet:
				cp, _, err := h.service.GetCheckpoint(ctx, id, ref)
				serverResult(w, serverCheckpoint(cp, meta.HeadSavepoint), err)
			case http.MethodDelete:
				err := h.service.DeleteCheckpoint(ctx, id, ref)
				serverResult(w, map[string]bool{"deleted": err == nil}, err)
			default:
				serverMethod(w, "GET, DELETE")
			}
			return
		}
		// The retained history handler already owns its selectors and recovery rules.
		copy := r.Clone(ctx)
		copy.Header = r.Header.Clone()
		copy.Header.Del("Origin")
		copy.Header.Del("X-AFS-Session-ID")
		copy.Header.Del("X-AFS-Agent-ID")
		copy.Header.Set("X-AFS-User", "operator")
		h.history.ServeHTTP(w, copy)
	}
}

func serverDecode(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}
func serverJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func serverResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		serverError(w, err)
		return
	}
	serverJSON(w, 200, value)
}
func serverMethod(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	serverJSON(w, 405, map[string]string{"error": "method not allowed"})
}
func serverError(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, redis.Nil) {
		status = 404
	}
	if errors.Is(err, ErrWorkspaceConflict) || errors.Is(err, redis.TxFailedErr) || errors.Is(err, afsclient.ErrWriteConflict) || errors.Is(err, afsclient.ErrWorkspaceChanged) || errors.Is(err, ErrImportInProgress) {
		status = 409
	}
	serverJSON(w, status, map[string]string{"error": err.Error()})
}
