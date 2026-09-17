package client

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func (c *nativeClient) prepareHistory(ctx context.Context, operationID string, requests []filehistory.PrepareRequest) error {
	err := filehistory.Prepare(ctx, c.rdb, c.key, operationID, requests)
	if errors.Is(err, filehistory.ErrPreparationConflict) {
		return ErrWriteConflict
	}
	return err
}

func (c *nativeClient) runPreparedMutation(ctx context.Context, script *redis.Script, alwaysEval bool, operationID string, requests []filehistory.PrepareRequest, keys []string, args ...interface{}) (int, error) {
	prepared := false
	defer func() {
		if prepared {
			filehistory.DiscardPreparation(c.rdb, c.key, operationID)
		}
	}()
	for attempt := 0; attempt < 3; attempt++ {
		code, err := c.runMutationScript(ctx, script, alwaysEval, keys, args...)
		if err == nil || !strings.Contains(err.Error(), "HISTORY_PREPARATION_REQUIRED") {
			return code, err
		}
		if err := c.prepareHistory(ctx, operationID, requests); err != nil {
			return 0, err
		}
		prepared = true
	}
	return 0, errors.New("versioning policy changed repeatedly during publication; retry")
}

// Redis scripts are atomic but do not roll back after a command error. Check
// counters before history or content changes; HINCRBY must not fail afterwards.
const historyPublicationLua = `
local function publication_counter(key, field, delta)
 history_type(key,'hash')
 local raw=redis.call('HGET',key,field) or '0'
 local n=tonumber(raw)
 if not string.match(raw,'^%-?%d+$') or not n or math.abs(n)+math.abs(delta)>=100000000000000
  or string.format('%.0f',n)~=raw or (n==0 and raw~='0') then
  error('workspace accounting counter is invalid or exhausted')
 end
end
local function publication_prefix(inodeKey)
 return string.match(inodeKey,'^(.*):inode:')..':history:'
end
local function publication_origin(payload)
 if payload=='' then return '' end
 return cjson.decode(payload).origin or ''
end
local function publication_payload(payload,prefix,changes,tracked)
 if payload=='' or not changes[1] then return payload end
 local value=cjson.decode(payload)
 local change=changes[1]
 local before=change.before
 if before==nil then before=history_hash(string.sub(prefix,1,#prefix-8)..'inode:'..change.id) end
 local after=change.after
 local fields=after or before
 if not fields or not fields.type then return payload end
 local metadata=change.metadata or {}
 local oldPath=change.path
 if before and before.type then oldPath=change.explicit_path and (before.path or oldPath) or history_path(string.sub(prefix,1,#prefix-8),before,oldPath) end
 local path=after and (change.explicit_path and (after.path or change.path) or history_path(string.sub(prefix,1,#prefix-8),after,change.path)) or oldPath
 local op=change.operation or 'write'
 if not after then op='delete'
 elseif op=='metadata' then op='chmod'
 elseif op~='rename' then op=fields.type=='symlink' and 'symlink' or (fields.type=='dir' and 'mkdir' or 'put') end
 local event={op=op,path=path,kind=after and fields.type or 'tombstone',size_bytes=after and tonumber(fields.size or 0) or 0,
  delta_bytes=(after and tonumber(fields.size or 0) or 0)-(before and tonumber(before.size or 0) or 0),
  mode=tonumber(fields.mode or 0),source=metadata.source or 'mount',session_id=metadata.session_id,
  agent_id=metadata.agent_id,user=metadata.user,origin=value.origin}
 if oldPath~=path then event.prev_path=oldPath end
 if tracked and change._record then
  local record=change._record
  event.file_id=record.file_id;event.version_id=record.id;event.content_hash=record.content_hash;event.prev_hash=record.prev_hash
  if record.operation=='metadata' then event.op='chmod' end
  if record.checkpoint_ids and #record.checkpoint_ids>0 then event.checkpoint_id=record.checkpoint_ids[1] end
 end
 value.change=event
 return cjson.encode(value)
end
`

// Creation and rename used to queue multiple Redis commands in EXEC. A Lua
// publication keeps a history validation failure from allowing later queued
// live mutations to execute (EXEC does not roll back command errors).
var createMetadataScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
local after=cjson.decode(ARGV[3])
local linked=redis.call('HGET',KEYS[2],after.name)
if linked==ARGV[1] and redis.call('HGET',KEYS[1],'revision')==after.revision then return 2 end
if linked then return -1 end
if redis.call('HGET',KEYS[3],'type')~='dir' then return -1 end
history_type(KEYS[1],'hash')
history_type(KEYS[5],'stream')
local counter=after.type=='dir' and 'directories' or 'symlinks'
publication_counter(KEYS[4],counter,1)
local changes={
 {id=ARGV[1],path=ARGV[2],after=after,operation='create'}
}
local prefix=publication_prefix(KEYS[1])
local tracked=history_capture(prefix,after.revision,publication_origin(ARGV[4]),changes)
ARGV[4]=publication_payload(ARGV[4],prefix,changes,tracked)
for field,value in pairs(after) do redis.call('HSET',KEYS[1],field,value) end
redis.call('HSET',KEYS[2],after.name,ARGV[1])
redis.call('HSET',KEYS[3],'mtime_ms',after.ctime_ms,'ctime_ms',after.ctime_ms)
redis.call('HINCRBY',KEYS[4],counter,1)
redis.call('SET',KEYS[7],'1')
if ARGV[4]~='' then
 redis.call('XADD',KEYS[5],'MAXLEN','~',10000,'*','payload',ARGV[4])
 redis.call('PUBLISH',KEYS[6],ARGV[4])
end
return tracked and 3 or 1
`)

var renameMetadataScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
local linked=redis.call('HGET',KEYS[3],ARGV[3])
local revision=redis.call('HGET',KEYS[1],'revision') or ''
if linked==ARGV[1] and revision==ARGV[6] then return 2 end
if redis.call('HGET',KEYS[2],ARGV[2])~=ARGV[1] or revision~=ARGV[5] then return -1 end
if (linked or '')~=ARGV[9] then return -1 end
if redis.call('HGET',KEYS[4],'type')~='dir' or redis.call('HGET',KEYS[5],'type')~='dir' then return -1 end
history_type(KEYS[7],'stream')
local prefix=publication_prefix(KEYS[1])
local base=string.sub(prefix,1,#prefix-8)
local source=history_hash(KEYS[1])
source.parent=ARGV[4]; source.name=ARGV[3]; source.ctime_ms=ARGV[7]
source.path=ARGV[11]; source.path_ancestors=ARGV[13]; source.revision=ARGV[6]
local changes={{id=ARGV[1],path=ARGV[12],after=source,operation='rename'}}
local replaced=nil
local counter=nil
if linked then
 replaced=history_hash(base..'inode:'..linked)
 if (replaced.revision or '')~=ARGV[10] then return -1 end
 if not replaced.type then return -1 end
 counter=replaced.type=='dir' and 'directories' or (replaced.type=='file' and 'files' or 'symlinks')
 publication_counter(KEYS[6],counter,-1)
 if replaced.type=='file' then publication_counter(KEYS[6],'total_data_bytes',-tonumber(replaced.size or '0')) end
 table.insert(changes,{id=linked,path=ARGV[11],after=false,operation='replace'})
end
local tracked=history_capture(prefix,ARGV[6],publication_origin(ARGV[8]),changes)
ARGV[8]=publication_payload(ARGV[8],prefix,changes,tracked)
redis.call('HDEL',KEYS[2],ARGV[2])
redis.call('HSET',KEYS[3],ARGV[3],ARGV[1])
redis.call('HSET',KEYS[1],'parent',source.parent,'name',source.name,'ctime_ms',source.ctime_ms,
 'path',source.path,'path_ancestors',source.path_ancestors,'revision',source.revision)
redis.call('HSET',KEYS[4],'ctime_ms',ARGV[7],'mtime_ms',ARGV[7])
redis.call('HSET',KEYS[5],'ctime_ms',ARGV[7],'mtime_ms',ARGV[7])
if replaced then
 redis.call('DEL',base..'inode:'..linked,base..'content:'..linked,base..'dirents:'..linked)
 redis.call('HINCRBY',KEYS[6],counter,-1)
 local size=tonumber(replaced.size or '0')
 if replaced.type=='file' and size>0 then redis.call('HINCRBY',KEYS[6],'total_data_bytes',-size) end
end
redis.call('SET',KEYS[9],'1')
if ARGV[8]~='' then
 redis.call('XADD',KEYS[7],'MAXLEN','~',10000,'*','payload',ARGV[8])
 redis.call('PUBLISH',KEYS[8],ARGV[8])
end
return tracked and 3 or 1
`)

// Retention is bounded and outside publication. A failed cleanup cannot turn a
// committed write into a failed write; an explicit versioning --prune can retry.
func (c *nativeClient) pruneHistory(ctx context.Context, result int) {
	if result != 3 {
		return
	}
	if _, err := filehistory.Prune(ctx, c.rdb, c.key, 64); err != nil {
		log.Printf("file history retention: %v", err)
	}
}
