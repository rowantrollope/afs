package filehistory

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/rediscontent"
)

type Record struct {
	ID            string   `json:"id"`
	FileID        string   `json:"file_id"`
	Version       int64    `json:"version"`
	Path          string   `json:"path"`
	PreviousPath  string   `json:"previous_path,omitempty"`
	Operation     string   `json:"operation"`
	Type          string   `json:"type"`
	Mode          uint32   `json:"mode"`
	Size          int64    `json:"size"`
	Target        string   `json:"target,omitempty"`
	Deleted       bool     `json:"deleted"`
	CreatedAt     int64    `json:"created_at"`
	Origin        string   `json:"origin"`
	ContentRef    string   `json:"content_ref"`
	ContentHash   string   `json:"content_hash,omitempty"`
	PrevHash      string   `json:"prev_hash,omitempty"`
	BlobID        string   `json:"blob_id,omitempty"`
	DeltaBytes    int64    `json:"delta_bytes,omitempty"`
	Source        string   `json:"source,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	User          string   `json:"user,omitempty"`
	CheckpointIDs []string `json:"checkpoint_ids,omitempty"`
	MetadataOnly  bool     `json:"metadata_only,omitempty"`
	Sequence      int64    `json:"sequence,omitempty"`
}

func BodyKey(id string, record Record) string {
	if record.BlobID != "" {
		return Prefix(id) + "blob:" + record.BlobID
	}
	return Prefix(id) + "body:" + record.ID
}

type Page struct {
	Versions   []Record `json:"versions"`
	FileID     string   `json:"file_id"`
	NextBefore int64    `json:"next_before,omitempty"`
}

type Lineage struct {
	FileID   string `json:"file_id"`
	Sequence int64  `json:"sequence"`
}

type LineagePage struct {
	Files      []Lineage `json:"files"`
	NextBefore int64     `json:"next_before,omitempty"`
}

func pathKey(prefix, name string) string {
	sum := sha1.Sum([]byte(name))
	return prefix + "path:" + hex.EncodeToString(sum[:])
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

// Resolve through current dirents as directory renames do not rewrite every
// descendant's historical path index. The loop never follows symlinks.
const liveResolutionLua = `
local function liveFile(prefix, path)
  local fs = string.sub(prefix, 1, -9)
  local inode = '1'
  for part in string.gmatch(path, '[^/]+') do
    inode = redis.call('HGET', fs .. 'dirents:' .. inode, part)
    if not inode then return nil end
  end
  return redis.call('HGET', fs .. 'inode:' .. inode, 'history_id')
end
`

var lineagesScript = redis.NewScript(liveResolutionLua + `
local prefix, before, count = ARGV[1], ARGV[2], tonumber(ARGV[3])
local max = '+inf'
if before ~= '0' then max = '(' .. before end
local values = redis.call('ZREVRANGEBYSCORE', KEYS[1], max, '-inf', 'WITHSCORES', 'LIMIT', 0, count + 1)
local file = liveFile(prefix, ARGV[4])
if file and not redis.call('ZSCORE', KEYS[1], file) then
  local head = redis.call('HGET', prefix .. 'file:' .. file, 'head')
  local sequence = head and redis.call('ZSCORE', prefix .. 'retention', head)
  if sequence and (before == '0' or tonumber(sequence) < tonumber(before)) then
    local offset = #values + 1
    for i = 1, #values, 2 do
      if tonumber(sequence) > tonumber(values[i+1]) then offset = i; break end
    end
    table.insert(values, offset, file)
    table.insert(values, offset+1, sequence)
  end
end
return values
`)

// Lineages lists distinct file incarnations ever captured at this path, newest
// first. Before is an exclusive workspace sequence cursor. Renames retain the
// same identity; deleting and recreating a path creates another identity.
func Lineages(ctx context.Context, rdb *redis.Client, id, name string, limit int, before int64) (LineagePage, error) {
	page := LineagePage{Files: []Lineage{}}
	name, err := NormalizePath(name)
	if err != nil {
		return page, err
	}
	if before < 0 {
		return page, fmt.Errorf("history cursor must be nonnegative")
	}
	limit = pageLimit(limit)
	items, err := lineagesScript.Run(ctx, rdb, []string{pathKey(Prefix(id), name)}, Prefix(id), before, limit, name).Slice()
	if err != nil {
		return page, err
	}
	for i := 0; i < len(items); i += 2 {
		if i == limit*2 {
			page.NextBefore = page.Files[len(page.Files)-1].Sequence
			break
		}
		sequence, err := strconv.ParseInt(fmt.Sprint(items[i+1]), 10, 64)
		if err != nil {
			return page, fmt.Errorf("invalid history sequence: %w", err)
		}
		page.Files = append(page.Files, Lineage{FileID: fmt.Sprint(items[i]), Sequence: sequence})
	}
	return page, nil
}

var listScript = redis.NewScript(liveResolutionLua + `
local prefix, file, before, count = ARGV[1], ARGV[2], ARGV[3], tonumber(ARGV[4])
local live = liveFile(prefix, ARGV[5])
if file == '' then
  if live then file = live else
    local files = redis.call('ZREVRANGE', KEYS[1], 0, 0)
    if #files == 0 then return {} end
    file = files[1]
  end
elseif file ~= live and not redis.call('ZSCORE', KEYS[1], file) then return {} end
local max = '+inf'
if before ~= '0' then max = '(' .. before end
local ids = redis.call('ZREVRANGEBYSCORE', prefix .. 'versions:' .. file, max, '-inf', 'LIMIT', 0, count + 1)
local out = {file}
for _, id in ipairs(ids) do
  local record = redis.call('HGET', KEYS[2], id)
  if record then table.insert(out, record) end
end
return out
`)

// List returns one file lineage, including its previous names. Without fileID
// it selects the newest lineage associated with name. Before is an exclusive
// per-file version cursor. Query and record reads form one Redis snapshot.
func List(ctx context.Context, rdb *redis.Client, id, name string, limit int, before int64, fileID string) (Page, error) {
	page := Page{Versions: []Record{}}
	name, err := NormalizePath(name)
	if err != nil {
		return page, err
	}
	if before < 0 {
		return page, fmt.Errorf("history cursor must be nonnegative")
	}
	limit = pageLimit(limit)
	prefix := Prefix(id)
	values, err := listScript.Run(ctx, rdb, []string{pathKey(prefix, name), prefix + "records"}, prefix, fileID, before, limit, name).Slice()
	if err != nil {
		return page, err
	}
	if len(values) == 0 {
		return page, nil
	}
	page.FileID = fmt.Sprint(values[0])
	for i, value := range values[1:] {
		if i == limit {
			page.NextBefore = page.Versions[len(page.Versions)-1].Version
			break
		}
		var record Record
		if err := json.Unmarshal([]byte(fmt.Sprint(value)), &record); err != nil {
			return page, fmt.Errorf("decode history record: %w", err)
		}
		page.Versions = append(page.Versions, record)
	}
	return page, nil
}

var pinScript = redis.NewScript(liveResolutionLua + `
local prefix, file, selector = ARGV[1], ARGV[2], ARGV[3]
local live = liveFile(prefix, ARGV[5])
if file == '' then
  if live then file = live else
    local files = redis.call('ZREVRANGE', KEYS[1], 0, 0)
    if #files == 0 then return false end
    file = files[1]
  end
elseif file ~= live and not redis.call('ZSCORE', KEYS[1], file) then return false end
local id = selector
if selector == '' or selector == 'latest' then
  id = redis.call('HGET', prefix .. 'file:' .. file, 'recoverable')
elseif string.match(selector, '^%d+$') then
  local ids = redis.call('ZRANGEBYSCORE', prefix .. 'versions:' .. file, selector, selector, 'LIMIT', 0, 1)
  id = ids[1]
end
if not id then return false end
local raw = redis.call('HGET', KEYS[2], id)
if not raw then return false end
local record = cjson.decode(raw)
if record.file_id ~= file then return false end
if record.type == 'file' and not record.deleted and not record.metadata_only then
  local body=prefix .. 'body:' .. id
  if record.blob_id and record.blob_id~='' then body=prefix .. 'blob:' .. record.blob_id end
  if redis.call('COPY', body, KEYS[3], 'REPLACE') == 0 then
    return redis.error_reply('HISTORY_BODY_MISSING')
  end
  redis.call('PEXPIRE', KEYS[3], ARGV[4])
end
return raw
`)

// Get retrieves a numeric version, immutable record ID, or latest recoverable
// version. It atomically pins private content before reading, so concurrent
// retention cannot invalidate recovery. Tombstones and symlinks have no body.
func Get(ctx context.Context, rdb *redis.Client, id, name, selector, fileID string) (Record, []byte, error) {
	var record Record
	name, err := NormalizePath(name)
	if err != nil {
		return record, nil, err
	}
	prefix := Prefix(id)
	pin := prefix + "read:" + uuid.NewString()
	raw, err := pinScript.Run(ctx, rdb, []string{pathKey(prefix, name), prefix + "records", pin}, prefix, fileID, selector, (5 * time.Minute).Milliseconds(), name).Text()
	if errors.Is(err, redis.Nil) {
		return record, nil, os.ErrNotExist
	}
	if err != nil {
		return record, nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = rdb.Unlink(cleanup, pin).Err()
	}()
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return record, nil, fmt.Errorf("decode history record: %w", err)
	}
	if record.Deleted || record.MetadataOnly || record.Type != "file" {
		return record, nil, nil
	}
	if record.Size < 0 {
		return record, nil, fmt.Errorf("invalid history content size")
	}
	data, err := rediscontent.Load(ctx, rdb, pin, record.ContentRef, record.Size)
	if err != nil {
		return record, nil, err
	}
	exists, err := rdb.Exists(ctx, pin).Result()
	if err != nil {
		return record, nil, err
	}
	if exists == 0 || int64(len(data)) != record.Size {
		return record, nil, fmt.Errorf("history content pin expired or has incorrect size")
	}
	if record.ContentHash != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != record.ContentHash {
			return record, nil, fmt.Errorf("history content failed SHA256 verification")
		}
	}
	return record, data, nil
}

func RecordByID(ctx context.Context, rdb *redis.Client, id, versionID string) (Record, error) {
	var record Record
	raw, err := rdb.HGet(ctx, Prefix(id)+"records", versionID).Bytes()
	if errors.Is(err, redis.Nil) {
		return record, os.ErrNotExist
	}
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(raw, &record)
	return record, err
}

func RecordAtOrdinal(ctx context.Context, rdb *redis.Client, id, fileID string, ordinal int64) (Record, error) {
	if ordinal <= 0 {
		return Record{}, os.ErrNotExist
	}
	n := strconv.FormatInt(ordinal, 10)
	ids, err := rdb.ZRangeByScore(ctx, Prefix(id)+"versions:"+fileID, &redis.ZRangeBy{Min: n, Max: n, Count: 1}).Result()
	if err != nil {
		return Record{}, err
	}
	if len(ids) == 0 {
		return Record{}, os.ErrNotExist
	}
	return RecordByID(ctx, rdb, id, ids[0])
}

func LineageHead(ctx context.Context, rdb *redis.Client, id, fileID string) (Record, error) {
	head, err := rdb.HGet(ctx, Prefix(id)+"file:"+fileID, "head").Result()
	if errors.Is(err, redis.Nil) {
		return Record{}, os.ErrNotExist
	}
	if err != nil {
		return Record{}, err
	}
	return RecordByID(ctx, rdb, id, head)
}

var currentPathScript = redis.NewScript(`
local inode=redis.call('HGET',KEYS[1],'inode_id')
if not inode then return false end
if redis.call('HGET',ARGV[1]..'inode:'..inode,'history_id')~=ARGV[2] then return false end
local parts,seen={},{}
while inode~='1' do
 if seen[inode] or #parts>4096 then return redis.error_reply('invalid inode ancestry') end
 seen[inode]=true
 local fields=redis.call('HMGET',ARGV[1]..'inode:'..inode,'parent','name')
 if not fields[1] or not fields[2] then return false end
 table.insert(parts,1,fields[2]);inode=fields[1]
end
return '/'..table.concat(parts,'/')
`)

func CurrentPath(ctx context.Context, rdb *redis.Client, id, fileID string) (string, error) {
	name, err := currentPathScript.Run(ctx, rdb, []string{Prefix(id) + "file:" + fileID}, "afs:{"+id+"}:", fileID).Text()
	if errors.Is(err, redis.Nil) {
		return "", os.ErrNotExist
	}
	return name, err
}

type PathPageResult struct {
	Versions   []Record `json:"versions"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type pathCursor struct {
	FileID   string `json:"file_id"`
	Ordinal  int64  `json:"ordinal"`
	Sequence int64  `json:"sequence,omitempty"`
}

var pathPageScript = redis.NewScript(liveResolutionLua + `
local prefix,path,limit,cursor,direction=ARGV[1],ARGV[2],tonumber(ARGV[3]),ARGV[4],ARGV[5]
local function candidates(key)
 if direction=='desc' then
  local max=cursor=='0' and '+inf' or '('..cursor
  return redis.call('ZREVRANGEBYSCORE',key,max,'-inf','WITHSCORES','LIMIT',0,limit+1)
 else
  local min=cursor=='0' and '-inf' or '('..cursor
  return redis.call('ZRANGEBYSCORE',key,min,'+inf','WITHSCORES','LIMIT',0,limit+1)
 end
end
local values=candidates(KEYS[1])
local file=liveFile(prefix,path)
if file then
 local live=candidates(prefix..'file_timeline:'..file)
 for _,v in ipairs(live) do table.insert(values,v) end
end
local items,seen={},{}
for i=1,#values,2 do
 if not seen[values[i]] then
  seen[values[i]]=true
  local raw=redis.call('HGET',KEYS[2],values[i])
  if raw then table.insert(items,{raw=raw,seq=tonumber(values[i+1])}) end
 end
end
table.sort(items,function(a,b) if direction=='desc' then return a.seq>b.seq else return a.seq<b.seq end end)
local out={}
for i=1,math.min(#items,limit+1) do table.insert(out,items[i].raw) end
return out
`)

func PathPage(ctx context.Context, rdb *redis.Client, id, name string, limit int, cursor string, newestFirst bool) (PathPageResult, error) {
	page := PathPageResult{Versions: []Record{}}
	name, err := NormalizePath(name)
	if err != nil {
		return page, err
	}
	var before pathCursor
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			decoded, err = base64.URLEncoding.DecodeString(cursor)
		}
		if err != nil {
			return page, fmt.Errorf("invalid history cursor")
		}
		if err = json.Unmarshal(decoded, &before); err != nil || before.FileID == "" || before.Ordinal <= 0 || before.Sequence < 0 {
			return page, fmt.Errorf("invalid history cursor")
		}
		if before.Sequence == 0 {
			record, err := RecordAtOrdinal(ctx, rdb, id, before.FileID, before.Ordinal)
			if err != nil {
				return page, err
			}
			before.Sequence = record.Sequence
			if before.Sequence == 0 {
				score, err := rdb.ZScore(ctx, Prefix(id)+"retention", record.ID).Result()
				if err != nil {
					return page, err
				}
				before.Sequence = int64(score)
			}
		}
	}
	limit = pageLimit(limit)
	direction := "asc"
	if newestFirst {
		direction = "desc"
	}
	prefix := Prefix(id)
	items, err := pathPageScript.Run(ctx, rdb, []string{prefix + "timeline:" + pathDigest(name), prefix + "records"}, prefix, name, limit, before.Sequence, direction).Slice()
	if err != nil {
		return page, err
	}
	for i, item := range items {
		if i == limit {
			last := page.Versions[len(page.Versions)-1]
			raw, _ := json.Marshal(pathCursor{FileID: last.FileID, Ordinal: last.Version, Sequence: last.Sequence})
			page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		var record Record
		if err := json.Unmarshal([]byte(fmt.Sprint(item)), &record); err != nil {
			return page, err
		}
		page.Versions = append(page.Versions, record)
	}
	return page, nil
}

func pathDigest(name string) string { sum := sha1.Sum([]byte(name)); return hex.EncodeToString(sum[:]) }
