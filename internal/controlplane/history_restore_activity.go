package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
)

func restoreActivityBeforeKey(id string) string {
	return workspaceHistoryPrefix(id) + "restore-before-checkpoint"
}

func activityEntryHash(entry ManifestEntry) (string, error) {
	if entry.Type == "symlink" {
		return "symlink:" + entry.Target, nil
	}
	if entry.Type != "file" {
		return "", nil
	}
	if entry.BlobID != "" {
		return entry.BlobID, nil
	}
	data, err := base64.StdEncoding.DecodeString(entry.Inline)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// The before checkpoint survives an interrupted materialization, allowing a
// retry to report the original full-tree change instead of its partial output.
// Activity is independent of version capture and never requires copying bodies.
func (s *Service) prepareRestoreActivity(ctx context.Context, id string, recovering bool) (Manifest, error) {
	key := restoreActivityBeforeKey(id)
	var checkpoint string
	var err error
	if recovering {
		checkpoint, err = s.store.rdb.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			// A restore started by an older binary has no durable activity
			// baseline. Preserve its recovery path without inventing prior
			// per-file contents or reporting the partial tree as its baseline.
			return Manifest{}, nil
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read interrupted restore activity baseline: %w", err)
		}
	} else {
		meta, readErr := s.store.GetWorkspaceMeta(ctx, id)
		if readErr != nil {
			return Manifest{}, readErr
		}
		checkpoint = meta.HeadSavepoint
		if err = s.store.rdb.Set(ctx, key, checkpoint, 0).Err(); err != nil {
			return Manifest{}, err
		}
	}
	return s.store.GetManifest(ctx, id, checkpoint)
}

var completeRestoreActivityScript = redis.NewScript(`
local generation=redis.call('GET',KEYS[1])
if generation==ARGV[2] then return 2 end
if generation~=ARGV[1] or redis.call('GET',KEYS[2])~=ARGV[3] then return redis.error_reply('workspace restore generation or lock changed') end
local streamtype=redis.call('TYPE',KEYS[3]).ok
if streamtype~='none' and streamtype~='stream' then return redis.error_reply('workspace change journal has wrong Redis type') end
local changes=cjson.decode(ARGV[4])
local payloads={}
for _,change in ipairs(changes) do
 local timeline=ARGV[5]..'timeline:'..redis.sha1hex(change.path)
 local typ=redis.call('TYPE',timeline).ok
 if typ~='none' and typ~='zset' then return redis.error_reply('invalid file activity history index') end
 local ids=redis.call('ZREVRANGE',timeline,0,0)
 if ids[1] then
  local raw=redis.call('HGET',ARGV[5]..'records',ids[1])
  if raw then
   local record=cjson.decode(raw)
   local current=string.sub(ids[1],1,#ARGV[6]+1)==ARGV[6]..'-'
   -- A prior attempt may have captured this state before materialization
   -- failed. Deduplication preserves that version on retry; link it only
   -- when its checkpoint and complete target state match this action.
   local retained=false
   if ARGV[8]=='1' and record.source=='checkpoint_restore' and record.path==change.path
    and record.mode==change.mode and record.size==change.size_bytes
    and (record.content_hash or '')==change.content_hash
    and ((record.deleted and change.op=='delete') or (not record.deleted and record.type==change.kind)) then
    for _,checkpoint in ipairs(record.checkpoint_ids or {}) do
     if checkpoint==change.checkpoint_id then retained=true;break end
    end
   end
   if current or retained then change.version_id=record.id;change.file_id=record.file_id end
  end
 end
 table.insert(payloads,cjson.encode({origin='afs',op='inode',paths={change.path},change=change}))
end
-- No live generation is released and no activity is published until every
-- payload/index is validated. A lost reply retries the completed generation.
for _,payload in ipairs(payloads) do redis.call('XADD',KEYS[3],'MAXLEN','~',10000,'*','payload',payload) end
local root=cjson.encode({origin='afs',op='root-replace',paths={'/'},activity=ARGV[7]=='1'})
redis.call('XADD',KEYS[3],'MAXLEN','~',10000,'*','payload',root)
redis.call('SET',KEYS[1],ARGV[2])
redis.call('DEL',KEYS[4])
redis.call('PUBLISH',KEYS[5],root)
return 1
`)

func (s *Service) completeRestoreWithActivity(ctx context.Context, id, generation, token, checkpoint string, before, after Manifest, recovering bool) error {
	paths := map[string]bool{}
	for name := range before.Entries {
		paths[name] = true
	}
	for name := range after.Entries {
		paths[name] = true
	}
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	changes := make([]map[string]any, 0)
	for _, name := range ordered {
		if before.Entries == nil {
			break
		}
		old, next := before.Entries[name], after.Entries[name]
		oldFile := old.Type == "file" || old.Type == "symlink"
		nextFile := next.Type == "file" || next.Type == "symlink"
		if !oldFile && !nextFile {
			continue
		}
		oldHash, err := activityEntryHash(old)
		if err != nil {
			return err
		}
		nextHash, err := activityEntryHash(next)
		if err != nil {
			return err
		}
		oldMode, nextMode := manifestEntryModeForWorkspaceFS(old), manifestEntryModeForWorkspaceFS(next)
		oldSize, nextSize := old.Size, next.Size
		if old.Type == "symlink" {
			oldSize = int64(len(old.Target))
		}
		if next.Type == "symlink" {
			nextSize = int64(len(next.Target))
		}
		if old.Type == next.Type && oldMode == nextMode && oldHash == nextHash && oldSize == nextSize {
			continue
		}
		op, kind, size, mode := "put", next.Type, nextSize, nextMode
		if !nextFile {
			op, kind, size, mode = "delete", "tombstone", 0, oldMode
		} else if old.Type == next.Type && oldHash == nextHash {
			op = "chmod"
		} else if next.Type == "symlink" {
			op = "symlink"
		}
		changes = append(changes, map[string]any{"op": op, "path": name, "kind": kind, "size_bytes": size, "delta_bytes": size - oldSize, "mode": mode, "content_hash": nextHash, "prev_hash": oldHash, "source": "checkpoint_restore", "checkpoint_id": checkpoint, "origin": "afs"})
	}
	payload, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	base := "afs:{" + id + "}:"
	legacy := "0"
	if before.Entries == nil {
		legacy = "1"
	}
	recovery := "0"
	if recovering {
		recovery = "1"
	}
	return completeRestoreActivityScript.Run(ctx, s.store.rdb, []string{WorkspaceGenerationKey(id), base + "import_lock", base + "changes", restoreActivityBeforeKey(id), base + "invalidate"}, "restoring:"+generation, generation, token, string(payload), workspaceHistoryPrefix(id), "restore-"+generation, legacy, recovery).Err()
}
