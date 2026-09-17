package filehistory

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

var pruneScript = redis.NewScript(`
local prefix, limit = ARGV[1], tonumber(ARGV[2])
local function keytype(key, expected)
  local actual = redis.call('TYPE', key).ok
  if actual ~= 'none' and actual ~= expected then error('invalid history retention key type: ' .. key) end
end
keytype(KEYS[1], 'string')
keytype(KEYS[2], 'zset')
keytype(KEYS[3], 'string')
keytype(KEYS[4], 'string')
keytype(KEYS[5], 'hash')
keytype(prefix..'blobrefs','hash')
keytype(prefix..'blobmeta','hash')
keytype(prefix..'physical_bytes','string')
local raw = redis.call('GET', KEYS[1])
if not raw then return {0, 0, 0} end
local policy = cjson.decode(raw)
local maxVersions = tonumber(policy.max_versions or 0)
local maxAge = tonumber(policy.max_age_days or 0)
local maxBytes = tonumber(policy.max_bytes or 0)
if not maxVersions or not maxAge or not maxBytes or maxVersions < 0 or maxAge < 0 or maxBytes < 0 then error('invalid history retention limits') end
if maxVersions == 0 and maxAge == 0 and maxBytes == 0 then return {0, 0, 0} end
local clock = redis.call('TIME')
local cutoff = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000) - maxAge * 86400000
local rawBytes = redis.call('GET', KEYS[4]) or '0'
local retained = tonumber(rawBytes)
if not retained or retained < 0 or retained >= 100000000000000 or not string.match(rawBytes, '^%d+$') then error('invalid history byte counter') end
local cursor = redis.call('GET', KEYS[3]) or '0'
if not string.match(cursor, '^%d+$') then error('invalid history retention cursor') end
local candidates = redis.call('ZRANGEBYSCORE', KEYS[2], '(' .. cursor, '+inf', 'WITHSCORES', 'LIMIT', 0, limit)
local removed = 0
local plans, stale, files, blobs = {}, {}, {}, {}
local physicalRaw=redis.call('GET',prefix..'physical_bytes') or '0'
local physical=tonumber(physicalRaw)
if not physical or physical<0 or not string.match(physicalRaw,'^%d+$') then error('invalid history physical byte counter') end
-- Redis scripts do not roll back command errors. Validate and plan the entire
-- bounded batch before unlinking any body or changing byte accounting.
for i = 1, #candidates, 2 do
  local id, seq = candidates[i], candidates[i+1]
  cursor = seq
  local rawRecord = redis.call('HGET', KEYS[5], id)
  if not rawRecord then
    table.insert(stale, id)
  else
    local record = cjson.decode(rawRecord)
    if type(record.file_id) ~= 'string' or record.file_id == '' or record.id ~= id
      or type(record.created_at) ~= 'number' or type(record.version) ~= 'number'
      or type(record.deleted) ~= 'boolean' or (record.type ~= 'file' and record.type ~= 'symlink')
      or type(record.size) ~= 'number' or record.size < 0 or record.size >= 100000000000000 then
      error('invalid history retention record: ' .. id)
    end
    local file = prefix .. 'file:' .. record.file_id
    local versions = prefix .. 'versions:' .. record.file_id
    keytype(prefix..'file_timeline:'..record.file_id,'zset')
    keytype(prefix..'timeline:'..redis.sha1hex(record.path),'zset')
    if record.previous_path then keytype(prefix..'timeline:'..redis.sha1hex(record.previous_path),'zset') end
    local state = files[record.file_id]
    if not state then
      keytype(file, 'hash')
      keytype(versions, 'zset')
      state = {head=redis.call('HGET', file, 'head'), recoverable=redis.call('HGET', file, 'recoverable'), count=redis.call('ZCARD', versions)}
      if not state.head then error('history retention lineage has no protected head') end
      for _, pointer in ipairs({state.head,state.recoverable}) do
        -- Redis represents an absent HGET as false. A lineage created above
        -- the size cutoff can legitimately have no recoverable body yet.
        if pointer then
          local protected = redis.call('HGET', KEYS[5], pointer)
          if not protected or not redis.call('ZSCORE', versions, pointer) then error('history retention protected record is missing') end
          protected = cjson.decode(protected)
          if protected.file_id ~= record.file_id or protected.id ~= pointer
            or (pointer == state.recoverable and protected.deleted ~= false) then error('history retention protected record is inconsistent') end
        end
      end
      files[record.file_id] = state
    end
    if not state.recoverable and record.type=='file' and not record.deleted and not record.metadata_only then error('history retention lineage has no recoverable head') end
    local ordinal = redis.call('ZSCORE', versions, id)
    if not ordinal or tonumber(ordinal) ~= record.version then error('history retention version index is inconsistent') end
    if id ~= state.head and id ~= state.recoverable then
      local excess = maxVersions > 0 and state.count > maxVersions
      local expired = maxAge > 0 and tonumber(record.created_at) < cutoff
      local overBudget = maxBytes > 0 and retained > maxBytes
      if excess or expired or overBudget then
        table.insert(plans, {id=id, versions=versions,record=record})
        if record.type == 'file' and not record.deleted and not record.metadata_only then retained = retained - tonumber(record.size or 0) end
        if retained < 0 then error('history retention byte accounting is inconsistent') end
        if record.blob_id and record.blob_id~='' then
          local blob=blobs[record.blob_id]
          if not blob then
            local countRaw=redis.call('HGET',prefix..'blobrefs',record.blob_id)
            local metadata=redis.call('HGET',prefix..'blobmeta',record.blob_id)
            local count=tonumber(countRaw)
            if not count or count<=0 or not string.match(countRaw,'^%d+$') or not metadata or redis.call('EXISTS',prefix..'blob:'..record.blob_id)==0 then error('invalid history blob ownership') end
            local meta=cjson.decode(metadata)
            if meta.size~=record.size then error('invalid history blob size') end
            if meta.ref~='ext' and meta.ref~='array' then error('invalid history blob backend') end
            local typ=redis.call('TYPE',prefix..'blob:'..record.blob_id).ok
            if (meta.ref=='ext' and (typ~='string' or redis.call('STRLEN',prefix..'blob:'..record.blob_id)~=meta.size)) or (meta.ref=='array' and typ~='array') then error('invalid history blob backend') end
            blob={count=count,size=record.size};blobs[record.blob_id]=blob
          end
          blob.count=blob.count-1
          if blob.count<0 then error('invalid history blob ownership') end
        end
        state.count = state.count - 1
        removed = removed + 1
      end
    end
  end
end
local releasedPhysical=0
for _,blob in pairs(blobs) do if blob.count==0 then releasedPhysical=releasedPhysical+blob.size end end
if releasedPhysical>physical then error('history physical byte accounting is inconsistent') end
for _, plan in ipairs(plans) do
  local record=plan.record
  if not record.blob_id or record.blob_id=='' then redis.call('UNLINK', prefix .. 'body:' .. plan.id) end
  redis.call('HDEL', KEYS[5], plan.id)
  redis.call('ZREM', plan.versions, plan.id)
  redis.call('ZREM', KEYS[2], plan.id)
  redis.call('ZREM',prefix..'file_timeline:'..record.file_id,plan.id)
  redis.call('ZREM',prefix..'timeline:'..redis.sha1hex(record.path),plan.id)
  if record.previous_path then redis.call('ZREM',prefix..'timeline:'..redis.sha1hex(record.previous_path),plan.id) end
end
for id,blob in pairs(blobs) do
  if blob.count==0 then
    redis.call('UNLINK',prefix..'blob:'..id)
    redis.call('HDEL',prefix..'blobrefs',id);redis.call('HDEL',prefix..'blobmeta',id)
    physical=physical-blob.size
  else redis.call('HSET',prefix..'blobrefs',id,string.format('%.0f',blob.count)) end
end
for _, id in ipairs(stale) do redis.call('ZREM', KEYS[2], id) end
if #candidates < limit * 2 then cursor = '0' end
redis.call('SET', KEYS[3], cursor)
redis.call('SET', KEYS[4], string.format('%.0f',math.max(0,retained)))
redis.call('SET',prefix..'physical_bytes',string.format('%.0f',physical))
local more = 0
if cursor ~= '0' then more = 1 end
return {removed, #candidates / 2, more}
`)

type PruneResult struct {
	Removed int  `json:"removed"`
	Scanned int  `json:"scanned"`
	More    bool `json:"more"`
}

// Prune examines at most limit records and deletes eligible snapshots atomically
// with their indexes and logical byte accounting. A rotating cursor ensures
// protected heads do not starve later candidates. The newest record and newest
// recoverable snapshot of every lineage survive all retention limits.
func Prune(ctx context.Context, rdb *redis.Client, id string, limit int) (int, error) {
	result, err := PrunePage(ctx, rdb, id, limit)
	return result.Removed, err
}

// PrunePage reports whether the bounded sweep reached its end, including when
// protected heads caused fewer deletions than the requested examination limit.
func PrunePage(ctx context.Context, rdb *redis.Client, id string, limit int) (PruneResult, error) {
	var result PruneResult
	if limit <= 0 {
		limit = 128
	}
	if limit > 1000 {
		limit = 1000
	}
	prefix := Prefix(id)
	values, err := pruneScript.Run(ctx, rdb, []string{prefix + "policy", prefix + "retention", prefix + "prune_cursor", prefix + "bytes", prefix + "records"}, prefix, limit).Slice()
	if err != nil {
		return result, err
	}
	if len(values) != 3 {
		return result, fmt.Errorf("unexpected retention response")
	}
	result.Removed, err = strconv.Atoi(fmt.Sprint(values[0]))
	if err != nil {
		return result, err
	}
	result.Scanned, err = strconv.Atoi(fmt.Sprint(values[1]))
	if err != nil {
		return result, err
	}
	result.More = fmt.Sprint(values[2]) == "1"
	return result, nil
}
