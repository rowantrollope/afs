package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func serverView(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "head", nil
	}
	if raw == "head" || raw == "working-copy" {
		return raw, nil
	}
	if strings.HasPrefix(raw, "checkpoint:") {
		if err := ValidateName("checkpoint", strings.TrimPrefix(raw, "checkpoint:")); err != nil {
			return "", err
		}
		return raw, nil
	}
	return "", fmt.Errorf("unsupported workspace view %q", raw)
}
func serverRequireLiveRoot(ctx context.Context, s *Service, id string) error {
	n, err := s.store.rdb.Exists(ctx, "afs:{"+id+"}:inode:1").Result()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("workspace live root is missing; restore a checkpoint explicitly: %w", os.ErrNotExist)
	}
	return nil
}
func serverTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func serverCheckpoint(cp SavepointMeta, head string) map[string]any {
	return map[string]any{"id": cp.ID, "name": cp.Name, "description": cp.Description, "note": cp.Description, "author": cp.Author, "kind": cp.Kind, "source": cp.Source, "created_by": cp.CreatedBy, "session_id": cp.SessionID, "agent_id": cp.AgentID, "agent_name": cp.AgentName, "parent_checkpoint_id": cp.ParentSavepoint, "manifest_hash": cp.ManifestHash, "created_at": serverTime(cp.CreatedAt), "file_count": cp.FileCount, "folder_count": cp.DirCount, "total_bytes": cp.TotalBytes, "is_head": cp.ID == head}
}

func (h *serverHandler) workspace(ctx context.Context, meta WorkspaceMeta, detail bool) (map[string]any, error) {
	id := workspaceStorageID(meta)
	cps, err := h.service.ListCheckpoints(ctx, id)
	if err != nil {
		return nil, err
	}
	files, dirs := int64(0), int64(0)
	size := int64(0)
	liveRootErr := serverRequireLiveRoot(ctx, h.service, id)
	liveRootAvailable := liveRootErr == nil
	if liveRootErr != nil && !errors.Is(liveRootErr, os.ErrNotExist) {
		return nil, liveRootErr
	}
	state, status := "unavailable", "unavailable"
	if liveRootAvailable {
		info, err := afsclient.New(h.service.store.rdb, id).Info(ctx)
		if err != nil {
			return nil, err
		}
		if info != nil {
			files, dirs, size = info.Files, info.Directories-1, info.TotalDataBytes
			if dirs < 0 {
				dirs = 0
			}
		}
		dirty, known, err := WorkspaceRootDirtyState(ctx, h.service.store, id)
		if err != nil {
			return nil, err
		}
		if !known {
			dirty = meta.DirtyHint
		}
		state, status = "clean", "ready"
		if dirty {
			state, status = "dirty", "dirty"
		}
	} else {
		// Metadata and immutable checkpoints remain useful for explicit recovery.
		// Never infer an empty live tree or materialize one while reading it.
		for _, cp := range cps {
			if cp.ID == meta.HeadSavepoint {
				files, dirs, size = int64(cp.FileCount), int64(cp.DirCount), cp.TotalBytes
				break
			}
		}
	}
	last := serverTime(meta.CreatedAt)
	if len(cps) > 0 {
		last = serverTime(cps[0].CreatedAt)
	}
	result := map[string]any{"id": id, "name": meta.Name, "description": meta.Description, "cloud_account": "Self-managed", "database_id": h.options.DatabaseID, "database_name": h.options.DatabaseName, "database_management_type": "self-managed", "database_can_edit": false, "database_can_delete": false, "redis_key": id, "status": status, "file_count": files, "folder_count": dirs, "total_bytes": size, "checkpoint_count": len(cps), "draft_state": state, "last_checkpoint_at": last, "updated_at": serverTime(meta.UpdatedAt), "created_at": serverTime(meta.CreatedAt), "region": "", "source": "blank"}
	result["live_root_available"] = liveRootAvailable
	if !liveRootAvailable {
		result["unavailable_reason"] = "The live workspace tree is missing. Counts show the last checkpoint. Browse or restore a saved checkpoint to recover it."
	}
	if detail {
		checkpoints := make([]any, 0, len(cps))
		for _, cp := range cps {
			checkpoints = append(checkpoints, serverCheckpoint(cp, meta.HeadSavepoint))
		}
		result["head_checkpoint_id"] = meta.HeadSavepoint
		result["checkpoints"] = checkpoints
		result["activity"] = []any{}
		result["tags"] = []string{}
		result["capabilities"] = map[string]bool{"browse_head": liveRootAvailable, "browse_checkpoints": len(cps) > 0, "browse_working_copy": liveRootAvailable, "edit_working_copy": false, "create_checkpoint": liveRootAvailable, "restore_checkpoint": len(cps) > 0}
	}
	return result, nil
}
func (h *serverHandler) database(ctx context.Context) (map[string]any, error) {
	opts := h.service.store.rdb.Options()
	result := map[string]any{"id": h.options.DatabaseID, "name": h.options.DatabaseName, "description": "Configured Redis backend", "management_type": "self-managed", "purpose": "workspace", "can_edit": false, "can_delete": false, "can_create_workspaces": true, "redis_addr": opts.Addr, "redis_db": opts.DB, "redis_tls": opts.TLSConfig != nil, "is_default": true, "workspace_count": 0, "active_session_count": 0, "afs_total_bytes": 0, "afs_file_count": 0, "supports_search": false}
	if err := h.service.store.rdb.Ping(ctx).Err(); err != nil {
		result["connection_error"] = "Redis is unavailable"
		return result, nil
	}
	metas, err := h.service.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	result["workspace_count"] = len(metas)
	var bytes, files int64
	for _, meta := range metas {
		info, err := afsclient.New(h.service.store.rdb, workspaceStorageID(meta)).Info(ctx)
		if err == nil && info != nil {
			bytes += info.TotalDataBytes
			files += info.Files
		}
	}
	result["afs_total_bytes"], result["afs_file_count"] = bytes, files
	sessions, err := h.sessions(ctx, "")
	if err != nil {
		return nil, err
	}
	active := 0
	for _, session := range sessions {
		if session.State == "active" {
			active++
		}
	}
	result["active_session_count"] = active
	if stats, err := h.service.store.CollectRedisStats(ctx); err == nil {
		result["stats"] = stats
	}
	return result, nil
}
func (h *serverHandler) tree(ctx context.Context, meta WorkspaceMeta, rawView, rawPath string, depth int) (map[string]any, error) {
	view, err := serverView(rawView)
	if err != nil {
		return nil, err
	}
	name := path.Clean("/" + strings.TrimPrefix(rawPath, "/"))
	id := workspaceStorageID(meta)
	items := []map[string]any{}
	if view == "working-copy" {
		if err := serverRequireLiveRoot(ctx, h.service, id); err != nil {
			return nil, err
		}
		generation, err := h.service.WorkspaceGeneration(ctx, id)
		if err != nil {
			return nil, err
		}
		ctx = afsclient.WithWorkspaceGeneration(ctx, generation)
		client := afsclient.New(h.service.store.rdb, id)
		st, err := client.Stat(ctx, name)
		if err != nil {
			return nil, err
		}
		if st == nil {
			return nil, os.ErrNotExist
		}
		if st.Type != "dir" {
			return nil, fmt.Errorf("path is not a directory")
		}
		var walk func(string, int) error
		walk = func(parent string, remaining int) error {
			entries, err := client.LsLong(ctx, parent)
			if err != nil {
				return err
			}
			for _, e := range entries {
				p := path.Join(parent, e.Name)
				item := map[string]any{"path": p, "name": e.Name, "kind": e.Type, "size": e.Size, "modified_at": serverTime(time.UnixMilli(e.Mtime))}
				if e.Type == "symlink" {
					target, err := client.Readlink(ctx, p)
					if err != nil {
						return err
					}
					item["target"] = target
				}
				items = append(items, item)
				if len(items) > 100000 {
					return fmt.Errorf("tree is too large; browse a shallower directory")
				}
				if e.Type == "dir" && remaining > 1 {
					if err := walk(p, remaining-1); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(name, depth); err != nil {
			return nil, err
		}
	} else {
		ref := strings.TrimPrefix(view, "checkpoint:")
		if view == "head" {
			ref = meta.HeadSavepoint
		}
		_, m, err := h.service.GetCheckpoint(ctx, id, ref)
		if err != nil {
			return nil, err
		}
		root, ok := m.Entries[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		if root.Type != "dir" {
			return nil, fmt.Errorf("path is not a directory")
		}
		prefix := strings.TrimSuffix(name, "/") + "/"
		for p, e := range m.Entries {
			if p == name || !strings.HasPrefix(p, prefix) {
				continue
			}
			relative := strings.TrimPrefix(p, prefix)
			if strings.Count(relative, "/")+1 > depth {
				continue
			}
			items = append(items, map[string]any{"path": p, "name": path.Base(p), "kind": e.Type, "size": e.Size, "target": e.Target, "modified_at": serverTime(time.UnixMilli(e.MtimeMs))})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a["kind"] == "dir") != (b["kind"] == "dir") {
			return a["kind"] == "dir"
		}
		return a["path"].(string) < b["path"].(string)
	})
	return map[string]any{"workspace_id": id, "view": view, "path": name, "items": items}, nil
}
func (h *serverHandler) content(ctx context.Context, meta WorkspaceMeta, rawView, rawPath string) (map[string]any, error) {
	view, err := serverView(rawView)
	if err != nil {
		return nil, err
	}
	id := workspaceStorageID(meta)
	name := path.Clean("/" + strings.TrimPrefix(rawPath, "/"))
	ref := strings.TrimPrefix(view, "checkpoint:")
	var content FileVersionContentResponse
	revision, modified := view, ""
	if view == "working-copy" {
		if err := serverRequireLiveRoot(ctx, h.service, id); err != nil {
			return nil, err
		}
		generation, err := h.service.WorkspaceGeneration(ctx, id)
		if err != nil {
			return nil, err
		}
		ctx = afsclient.WithWorkspaceGeneration(ctx, generation)
		client := afsclient.New(h.service.store.rdb, id)
		stable := false
		for attempt := 0; attempt < 5; attempt++ {
			before, err := client.Stat(ctx, name)
			if err != nil {
				return nil, err
			}
			if before == nil {
				return nil, os.ErrNotExist
			}
			content = FileVersionContentResponse{Path: name, Kind: before.Type, Size: before.Size}
			if before.Type == "symlink" {
				content.Target, err = client.Readlink(ctx, name)
				content.Content = content.Target
			} else if before.Type == "file" {
				var body []byte
				body, err = client.Cat(ctx, name)
				if err == nil {
					fillVersionContent(&content, body)
				}
			} else {
				return nil, fmt.Errorf("path is not a file or symlink")
			}
			if err != nil {
				return nil, err
			}
			after, err := client.Stat(ctx, name)
			if err != nil {
				return nil, err
			}
			current, err := h.service.WorkspaceGeneration(ctx, id)
			if err != nil {
				return nil, err
			}
			if current != generation {
				return nil, ErrWorkspaceConflict
			}
			if after == nil || after.Inode != before.Inode || after.Revision != before.Revision {
				continue
			}
			revision = before.Revision
			modified = serverTime(time.UnixMilli(before.Mtime))
			stable = true
			break
		}
		if !stable {
			return nil, ErrWorkspaceConflict
		}
	} else {
		if view == "head" {
			ref = meta.HeadSavepoint
		}
		cp, m, err := h.service.GetCheckpoint(ctx, id, ref)
		if err != nil {
			return nil, err
		}
		content, err = h.service.GetFileContent(ctx, id, cp.ID, name)
		if err != nil {
			return nil, err
		}
		revision = cp.ManifestHash + ":" + name
		modified = serverTime(time.UnixMilli(m.Entries[name].MtimeMs))
	}
	return map[string]any{"workspace_id": id, "view": view, "path": name, "kind": content.Kind, "revision": revision, "language": content.Language, "encoding": content.Encoding, "content_type": content.ContentType, "size": content.Size, "modified_at": modified, "binary": content.Binary, "content": content.Content, "target": content.Target, "data_base64": content.DataBase64}, nil
}

func (s *Service) UpdateWorkspaceSettings(ctx context.Context, workspace, name, description string) (WorkspaceMeta, error) {
	meta, err := s.GetWorkspace(ctx, workspace)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	id := workspaceStorageID(meta)
	if name == "" {
		name = meta.Name
	}
	if err := ValidateName("workspace", name); err != nil {
		return WorkspaceMeta{}, err
	}
	generation, err := s.WorkspaceGeneration(ctx, id)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	var updated WorkspaceMeta
	for attempt := 0; attempt < 8; attempt++ {
		err = s.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
			current, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(id))
			if err != nil {
				return err
			}
			g, err := tx.Get(ctx, WorkspaceGenerationKey(id)).Result()
			if err != nil {
				return err
			}
			if g != generation {
				return ErrWorkspaceConflict
			}
			existing, err := tx.HGet(ctx, workspaceNameIndexKey(), name).Result()
			if err != nil && err != redis.Nil {
				return err
			}
			if existing != "" && existing != id {
				return fmt.Errorf("workspace %q already exists", name)
			}
			oldName := current.Name
			current.Name, current.Description, current.UpdatedAt = name, strings.TrimSpace(description), time.Now().UTC()
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				if err := setJSON(ctx, p, workspaceMetaKey(id), current); err != nil {
					return err
				}
				p.HSet(ctx, workspaceNameIndexKey(), name, id)
				if oldName != name {
					p.HDel(ctx, workspaceNameIndexKey(), oldName)
				}
				return nil
			})
			updated = current
			return err
		}, workspaceMetaKey(id), WorkspaceGenerationKey(id), workspaceNameIndexKey())
		if err == redis.TxFailedErr {
			continue
		}
		if err != nil {
			return WorkspaceMeta{}, err
		}
		return updated, s.recordLifecycle(ctx, updated, "workspace", "update", nil)
	}
	return WorkspaceMeta{}, ErrWorkspaceConflict
}
