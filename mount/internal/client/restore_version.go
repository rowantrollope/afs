package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
)

// RestoreMetadata describes the current action, separately from the historical
// author of the selected version.
type RestoreMetadata struct {
	SessionID     string
	AgentID       string
	User          string
	CheckpointIDs []string
}
type restoreMetadataKey struct{}

func WithFileVersionRestoreMetadata(ctx context.Context, metadata RestoreMetadata) context.Context {
	metadata.CheckpointIDs = append([]string(nil), metadata.CheckpointIDs...)
	return context.WithValue(ctx, restoreMetadataKey{}, metadata)
}

// Restore is a single conditional publication, including type replacement,
// mode, lineage, history, directory linkage and the change notification. It
// never removes the live path before the replacement is ready.
var restoreVersionScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
local prefix,expected,generation,operation=ARGV[1],ARGV[2],ARGV[3],ARGV[4]
local base=string.sub(prefix,1,#prefix-8)
local change=cjson.decode(ARGV[5])
local after=change.after
if generation=='' or redis.call('GET',base..'generation')~=generation then return redis.error_reply('HISTORY_RESTORE_GENERATION') end
local id=change.id
local inodeKey=base..'inode:'..id
local resultID=operation..'-'..id..'-a'
if redis.call('HGET',inodeKey,'revision')==operation then
 local result=redis.call('HGET',prefix..'records',resultID)
 if result then return result end
 return redis.error_reply('HISTORY_RESTORE_RESULT_MISSING')
end
-- Resolve the parent from the root again to reject a concurrent ancestor
-- move/deletion or symlink replacement, not merely a changed leaf revision.
local parent='1'
for name in string.gmatch(ARGV[6],'[^/]+') do
 parent=redis.call('HGET',base..'dirents:'..parent,name)
 if not parent or redis.call('HGET',base..'inode:'..parent,'type')~='dir' then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
end
if redis.call('HGET',base..'inode:'..parent,'type')~='dir' then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
if parent~=tostring(after.parent) then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
local dirents=base..'dirents:'..parent
local current=redis.call('HGET',dirents,after.name)
if ARGV[7]=='' then
 if current then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
else
 if current~=ARGV[7] or (redis.call('HGET',base..'inode:'..current,'revision') or '')~=expected then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
end
history_type(inodeKey,'hash')
history_type(base..'changes','stream')
local before=current and history_hash(base..'inode:'..current) or false
if before and before.type~='file' and before.type~='symlink' then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
if ARGV[8]=='1' then
 local head=redis.call('HGET',prefix..'file:'..change.file_id,'head')
 local raw=head and redis.call('HGET',prefix..'records',head)
 if not raw or not cjson.decode(raw).deleted then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
elseif before then
 change.file_id=before.history_id
elseif change.file_id and change.file_id~='' then
 -- Restoring to an absent path is a new incarnation; undelete explicitly
 -- opts into continuing a deleted lineage.
 change.file_id=nil
end
if before and before.type=='file' and before.content_ref~='ext' and before.content_ref~='array' then
 if (before.content or '')~=(change.before.content or '') then return redis.error_reply('HISTORY_RESTORE_CONFLICT') end
end
change.before=before
change.before_body=current and base..'content:'..current or nil
local oldSize=before and before.type=='file' and tonumber(before.size or '0') or 0
local newSize=after.type=='file' and tonumber(after.size) or 0
local fileDelta=(after.type=='file' and 1 or 0)-(before and before.type=='file' and 1 or 0)
local linkDelta=(after.type=='symlink' and 1 or 0)-(before and before.type=='symlink' and 1 or 0)
publication_counter(base..'info','files',fileDelta)
publication_counter(base..'info','symlinks',linkDelta)
publication_counter(base..'info','total_data_bytes',newSize-oldSize)
if after.type=='file' then
 local stageType=redis.call('TYPE',change.body).ok
 if newSize==0 or after.content_ref=='ext' then
  if stageType~='string' or redis.call('STRLEN',change.body)~=newSize then return redis.error_reply('HISTORY_RESTORE_STAGE_MISSING') end
 elseif after.content_ref~='array' or stageType~='array' then return redis.error_reply('HISTORY_RESTORE_STAGE_MISSING') end
end
history_capture(prefix,operation,ARGV[9],{change})
-- Capture may bind history fields before the inode is created. Preserve those
-- fields while removing obsolete live content/target fields during type change.
if current and current~=id then redis.call('DEL',base..'inode:'..current,base..'content:'..current) end
redis.call('HDEL',inodeKey,'content','target','content_ref','chunk_size','chunk_hashes')
for field,value in pairs(after) do redis.call('HSET',inodeKey,field,value) end
if after.type=='file' and newSize>0 then
 redis.call('RENAME',change.body,base..'content:'..id)
 redis.call('PERSIST',base..'content:'..id)
else
 redis.call('DEL',base..'content:'..id)
 if change.body then redis.call('DEL',change.body) end
end
redis.call('HSET',dirents,after.name,id)
redis.call('HSET',base..'inode:'..parent,'mtime_ms',after.mtime_ms,'ctime_ms',after.ctime_ms)
redis.call('HINCRBY',base..'info','files',fileDelta)
redis.call('HINCRBY',base..'info','symlinks',linkDelta)
redis.call('HINCRBY',base..'info','total_data_bytes',newSize-oldSize)
redis.call('SET',base..'root_dirty','1')
if ARGV[10]~='' then
 local payload=publication_payload(ARGV[10],prefix,{change},true)
 redis.call('XADD',base..'changes','MAXLEN','~',10000,'*','payload',payload)
 redis.call('PUBLISH',base..'invalidate',payload)
end
return redis.call('HGET',prefix..'records',resultID)
`)

// RestoreFileVersion restores selected bytes to a live path. expected must be
// the observation used for the user's action; nil requires the path to remain
// absent. The workspace generation is mandatory. An explicit restore is itself
// versioned even when automatic capture is disabled or the bytes are unchanged.
func RestoreFileVersion(ctx context.Context, rdb *redis.Client, workspaceID, path string, selected filehistory.Record, content []byte, expected *StatResult, undelete bool) (filehistory.Record, error) {
	var result filehistory.Record
	p, err := filehistory.NormalizePath(path)
	if err != nil {
		return result, err
	}
	if selected.Deleted || selected.MetadataOnly || (selected.Type != "file" && selected.Type != "symlink") {
		return result, fmt.Errorf("selected version has no recoverable content")
	}
	if selected.Type == "file" && int64(len(content)) != selected.Size {
		return result, fmt.Errorf("selected version size does not match content")
	}
	generation, _ := ctx.Value(workspaceGenerationKey{}).(string)
	if generation == "" || generation == "deleted" {
		return result, ErrWorkspaceChanged
	}
	if undelete && (expected != nil || selected.FileID == "") {
		return result, ErrWriteConflict
	}
	c := newNativeClient(rdb, workspaceID, nil).(*nativeClient)
	if err := c.checkGeneration(ctx); err != nil {
		return result, err
	}
	ctx = WithExpectedStat(ctx, expected)
	// Parent creation uses the retained guarded directory implementation. The
	// final script verifies the complete resulting ancestry before publication.
	if expected == nil {
		if err := c.ensureParents(ctx, p); err != nil {
			return result, err
		}
	}
	parentPath, parent, err := c.resolvePath(ctx, parentOf(p), true)
	if err != nil {
		if expected != nil && errors.Is(err, redis.Nil) {
			return result, ErrWriteConflict
		}
		return result, err
	}
	p = joinPath(parentPath, baseName(p))
	_, before, err := c.resolvePath(ctx, p, false)
	creating := errors.Is(err, redis.Nil)
	if err != nil && !creating {
		return result, err
	}
	if err := checkWriteCondition(ctx, before, creating); err != nil {
		return result, err
	}
	if !creating && before.Type != "file" && before.Type != "symlink" {
		return result, ErrWriteConflict
	}
	inodeID := ""
	expectedID, expectedRevision := "", ""
	beforeHash := ""
	var beforeFields any = false
	if !creating {
		inodeID, expectedID, expectedRevision = before.ID, before.ID, before.Revision
		beforeFields = c.inodeFieldsAtPath(before, p, true)
		if before.Type == "file" {
			if isExternalContentRef(before.ContentRef) {
				beforeHash, err = filehistory.HashContent(ctx, rdb, c.keys.content(before.ID), before.ContentRef, before.Size)
			} else {
				sum := sha256.Sum256([]byte(before.Content))
				beforeHash = hex.EncodeToString(sum[:])
			}
			if err != nil {
				return result, err
			}
		}
	}
	if creating || before.Type != selected.Type {
		inodeID, err = c.allocInodeID(ctx)
		if err != nil {
			return result, err
		}
	}
	operation := newOriginID()
	now := nowMs()
	after := &inodeData{ID: inodeID, Parent: parent.ID, Name: baseName(p), Type: selected.Type, Mode: selected.Mode, Size: selected.Size, Target: selected.Target, Revision: operation, CtimeMs: now, MtimeMs: now, AtimeMs: now}
	if !creating {
		after.UID, after.GID = before.UID, before.GID
	}
	afterHash := "symlink:" + selected.Target
	stage := ""
	if after.Type == "file" {
		if err := c.selectContentRef(ctx, after); err != nil {
			return result, err
		}
		after.Content = string(content)
		sum := sha256.Sum256(content)
		afterHash = hex.EncodeToString(sum[:])
		if selected.ContentHash != "" && selected.ContentHash != afterHash {
			return result, fmt.Errorf("selected version content hash mismatch")
		}
		stage, err = c.stageFullFile(ctx, after)
		defer c.discardStage(stage)
		if err != nil {
			return result, err
		}
	}
	fields := c.inodeFieldsAtPath(after, p, false)
	fields["revision"] = operation
	source, op := "version_restore", "restore"
	if undelete {
		source, op = "version_undelete", "undelete"
	}
	metadata, _ := ctx.Value(restoreMetadataKey{}).(RestoreMetadata)
	change := map[string]any{"id": inodeID, "path": p, "after": fields, "before": beforeFields, "body": stage, "before_hash": beforeHash, "after_hash": afterHash, "file_id": selected.FileID, "operation": op, "force": true,
		"metadata": map[string]any{"source": source, "session_id": metadata.SessionID, "agent_id": metadata.AgentID, "user": metadata.User, "checkpoint_ids": metadata.CheckpointIDs}}
	payload, err := json.Marshal(change)
	if err != nil {
		return result, err
	}
	raw, err := restoreVersionScript.Run(ctx, rdb, []string{c.keys.generation()}, filehistory.Prefix(workspaceID), expectedRevision, generation, operation, string(payload), parentPath, expectedID, strconv.Itoa(map[bool]int{true: 1}[undelete]), c.originID, c.invalidationPayload(InvalidateOpInode, p)).Text()
	if err != nil {
		// Resolve a lost acknowledgment using this operation's immutable result,
		// never by rereading and overwriting a newer live revision.
		recovered, readErr := rdb.HGet(ctx, filehistory.Prefix(workspaceID)+"records", operation+"-"+inodeID+"-a").Result()
		if readErr == nil {
			raw, err = recovered, nil
		} else if strings.TrimPrefix(err.Error(), "ERR ") == "HISTORY_RESTORE_CONFLICT" {
			return result, ErrWriteConflict
		} else if strings.TrimPrefix(err.Error(), "ERR ") == "HISTORY_RESTORE_GENERATION" {
			return result, ErrWorkspaceChanged
		}
	}
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, err
	}
	c.pruneHistory(ctx, 3)
	return result, nil
}
