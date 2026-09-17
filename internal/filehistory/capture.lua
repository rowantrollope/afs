-- History is deliberately independent of checkpoint bodies. COPY snapshots
-- retain the storage backend (string or Array). Immutable SHA256 blobs are
-- shared only within this workspace and reclaimed by atomic reference counts.
local function history_hash(key)
 local values=redis.call('HGETALL',key)
 local result={}
 for i=1,#values,2 do result[values[i]]=values[i+1] end
 return result
end

local function history_type(key, expected)
 local t=redis.call('TYPE',key).ok
 if t~='none' and t~=expected then error('history key has wrong Redis type: '..key) end
end

local function history_number(key)
 history_type(key,'string')
 local raw=redis.call('GET',key) or '0'
 local n=tonumber(raw)
 if not string.match(raw,'^%d+$') or not n or n<0 or n>=100000000000000 then
  error('history counter is invalid or exhausted: '..key)
 end
 return n
end

local function history_matches(policy, path)
 if policy.mode=='off' then return false end
 for _,rule in ipairs(policy.exclude or {}) do if history_glob(rule,path) then return false end end
 if policy.mode=='all' then return true end
 for _,rule in ipairs(policy.include or {}) do if history_glob(rule,path) then return true end end
 return false
end

local function history_path(base, fields, fallback)
 if not fields or not fields.name or not fields.parent then return fallback end
 local parts={fields.name}
 local id=fields.parent
 local seen={}
 while id~='1' and id~='' do
  if seen[id] or #parts>4096 then error('history encountered invalid inode ancestry') end
  seen[id]=true
  local parent=redis.call('HGET',base..'inode:'..id,'parent')
  local name=redis.call('HGET',base..'inode:'..id,'name')
  if not parent or not name then error('history encountered missing parent') end
  table.insert(parts,1,name)
  id=parent
 end
 return '/'..table.concat(parts,'/')
end

local function history_capture(prefix, operationID, origin, changes)
 history_type(prefix..'policy','string')
 local raw=redis.call('GET',prefix..'policy')
 local policy=raw and cjson.decode(raw) or {mode='off'}
 if policy.mode~='off' and policy.mode~='all' and policy.mode~='paths' then error('invalid history policy') end
 local forced=false
 for _,change in ipairs(changes) do if change.force then forced=true end end
 if policy.mode=='off' and not forced then return false end
 local base=string.sub(prefix,1,#prefix-8)
 local prepared=prefix..'prepared:'..operationID
 history_type(prepared,'hash')
 local plans,files,blobs={},{},{}
 local timestamp=redis.call('TIME')
 local now=tonumber(timestamp[1])*1000+math.floor(tonumber(timestamp[2])/1000)
 history_type(prefix..'records','hash')
 local function sha(value)
  return type(value)=='string' and #value==64 and string.match(value,'^[a-f0-9]+$')
 end
 local function contentHash(change,fields,which,body)
  if not fields or not fields.type then return '' end
  if fields.type=='symlink' then return 'symlink:'..(fields.target or '') end
  local hash=change[which..'_hash']
  if not hash then
   local revision=redis.call('HGET',prepared,change.id..':revision')
   local before=change.before or history_hash(base..'inode:'..change.id)
   if revision and revision==(before.revision or '') then
    hash=redis.call('HGET',prepared,change.id..':'..which..'_hash')
    if which=='after' then
     local expected=redis.call('HGET',prepared,change.id..':after_body')
     if expected and expected~='' and expected~=body then hash=nil end
    end
   end
   if not hash and which=='before' and fields.history_revision==fields.revision then hash=fields.history_hash end
   if not hash and which=='after' and body==base..'content:'..change.id
    and (change.operation=='metadata' or change.operation=='rename')
    and before.history_revision==before.revision and tonumber(before.size or 0)==tonumber(fields.size or 0) then hash=before.history_hash end
  end
  if not sha(hash) then error('HISTORY_PREPARATION_REQUIRED') end
  return hash
 end
 local function snapshot(change,fields,body,path,operation,deleted,lineage,suffix,previous,hash,prevHash,previousSize)
  local kind=fields.type
  local size=tonumber(fields.size or '0')
  if not size or size<0 or size>=100000000000000 or size%1~=0 then error('invalid history file size') end
  local metadataOnly=not deleted and kind=='file' and (policy.max_file_bytes or 0)>0 and size>policy.max_file_bytes
  local id=operationID..'-'..change.id..'-'..suffix
  local metadata=change.metadata or {}
  local record={id=id,file_id=lineage,path=path,operation=operation,type=kind,
   mode=tonumber(fields.mode or '0'),size=deleted and 0 or size,target=fields.target,
   deleted=deleted,created_at=now,origin=origin,content_ref=fields.content_ref or 'ext',
   content_hash=deleted and '' or hash,prev_hash=prevHash or '',delta_bytes=(deleted and 0 or size)-(previousSize or 0),
   source=metadata.source or 'mount',session_id=metadata.session_id,agent_id=metadata.agent_id,user=metadata.user,
   checkpoint_ids=metadata.checkpoint_ids,metadata_only=metadataOnly}
  if previous and previous~=path then record.previous_path=previous end
  local payload=nil
  if kind=='file' and not deleted and not metadataOnly then
   record.blob_id=hash
   if size==0 then payload='';record.content_ref='ext'
   elseif fields.content_ref~='ext' and fields.content_ref~='array' then
    payload=fields.content or ''
    if #payload~=size then error('history inline content has inconsistent size') end
    record.content_ref='ext'
   elseif not body or redis.call('EXISTS',body)==0 then error('history content is missing')
   else
    local bodyType=redis.call('TYPE',body).ok
    if fields.content_ref=='ext' and (bodyType~='string' or redis.call('STRLEN',body)~=size) then error('history content has inconsistent size or type') end
    if fields.content_ref=='array' and bodyType~='array' then error('history Array content has wrong Redis type') end
   end
  elseif metadataOnly then record.content_ref='' end
  local file=files[lineage]
  local prior=file.last
  local same=prior and not prior.deleted and not deleted and prior.path==path and prior.type==kind
   and prior.mode==record.mode and prior.size==record.size and prior.content_hash==record.content_hash
   and (prior.metadata_only or false)==metadataOnly
  if same and not change.force then return end
  file.last=record
  change._record=record
  table.insert(plans,{record=record,body=body,payload=payload,bytes=record.blob_id and size or 0})
 end
 for _,change in ipairs(changes) do
  local inodeKey=base..'inode:'..change.id
  history_type(inodeKey,'hash')
  local before=change.before
  if before==nil then before=history_hash(inodeKey) end
  local after=change.after
  local kind=(after and after.type) or (before and before.type)
  if kind=='file' or kind=='symlink' then
   local oldPath=change.path
   if before and before.type then
    if change.explicit_path then oldPath=before.path or oldPath else oldPath=history_path(base,before,oldPath) end
   end
   local newPath=oldPath
   if after then
    if change.explicit_path then newPath=after.path or change.path else newPath=history_path(base,after,after.path or change.path) end
   end
   if change.force or history_matches(policy,oldPath) or history_matches(policy,newPath) then
    local lineage=change.file_id or (before and before.history_id) or (operationID..'-'..change.id)
    if lineage=='' then lineage=operationID..'-'..change.id end
    local fileKey=prefix..'file:'..lineage
    history_type(fileKey,'hash')
    local nextRaw=redis.call('HGET',fileKey,'next') or '0'
    local next=tonumber(nextRaw)
    if not string.match(nextRaw,'^%d+$') or not next or next>=99999999999998 then error('history version counter is invalid or exhausted') end
    local head=redis.call('HGET',fileKey,'head')
    local last=head and redis.call('HGET',prefix..'records',head)
    if head and not last then error('history head record is missing') end
    if not files[lineage] then files[lineage]={next=next,key=fileKey,last=last and cjson.decode(last)} end
    local beforeHash=contentHash(change,before,'before',change.before_body or (base..'content:'..change.id))
    local afterHash=contentHash(change,after,'after',change.body or (base..'content:'..change.id))
    local initialNoop=next==0 and not change.force and before and before.type and after
     and before.type==after.type and oldPath==newPath and beforeHash==afterHash
     and tonumber(before.mode or 0)==tonumber(after.mode or 0) and tonumber(before.size or 0)==tonumber(after.size or 0)
    if not initialNoop then
    if before and before.type and (not before.history_id or before.history_revision~=(before.revision or '') or next==0) then
     snapshot(change,before,change.before_body or (base..'content:'..change.id),oldPath,'baseline',false,lineage,'b',nil,beforeHash,'',0)
    end
    if after then
     local operation=change.operation or 'write'
     if operation=='write' and before and before.type==after.type and beforeHash==afterHash and tonumber(before.mode or 0)~=tonumber(after.mode or 0) then operation='metadata' end
     snapshot(change,after,change.body or (base..'content:'..change.id),newPath,operation,false,lineage,'a',oldPath,afterHash,beforeHash,before and tonumber(before.size or 0) or 0)
    else
     snapshot(change,before,nil,oldPath,change.operation or 'delete',true,lineage,'a',nil,'',beforeHash,tonumber(before.size or 0))
    end
    change._lineage=lineage;change._revision=after and after.revision or nil;change._hash=afterHash
    end
   end
  end
 end
 local function bind()
  for _,change in ipairs(changes) do
   if change._lineage and change.after then
    redis.call('HSET',base..'inode:'..change.id,'history_id',change._lineage,'history_revision',change._revision or '', 'history_hash',change._hash or '')
    redis.call('HSET',prefix..'file:'..change._lineage,'inode_id',change.id)
   elseif change._lineage then redis.call('HDEL',prefix..'file:'..change._lineage,'inode_id') end
  end
 end
 if #plans==0 then bind();return false end
 history_type(prefix..'retention','zset');history_type(prefix..'blobrefs','hash');history_type(prefix..'blobmeta','hash')
 local sequence=history_number(prefix..'sequence')
 local bytes=history_number(prefix..'bytes')
 local physical=history_number(prefix..'physical_bytes')
 local existingPhysical=physical
 local added=0
 local function indexTypes(record)
  history_type(prefix..'versions:'..record.file_id,'zset')
  history_type(prefix..'file_timeline:'..record.file_id,'zset')
  history_type(prefix..'path:'..redis.sha1hex(record.path),'zset')
  history_type(prefix..'timeline:'..redis.sha1hex(record.path),'zset')
  if record.previous_path and record.previous_path~=record.path then
   history_type(prefix..'path:'..redis.sha1hex(record.previous_path),'zset')
   history_type(prefix..'timeline:'..redis.sha1hex(record.previous_path),'zset')
  end
 end
 local function blobState(record)
  local id=record.blob_id
  if not id or id=='' then return nil end
  if not sha(id) then error('invalid history blob ID') end
  if blobs[id] then
   if blobs[id].size~=record.size then error('inconsistent history blob size') end
   return blobs[id]
  end
  local countRaw=redis.call('HGET',prefix..'blobrefs',id)
  local metadata=redis.call('HGET',prefix..'blobmeta',id)
  local exists=redis.call('EXISTS',prefix..'blob:'..id)==1
  local state={id=id,count=0,size=record.size,ref=record.content_ref,new=true}
  if countRaw or metadata or exists then
   local count=tonumber(countRaw)
   if not count or count<=0 or count>=100000000000000 or not string.match(countRaw,'^%d+$') or not metadata or not exists then error('inconsistent history blob ownership') end
   local meta=cjson.decode(metadata)
   if meta.size~=record.size then error('inconsistent history blob size') end
   if meta.ref~='ext' and meta.ref~='array' then error('invalid history blob backend') end
   local typ=redis.call('TYPE',prefix..'blob:'..id).ok
   if (meta.ref=='ext' and (typ~='string' or redis.call('STRLEN',prefix..'blob:'..id)~=meta.size)) or (meta.ref=='array' and typ~='array') then error('inconsistent history blob backend') end
   state.count=count;state.ref=meta.ref;state.new=false
  end
  blobs[id]=state;return state
 end
 for _,plan in ipairs(plans) do
  local record=plan.record
  local file=files[record.file_id]
  file.next=file.next+1;file.head=record.id
  if not record.deleted and not record.metadata_only then file.recoverable=record.id end
  record.version=file.next;sequence=sequence+1;record.sequence=sequence;plan.sequence=sequence
  added=added+plan.bytes
  indexTypes(record)
  if redis.call('HEXISTS',prefix..'records',record.id)==1 or redis.call('EXISTS',prefix..'body:'..record.id)==1 then error('history operation identity already exists') end
  local blob=blobState(record)
  if blob then
   blob.count=blob.count+1;record.content_ref=blob.ref
   if blob.count>=100000000000000 then error('history blob reference counter exhausted') end
   if blob.new and not blob.plan then blob.plan=plan;physical=physical+record.size end
  end
  plan.json=cjson.encode(record)
 end
 if sequence>=100000000000000 or bytes+added>=100000000000000 or physical>=100000000000000 then error('history counter exhausted') end
 local evictions={}
 if (policy.max_bytes or 0)>0 and bytes+added>policy.max_bytes then
  local candidates=redis.call('ZRANGE',prefix..'retention',0,127)
  for _,id in ipairs(candidates) do
   local rawRecord=redis.call('HGET',prefix..'records',id)
   if rawRecord then
    local record=cjson.decode(rawRecord)
    if type(record.file_id)~='string' or record.id~=id then error('invalid history record') end
    local fileKey=prefix..'file:'..record.file_id
    history_type(fileKey,'hash')
    local planned=files[record.file_id]
    local head=(planned and planned.head) or redis.call('HGET',fileKey,'head')
    local recoverable=(planned and planned.recoverable) or redis.call('HGET',fileKey,'recoverable')
    if id~=head and id~=recoverable and record.type=='file' and not record.deleted and not record.metadata_only then
     local size=tonumber(record.size)
     if not size or size<0 or size>bytes then error('invalid history byte accounting') end
     indexTypes(record)
     local blob=blobState(record)
     if blob then blob.count=blob.count-1;if blob.count<0 then error('invalid history blob ownership') end end
     table.insert(evictions,record);bytes=bytes-size
     if bytes+added<=policy.max_bytes then break end
    end
   end
  end
  if bytes+added>policy.max_bytes then error('history byte limit exceeded; raise max_bytes or prune eligible old versions') end
 end
 local releasedPhysical=0
 for _,blob in pairs(blobs) do if blob.count==0 and not blob.new then releasedPhysical=releasedPhysical+blob.size end end
 if releasedPhysical>existingPhysical then error('invalid history physical byte accounting') end
 -- Only fully validated plans can allocate snapshots. Temporary copies expire
 -- if an allocation command fails before reference publication.
 for _,blob in pairs(blobs) do
  if blob.new and blob.plan then
   local plan=blob.plan;local destination=prefix..'blob:'..blob.id
   if plan.payload~=nil then redis.call('SET',destination,plan.payload,'PX',3600000)
   else
    if redis.call('COPY',plan.body,destination)==0 then error('history content disappeared') end
    redis.call('PEXPIRE',destination,3600000)
   end
  end
 end
 for _,record in ipairs(evictions) do
  if not record.blob_id or record.blob_id=='' then redis.call('UNLINK',prefix..'body:'..record.id) end
  redis.call('HDEL',prefix..'records',record.id)
  redis.call('ZREM',prefix..'versions:'..record.file_id,record.id)
  redis.call('ZREM',prefix..'file_timeline:'..record.file_id,record.id)
  redis.call('ZREM',prefix..'timeline:'..redis.sha1hex(record.path),record.id)
  if record.previous_path then redis.call('ZREM',prefix..'timeline:'..redis.sha1hex(record.previous_path),record.id) end
  redis.call('ZREM',prefix..'retention',record.id)
 end
 for _,plan in ipairs(plans) do
  local record=plan.record
  redis.call('HSET',prefix..'records',record.id,plan.json)
  redis.call('ZADD',prefix..'versions:'..record.file_id,record.version,record.id)
  redis.call('ZADD',prefix..'file_timeline:'..record.file_id,plan.sequence,record.id)
  redis.call('ZADD',prefix..'path:'..redis.sha1hex(record.path),plan.sequence,record.file_id)
  redis.call('ZADD',prefix..'timeline:'..redis.sha1hex(record.path),plan.sequence,record.id)
  if record.previous_path and record.previous_path~=record.path then
   redis.call('ZADD',prefix..'path:'..redis.sha1hex(record.previous_path),plan.sequence,record.file_id)
   redis.call('ZADD',prefix..'timeline:'..redis.sha1hex(record.previous_path),plan.sequence,record.id)
  end
  redis.call('ZADD',prefix..'retention',plan.sequence,record.id)
  redis.call('HSET',prefix..'file:'..record.file_id,'next',string.format('%.0f',record.version),'head',record.id)
  if not record.deleted and not record.metadata_only then redis.call('HSET',prefix..'file:'..record.file_id,'recoverable',record.id) end
 end
 for _,blob in pairs(blobs) do
  if blob.count>0 then
   redis.call('HSET',prefix..'blobrefs',blob.id,string.format('%.0f',blob.count))
   redis.call('HSET',prefix..'blobmeta',blob.id,cjson.encode({size=blob.size,ref=blob.ref}))
   redis.call('PERSIST',prefix..'blob:'..blob.id)
  else
   redis.call('UNLINK',prefix..'blob:'..blob.id)
   redis.call('HDEL',prefix..'blobrefs',blob.id);redis.call('HDEL',prefix..'blobmeta',blob.id)
   physical=physical-blob.size
  end
 end
 redis.call('SET',prefix..'sequence',string.format('%.0f',sequence))
 redis.call('SET',prefix..'bytes',string.format('%.0f',bytes+added))
 redis.call('SET',prefix..'physical_bytes',string.format('%.0f',physical))
 bind()
 return true
end
