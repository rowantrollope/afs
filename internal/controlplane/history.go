package controlplane

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	"github.com/rowantrollope/afs/internal/rediscontent"
)

type workspaceHistoryBinding struct {
	FileID          string
	Revision        string
	HistoryRevision string
}

type workspaceHistoryState struct {
	ID     string
	Fields map[string]string
	Body   string
}

type workspaceHistoryChange struct {
	ID           string         `json:"id"`
	Path         string         `json:"path"`
	After        any            `json:"after"`
	Body         string         `json:"body,omitempty"`
	Operation    string         `json:"operation"`
	Before       any            `json:"before"`
	BeforeBody   string         `json:"before_body,omitempty"`
	FileID       string         `json:"file_id,omitempty"`
	ExplicitPath bool           `json:"explicit_path"`
	BeforeHash   string         `json:"before_hash,omitempty"`
	AfterHash    string         `json:"after_hash,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func workspaceHistoryPrefix(id string) string { return "afs-lite:{" + id + "}:history:" }

// copyWorkspaceHistory snapshots all indexes, policy and immutable bodies in
// one Redis transaction. WATCH covers capture, pruning, policy updates and
// source deletion; a concurrent change causes a fresh bounded SCAN and retry.
func (s *Service) copyWorkspaceHistory(ctx context.Context, source, destination string) (map[string]workspaceHistoryState, error) {
	sourcePrefix, destinationPrefix := workspaceHistoryPrefix(source), workspaceHistoryPrefix(destination)
	var sourceStates map[string]workspaceHistoryState
	for attempt := 0; attempt < 8; attempt++ {
		err := s.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
			generation, err := tx.Get(ctx, WorkspaceGenerationKey(source)).Result()
			if err != nil {
				return err
			}
			if !strings.HasPrefix(generation, "g_") {
				return fmt.Errorf("source workspace is unavailable during history fork")
			}
			var keys []string
			var cursor uint64
			for {
				batch, next, err := tx.Scan(ctx, cursor, sourcePrefix+"*", 128).Result()
				if err != nil {
					return err
				}
				for _, key := range batch {
					suffix := strings.TrimPrefix(key, sourcePrefix)
					if !strings.HasPrefix(suffix, "read:") && !strings.HasPrefix(suffix, "prepare-read:") && !strings.HasPrefix(suffix, "prepared:") && suffix != "prune_cursor" && suffix != "restore-before-checkpoint" {
						keys = append(keys, key)
					}
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
			if hasHistory, err := tx.Exists(ctx, sourcePrefix+"sequence").Result(); err != nil {
				return err
			} else if hasHistory > 0 {
				sourceStates, err = s.liveHistoryStates(ctx, source)
				if err != nil {
					return err
				}
			}
			var copies []*redis.IntCmd
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				for _, key := range keys {
					copies = append(copies, pipe.Copy(ctx, key, destinationPrefix+strings.TrimPrefix(key, sourcePrefix), 0, true))
				}
				return nil
			})
			if err == nil {
				for _, copied := range copies {
					if copied.Val() != 1 {
						return fmt.Errorf("source history key disappeared during fork")
					}
				}
			}
			return err
		}, sourcePrefix+"sequence", sourcePrefix+"records", sourcePrefix+"bytes", sourcePrefix+"policy", WorkspaceGenerationKey(source), "afs-lite:{"+source+"}:changes", workspaceRootDirtyKey(source))
		if !errors.Is(err, redis.TxFailedErr) {
			return sourceStates, err
		}
	}
	return nil, fmt.Errorf("source history changed repeatedly during fork; retry")
}

// Validate copied immutable bodies before publishing the destination name.
// Array holes and absent trailing chunks represent zero-filled logical bytes,
// so only string-backed records have a physical length equal to their size.
func (s *Service) validateCopiedHistoryBodies(ctx context.Context, id string) error {
	prefix := workspaceHistoryPrefix(id)
	var cursor uint64
	for {
		values, next, err := s.store.rdb.HScan(ctx, prefix+"records", cursor, "*", 128).Result()
		if err != nil {
			return err
		}
		var records []filehistory.Record
		for i := 1; i < len(values); i += 2 {
			var record filehistory.Record
			if err := json.Unmarshal([]byte(values[i]), &record); err != nil {
				return err
			}
			if record.Type == "file" && !record.Deleted && !record.MetadataOnly {
				if record.Size < 0 || (record.ContentRef != rediscontent.RefExternal && record.ContentRef != rediscontent.RefArray) {
					return fmt.Errorf("invalid history body metadata for %s", record.ID)
				}
				records = append(records, record)
			}
		}
		pipe := s.store.rdb.Pipeline()
		types := make([]*redis.StatusCmd, len(records))
		sizes := make([]*redis.IntCmd, len(records))
		for i, record := range records {
			key := filehistory.BodyKey(id, record)
			types[i] = pipe.Type(ctx, key)
			if record.ContentRef == rediscontent.RefExternal || record.Size == 0 {
				sizes[i] = pipe.StrLen(ctx, key)
			}
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		for i, record := range records {
			expectedType := "string"
			if record.ContentRef == rediscontent.RefArray && record.Size > 0 {
				expectedType = "array"
			}
			if types[i].Val() != expectedType || (sizes[i] != nil && sizes[i].Val() != record.Size) {
				return fmt.Errorf("history body %s is missing or inconsistent", record.ID)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// liveHistoryStates reads metadata in bounded batches. Paths are walked from
// directory entries because descendants need not have refreshed path caches
// after a parent directory rename.
func (s *Service) liveHistoryStates(ctx context.Context, id string) (map[string]workspaceHistoryState, error) {
	type item struct{ id, path string }
	queue := []item{{workspaceFSRootInodeID, "/"}}
	states := map[string]workspaceHistoryState{}
	seen := map[string]bool{}
	for len(queue) > 0 {
		n := len(queue)
		if n > workspaceFSWriteBatchEntries {
			n = workspaceFSWriteBatchEntries
		}
		batch := queue[:n]
		queue = queue[n:]
		pipe := s.store.rdb.Pipeline()
		commands := make([]*redis.MapStringStringCmd, len(batch))
		for i, current := range batch {
			if seen[current.id] {
				return nil, fmt.Errorf("workspace inode ancestry contains a cycle")
			}
			seen[current.id] = true
			commands[i] = pipe.HGetAll(ctx, workspaceFSInodeKey(id, current.id))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
		for i, current := range batch {
			fields := commands[i].Val()
			fields["path"] = current.path
			if fields["type"] == "dir" {
				children, err := s.store.rdb.HGetAll(ctx, workspaceFSDirentsKey(id, current.id)).Result()
				if err != nil {
					return nil, err
				}
				for name, child := range children {
					queue = append(queue, item{child, workspaceFSNormalizePath(current.path + "/" + name)})
				}
			} else if fields["type"] == "file" || fields["type"] == "symlink" {
				states[current.path] = workspaceHistoryState{current.id, fields, workspaceFSContentKey(id, current.id)}
			}
		}
	}
	return states, nil
}

// retainedHistoryStates resolves the latest lineage still associated with its
// recorded path; historical aliases from a rename do not become live files.
func (s *Service) retainedHistoryStates(ctx context.Context, id string) (map[string]workspaceHistoryState, error) {
	prefix := workspaceHistoryPrefix(id)
	states := map[string]workspaceHistoryState{}
	scores := map[string]float64{}
	var cursor uint64
	for {
		keys, next, err := s.store.rdb.Scan(ctx, cursor, prefix+"file:*", 128).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			lineage := strings.TrimPrefix(key, prefix+"file:")
			head, err := s.store.rdb.HGet(ctx, key, "head").Result()
			if errors.Is(err, redis.Nil) {
				continue
			}
			if err != nil {
				return nil, err
			}
			raw, err := s.store.rdb.HGet(ctx, prefix+"records", head).Bytes()
			if err != nil {
				return nil, err
			}
			var record filehistory.Record
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, err
			}
			digest := sha1.Sum([]byte(record.Path))
			score, err := s.store.rdb.ZScore(ctx, prefix+"path:"+hex.EncodeToString(digest[:]), lineage).Result()
			if err != nil {
				return nil, err
			}
			if previous, ok := scores[record.Path]; ok && previous >= score {
				continue
			}
			scores[record.Path] = score
			fields := map[string]string{
				"type": record.Type, "path": record.Path, "mode": strconv.FormatUint(uint64(record.Mode), 10),
				"size": strconv.FormatInt(record.Size, 10), "target": record.Target, "content_ref": record.ContentRef,
				"history_id": lineage, "revision": "history:" + head, "history_revision": "history:" + head,
				"history_hash": record.ContentHash,
			}
			if record.Deleted {
				fields["deleted"] = "1"
			}
			if record.MetadataOnly {
				fields["metadata_only"] = "1"
			}
			states[record.Path] = workspaceHistoryState{Fields: fields, Body: filehistory.BodyKey(id, record)}
		}
		cursor = next
		if cursor == 0 {
			return states, nil
		}
	}
}

func historyStateContent(ctx context.Context, rdb *redis.Client, state workspaceHistoryState) ([]byte, error) {
	if state.Fields["content_ref"] == "" {
		return []byte(state.Fields["content"]), nil
	}
	size, err := strconv.ParseInt(state.Fields["size"], 10, 64)
	if err != nil {
		return nil, err
	}
	return rediscontent.Load(ctx, rdb, state.Body, state.Fields["content_ref"], size)
}

func historyStatesEqual(ctx context.Context, rdb *redis.Client, before, after workspaceHistoryState) (bool, error) {
	if before.Fields["deleted"] == "1" {
		return false, nil
	}
	for _, field := range []string{"type", "path", "mode", "size", "target"} {
		if before.Fields[field] != after.Fields[field] {
			return false, nil
		}
	}
	if after.Fields["type"] != "file" {
		return true, nil
	}
	if before.Fields["metadata_only"] == "1" {
		oldHash, err := historyStateHash(ctx, rdb, before)
		if err != nil {
			return false, err
		}
		newHash, err := historyStateHash(ctx, rdb, after)
		return oldHash == newHash, err
	}
	oldBody, err := historyStateContent(ctx, rdb, before)
	if err != nil {
		return false, err
	}
	newBody, err := historyStateContent(ctx, rdb, after)
	return bytes.Equal(oldBody, newBody), err
}

func historyStateHash(ctx context.Context, rdb *redis.Client, state workspaceHistoryState) (string, error) {
	if state.Fields["type"] != "file" {
		return "", nil
	}
	if digest := state.Fields["history_hash"]; digest != "" && state.Fields["history_revision"] == state.Fields["revision"] {
		return digest, nil
	}
	ref := state.Fields["content_ref"]
	if ref != "ext" && ref != "array" {
		digest := sha256.Sum256([]byte(state.Fields["content"]))
		return hex.EncodeToString(digest[:]), nil
	}
	size, err := strconv.ParseInt(state.Fields["size"], 10, 64)
	if err != nil {
		return "", err
	}
	return filehistory.HashContent(ctx, rdb, state.Body, ref, size)
}

func (s *Service) forkFileHistory(ctx context.Context, source, destination string) error {
	sourceStates, err := s.copyWorkspaceHistory(ctx, source, destination)
	if err != nil {
		return err
	}
	if err := s.validateCopiedHistoryBodies(ctx, destination); err != nil {
		return err
	}
	before, err := s.retainedHistoryStates(ctx, destination)
	if err != nil {
		return err
	}
	// Directory renames leave descendant record paths untouched. The live
	// lineage bindings are part of the same WATCH snapshot as the copied
	// histories, so use their current paths when matching checkpoint entries.
	pathsByLineage := map[string]string{}
	for path, state := range sourceStates {
		if lineage := state.Fields["history_id"]; lineage != "" {
			pathsByLineage[lineage] = path
		}
	}
	rebound := map[string]workspaceHistoryState{}
	for path, state := range before {
		if _, active := pathsByLineage[state.Fields["history_id"]]; !active {
			rebound[path] = state
		}
	}
	for path, state := range before {
		if currentPath, active := pathsByLineage[state.Fields["history_id"]]; active {
			state.Fields["path"] = currentPath
			rebound[currentPath] = state
		} else if _, exists := rebound[path]; !exists {
			rebound[path] = state
		}
	}
	before = rebound
	after, err := s.liveHistoryStates(ctx, destination)
	if err != nil {
		return err
	}
	if err := s.rebindHistoryAliases(ctx, destination, before, after); err != nil {
		return err
	}
	operationID, err := newWorkspaceGeneration()
	if err != nil {
		return err
	}
	_, err = s.captureWorkspaceHistory(ctx, destination, before, after, "fork-"+operationID, "checkpoint_fork", "", true, initialCheckpointName)
	return err
}

func (s *Service) prepareRestoreFileHistory(ctx context.Context, id string, m Manifest, operationID string, recovery bool) (map[string]workspaceHistoryBinding, error) {
	policy, err := filehistory.GetPolicy(ctx, s.store.rdb, id)
	if err != nil {
		return nil, err
	}
	if policy.Mode == filehistory.ModeOff {
		exists, err := s.store.rdb.Exists(ctx, workspaceHistoryPrefix(id)+"sequence").Result()
		if err != nil || exists == 0 {
			return nil, err
		}
	}
	var before map[string]workspaceHistoryState
	if recovery {
		before, err = s.retainedHistoryStates(ctx, id)
	} else {
		before, err = s.liveHistoryStates(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	if policy.Mode == filehistory.ModeOff {
		after := map[string]workspaceHistoryState{}
		for path, entry := range m.Entries {
			if entry.Type == "file" || entry.Type == "symlink" {
				after[path] = workspaceHistoryState{Fields: map[string]string{"path": path}}
			}
		}
		if err := s.rebindHistoryAliases(ctx, id, before, after); err != nil {
			return nil, err
		}
		bindings := map[string]workspaceHistoryBinding{}
		for path, state := range before {
			if _, ok := m.Entries[path]; ok && state.Fields["history_id"] != "" {
				bindings[path] = workspaceHistoryBinding{FileID: state.Fields["history_id"], Revision: operationID, HistoryRevision: state.Fields["history_revision"]}
			}
		}
		return bindings, nil
	}
	nodes, err := buildWorkspaceFSNodes(m)
	if err != nil {
		return nil, err
	}
	after := map[string]workspaceHistoryState{}
	var stages []string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := 0; i < len(stages); i += workspaceFSWriteBatchEntries {
			end := i + workspaceFSWriteBatchEntries
			if end > len(stages) {
				end = len(stages)
			}
			_ = s.store.rdb.Del(cleanup, stages[i:end]...).Err()
		}
	}()
	for _, node := range nodes {
		if node.Entry.Type != "file" && node.Entry.Type != "symlink" {
			continue
		}
		fields, content, _, err := workspaceFSNodeFields(ctx, s.store, id, node, SyncOptions{}, rediscontent.RefExternal)
		if err != nil {
			return nil, err
		}
		state := workspaceHistoryState{ID: "restore-" + node.ID, Fields: map[string]string{}}
		for key, value := range fields {
			state.Fields[key] = fmt.Sprint(value)
		}
		if node.Entry.Type == "file" {
			state.Body = workspaceFSContentKey(id, "history-stage-"+operationID+"-"+node.ID)
			stages = append(stages, state.Body)
			if err := s.store.rdb.Set(ctx, state.Body, content, time.Hour).Err(); err != nil {
				return nil, err
			}
		}
		after[node.Path] = state
	}
	if err := s.rebindHistoryAliases(ctx, id, before, after); err != nil {
		return nil, err
	}
	expectedGeneration := "fencing:" + strings.TrimPrefix(operationID, "restore-")
	if recovery {
		expectedGeneration = "restoring:" + strings.TrimPrefix(operationID, "restore-")
	}
	return s.captureWorkspaceHistory(ctx, id, before, after, operationID, "checkpoint_restore", expectedGeneration, false, m.Savepoint)
}

// Checkpoints predate per-file lineage metadata. Reuse a retained path alias
// when its lineage is otherwise absent from the selected tree; this restores
// an older filename as another rename of the same file.
func (s *Service) rebindHistoryAliases(ctx context.Context, id string, before, after map[string]workspaceHistoryState) error {
	retained, err := s.retainedHistoryStates(ctx, id)
	if err != nil {
		return err
	}
	byLineage := map[string]workspaceHistoryState{}
	for _, state := range retained {
		byLineage[state.Fields["history_id"]] = state
	}
	for _, state := range before {
		if lineage := state.Fields["history_id"]; lineage != "" {
			byLineage[lineage] = state
		}
	}
	for path := range after {
		if _, exists := before[path]; exists {
			continue
		}
		digest := sha1.Sum([]byte(path))
		lineages, err := s.store.rdb.ZRevRange(ctx, workspaceHistoryPrefix(id)+"path:"+hex.EncodeToString(digest[:]), 0, 0).Result()
		if err != nil {
			return err
		}
		if len(lineages) == 0 {
			continue
		}
		state, ok := byLineage[lineages[0]]
		if !ok {
			continue
		}
		oldPath := state.Fields["path"]
		if oldPath != path {
			if _, present := after[oldPath]; present {
				continue
			}
			delete(before, oldPath)
		}
		before[path] = state
		delete(byLineage, lineages[0])
	}
	return nil
}

func (s *Service) captureWorkspaceHistory(ctx context.Context, id string, before, after map[string]workspaceHistoryState, operationID, operation, generation string, skipEqual bool, checkpointIDs ...string) (map[string]workspaceHistoryBinding, error) {
	paths := make([]string, 0, len(before)+len(after))
	for path := range before {
		paths = append(paths, path)
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	changes := make([]workspaceHistoryChange, 0, len(paths))
	bindings := map[string]workspaceHistoryBinding{}
	for i, path := range paths {
		old, hasBefore := before[path]
		next, hasAfter := after[path]
		lineage := old.Fields["history_id"]
		if hasAfter {
			next.Fields["revision"] = operationID + "-" + strconv.Itoa(i)
			bindings[path] = workspaceHistoryBinding{FileID: lineage, Revision: next.Fields["revision"], HistoryRevision: old.Fields["history_revision"]}
		}
		if hasBefore && hasAfter && skipEqual {
			equal, err := historyStatesEqual(ctx, s.store.rdb, old, next)
			if err != nil {
				return nil, err
			}
			if equal {
				bindings[path] = workspaceHistoryBinding{FileID: lineage, Revision: next.Fields["revision"], HistoryRevision: next.Fields["revision"]}
				continue
			}
		}
		if !hasAfter && old.Fields["deleted"] == "1" {
			continue
		}
		inodeID := old.ID
		if inodeID == "" {
			inodeID = "history-" + operationID + "-" + strconv.Itoa(i)
		}
		if operation == "checkpoint_fork" && hasAfter {
			inodeID = next.ID
		}
		change := workspaceHistoryChange{ID: inodeID, Path: path, After: false, Before: false, Operation: operation, BeforeBody: old.Body, FileID: lineage, ExplicitPath: true}
		change.Metadata = map[string]any{"source": operation, "checkpoint_ids": checkpointIDs}
		if hasBefore && old.Fields["deleted"] != "1" {
			change.Before = old.Fields
			var err error
			change.BeforeHash, err = historyStateHash(ctx, s.store.rdb, old)
			if err != nil {
				return nil, err
			}
		}
		if hasAfter {
			change.After, change.Body = next.Fields, next.Body
			var err error
			change.AfterHash, err = historyStateHash(ctx, s.store.rdb, next)
			if err != nil {
				return nil, err
			}
		}
		changes = append(changes, change)
	}
	if len(changes) > 0 {
		payload, err := json.Marshal(changes)
		if err != nil {
			return nil, err
		}
		script := filehistory.CaptureLua + `
if ARGV[4] ~= '' and redis.call('GET', KEYS[1]) ~= ARGV[4] then return redis.error_reply('workspace generation changed during history restore') end
return history_capture(ARGV[1], ARGV[2], 'afs', cjson.decode(ARGV[3]))`
		if err := s.store.rdb.Eval(ctx, script, []string{WorkspaceGenerationKey(id)}, workspaceHistoryPrefix(id), operationID, string(payload), generation).Err(); err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
		for _, change := range changes {
			binding, ok := bindings[change.Path]
			if !ok {
				continue
			}
			values, err := s.store.rdb.HMGet(ctx, workspaceFSInodeKey(id, change.ID), "history_id", "history_revision").Result()
			if err != nil {
				return nil, err
			}
			if values[0] != nil {
				binding.FileID, binding.HistoryRevision = fmt.Sprint(values[0]), fmt.Sprint(values[1])
			}
			bindings[change.Path] = binding
		}
	}
	if operation == "checkpoint_fork" {
		pipe := s.store.rdb.Pipeline()
		for path, binding := range bindings {
			if binding.FileID != "" {
				pipe.HSet(ctx, workspaceFSInodeKey(id, after[path].ID), "history_id", binding.FileID, "revision", binding.Revision, "history_revision", binding.HistoryRevision)
				pipe.HSet(ctx, workspaceHistoryPrefix(id)+"file:"+binding.FileID, "inode_id", after[path].ID)
			}
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}
