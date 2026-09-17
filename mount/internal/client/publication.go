package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	"github.com/rowantrollope/afs/internal/rediscontent"
)

// ErrWriteConflict leaves the caller's candidate intact for the existing sync
// conflict/recovery path. A failed conditional publication is never retried
// against a newly observed remote version.
var ErrWriteConflict = errors.New("remote file changed during publication")
var ErrWorkspaceChanged = errors.New("workspace was restored or deleted; remount required")

type expectedStatKey struct{}
type expectedStat struct{ stat *StatResult }
type workspaceGenerationKey struct{}

// WithExpectedStat binds a mutation to an observation made before reading the
// remote bytes. nil expects an absent path. Copying prevents callers changing
// the condition while an asynchronous operation is running.
func WithExpectedStat(ctx context.Context, stat *StatResult) context.Context {
	if stat != nil {
		copy := *stat
		stat = &copy
	}
	return context.WithValue(ctx, expectedStatKey{}, expectedStat{stat})
}

func WithWorkspaceGeneration(ctx context.Context, generation string) context.Context {
	return context.WithValue(ctx, workspaceGenerationKey{}, generation)
}

func (c *nativeClient) checkGeneration(ctx context.Context) error {
	if checked, _ := ctx.Value(nativeRequestCheckedKey{}).(bool); checked {
		return nil
	}
	generation, ok := ctx.Value(workspaceGenerationKey{}).(string)
	if !ok {
		return nil
	}
	if lease := nativeLease(ctx); lease != "" {
		values, err := c.rdb.MGet(ctx, c.keys.generation(), lease).Result()
		if err != nil {
			return err
		}
		if values[0] != generation || generation == "deleted" {
			return ErrWorkspaceChanged
		}
		if values[1] == nil {
			return ErrNativeSessionLost
		}
		return nil
	}
	actual, err := c.rdb.Get(ctx, c.keys.generation()).Result()
	if errors.Is(err, redis.Nil) || (err == nil && (actual != generation || actual == "deleted")) {
		return ErrWorkspaceChanged
	}
	return err
}

func checkWriteCondition(ctx context.Context, inode *inodeData, creating bool) error {
	expected, ok := ctx.Value(expectedStatKey{}).(expectedStat)
	if !ok {
		return nil
	}
	if expected.stat == nil {
		if !creating {
			return ErrWriteConflict
		}
		return nil
	}
	if creating || expected.stat.Inode != inodeUint64(inode.ID) || expected.stat.Revision != inode.Revision {
		return ErrWriteConflict
	}
	return nil
}

// Only temporary keys are new: the published inode/content representation and
// chunk transfer remain unchanged. An interrupted upload expires without ever
// replacing a live content key. Redis executes publication as one command;
// operation identity makes its automatic network retries safe.
const publicationTTL = time.Hour

var publishFileScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
if ARGV[7] ~= '' and redis.call('GET', KEYS[7]) ~= ARGV[7] then return -2 end
if KEYS[11] ~= KEYS[7] and redis.call('EXISTS',KEYS[11]) == 0 then return -5 end
local current = redis.call('HGET', KEYS[1], 'revision') or ''
local linked = redis.call('HGET', KEYS[3], ARGV[2])
if current == ARGV[4] and linked == ARGV[1] then return 2 end
local streamtype=redis.call('TYPE',KEYS[9]).ok
if streamtype~='none' and streamtype~='stream' then return -4 end
if redis.call('EXISTS', KEYS[6]) == 0 then return -1 end
if ARGV[5] == '1' then
 if linked then return -1 end
else
 if linked ~= ARGV[1] or current ~= ARGV[3] then return -1 end
 if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
end
-- Every publication consumes staging, including an empty file. A missing
-- stage after a concurrent delete must never let a transport retry recreate it.
if redis.call('EXISTS', KEYS[2]) == 0 then return -3 end
if redis.call('HGET',KEYS[6],'type')~='dir' then return -1 end
local previous = tonumber(redis.call('HGET', KEYS[1], 'size') or '0')
publication_counter(KEYS[5],'files',1)
publication_counter(KEYS[5],'total_data_bytes',tonumber(ARGV[8])-previous)
local after={revision=ARGV[4]}
for i=10,#ARGV,2 do after[ARGV[i]]=ARGV[i+1] end
local changes={
 {id=ARGV[1],path=after.path,after=after,body=KEYS[2],operation=ARGV[5]=='1' and 'create' or 'write'}
}
local prefix=publication_prefix(KEYS[1])
local tracked=history_capture(prefix,ARGV[4],publication_origin(ARGV[9]),changes)
ARGV[9]=publication_payload(ARGV[9],prefix,changes,tracked)
if ARGV[8] == '0' then
 redis.call('DEL', KEYS[2], KEYS[4])
else
 redis.call('RENAME', KEYS[2], KEYS[4])
 redis.call('PERSIST', KEYS[4])
end
for i=10,#ARGV,2 do redis.call('HSET',KEYS[1],ARGV[i],ARGV[i+1]) end
redis.call('HSET',KEYS[1],'revision',ARGV[4])
redis.call('HDEL',KEYS[1],'content')
if ARGV[5] == '1' then
 redis.call('HSETNX',KEYS[3],ARGV[2],ARGV[1])
 redis.call('HINCRBY',KEYS[5],'files',1)
 redis.call('HSET',KEYS[6],'mtime_ms',ARGV[6],'ctime_ms',ARGV[6])
end
redis.call('HINCRBY',KEYS[5],'total_data_bytes',tonumber(ARGV[8])-previous)
redis.call('SET',KEYS[8],'1')
if ARGV[9] ~= '' then
 redis.call('XADD',KEYS[9],'MAXLEN','~',10000,'*','payload',ARGV[9])
 redis.call('PUBLISH',KEYS[10],ARGV[9])
end
return tracked and 3 or 1
`)

func (c *nativeClient) publishStagedFile(ctx context.Context, p string, inode *inodeData, stage string, creating bool, extra map[string]interface{}) error {
	if err := checkExpectedParent(ctx, p, inode.Parent); err != nil {
		return err
	}
	if err := checkWriteCondition(ctx, inode, creating); err != nil {
		return err
	}
	revision := newOriginID()
	generation, _ := ctx.Value(workspaceGenerationKey{}).(string)
	create := "0"
	if creating {
		create = "1"
	}
	fields := mergeFieldMaps(c.inodeFieldsAtPath(inode, p, false), extra)
	if _, ranged := ctx.Value(nativeRangeKey{}).(bool); ranged {
		// An open handle's path is only an invalidation hint. A concurrent
		// ancestor rename must not have its canonical path overwritten here.
		delete(fields, "path")
		delete(fields, "path_ancestors")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []interface{}{inode.ID, inode.Name, inode.Revision, revision, create, inode.MtimeMs, generation, inode.Size, c.invalidationPayload(InvalidateOpInode, p)}
	for _, key := range keys {
		args = append(args, key, fields[key])
	}
	request := filehistory.PrepareRequest{InodeID: inode.ID, ExpectedRevision: inode.Revision, Path: p, AfterType: "file", AfterBody: stage, AfterRef: inode.ContentRef, AfterSize: inode.Size}
	_, chunked := extra["chunk_size"]
	_, ranged := ctx.Value(nativeRangeKey{}).(bool)
	if !chunked && !ranged && int64(len(inode.Content)) == inode.Size {
		request.KnownContent = inode.Content
		request.HasKnownContent = true
	}
	result, err := c.runPreparedMutation(ctx, publishFileScript, false, revision, []filehistory.PrepareRequest{request}, []string{
		c.keys.inode(inode.ID), stage, c.keys.dirents(inode.Parent), c.keys.content(inode.ID), c.keys.info(), c.keys.inode(inode.Parent), c.keys.generation(), c.keys.rootDirty(), c.keys.changesStream(), c.keys.invalidateChannel(), c.leaseGuardKey(ctx),
	}, args...)
	if err != nil {
		// A response can be lost after Redis committed. Resolve that uncertainty
		// from the operation token, without replaying the candidate over new data.
		token, readErr := c.rdb.HGet(ctx, c.keys.inode(inode.ID), "revision").Result()
		if readErr != nil || token != revision {
			return err
		}
	} else {
		switch result {
		case -1:
			return ErrWriteConflict
		case -2:
			return ErrWorkspaceChanged
		case -3:
			return errors.New("staged file expired before publication")
		case -4:
			return errors.New("workspace change journal has wrong Redis type")
		case -5:
			return ErrNativeSessionLost
		}
	}
	inode.Revision = revision
	c.pruneHistory(ctx, result)
	if _, ranged := ctx.Value(nativeRangeKey{}).(bool); !ranged {
		c.cachePath(p, inode)
	}
	return nil
}

func (c *nativeClient) stageFullFile(ctx context.Context, inode *inodeData) (string, error) {
	stage := c.keys.content(inode.ID) + ":stage:" + newOriginID()
	pipe := c.rdb.TxPipeline()
	if inode.Size == 0 {
		// Empty Arrays need no live content key, but still need consumable staging.
		pipe.Set(ctx, stage, "", 0)
	} else {
		rediscontent.QueueWriteFull(ctx, pipe, stage, inode.ContentRef, []byte(inode.Content))
	}
	pipe.Expire(ctx, stage, publicationTTL)
	_, err := pipe.Exec(ctx)
	return stage, err
}

func (c *nativeClient) discardStage(stage string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.rdb.Del(ctx, stage).Err()
}

func (c *nativeClient) publishChunks(ctx context.Context, p string, chunks map[int][]byte, chunkSize int, newSize int64, hashes []string) error {
	if chunkSize < 0 || newSize < 0 || (chunkSize == 0 && len(chunks) != 0) {
		return errors.New("invalid chunk size or file size")
	}
	resolved, inode, err := c.resolvePath(ctx, p, true)
	creating := errors.Is(err, redis.Nil)
	if err != nil && !creating {
		return err
	}
	if creating {
		if chunkSize == 0 && newSize > 0 {
			return errors.New("missing complete chunks for new file")
		}
		p = normalizePath(p)
		if err := c.ensureParents(ctx, p); err != nil {
			return err
		}
		_, parent, err := c.resolvePath(ctx, parentOf(p), true)
		if err != nil {
			return err
		}
		id, err := c.allocInodeID(ctx)
		if err != nil {
			return err
		}
		now := nowMs()
		inode = &inodeData{ID: id, Parent: parent.ID, Name: baseName(p), Type: "file", Mode: 0o644, CtimeMs: now, MtimeMs: now, AtimeMs: now}
		resolved = p
		// A new file has no unchanged chunks to copy.
		for off := int64(0); off < newSize; off += int64(chunkSize) {
			idx := int(off / int64(chunkSize))
			data, ok := chunks[idx]
			want := int64(chunkSize)
			if newSize-off < want {
				want = newSize - off
			}
			if !ok || int64(len(data)) != want {
				return fmt.Errorf("missing complete chunk %d for new file", idx)
			}
		}
	}
	if inode.Type != "file" {
		return ErrNotFile
	}
	if err := checkWriteCondition(ctx, inode, creating); err != nil {
		return err
	}
	before, err := c.versionedSnapshotFromResolved(ctx, resolved, inode)
	if err != nil {
		return err
	}
	originalRef := inode.ContentRef
	if err := c.selectContentRef(ctx, inode); err != nil {
		return err
	}
	stage := c.keys.content(inode.ID) + ":stage:" + newOriginID()
	defer c.discardStage(stage)
	if !creating && isExternalContentRef(originalRef) && originalRef == inode.ContentRef {
		// COPY stays inside Redis, preserving delta upload bandwidth.
		pipe := c.rdb.TxPipeline()
		pipe.Copy(ctx, c.keys.content(inode.ID), stage, 0, true)
		pipe.Expire(ctx, stage, publicationTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	} else {
		if !creating {
			inode.Content, err = c.loadContentExternal(ctx, inode.ID, originalRef)
			if err != nil {
				return err
			}
		}
		pipe := c.rdb.TxPipeline()
		rediscontent.QueueWriteFull(ctx, pipe, stage, inode.ContentRef, []byte(inode.Content))
		pipe.Expire(ctx, stage, publicationTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}
	if inode.ContentRef == rediscontent.RefArray && inode.Size == 0 {
		// Seed an expiring empty Array before individual range writes. A
		// crash while creating a large Array must not leave immortal staging.
		pipe := c.rdb.TxPipeline()
		pipe.Do(ctx, "ARSET", stage, 0, "")
		pipe.Expire(ctx, stage, publicationTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}
	indices := make([]int, 0, len(chunks))
	for idx := range chunks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	if inode.ContentRef == rediscontent.RefArray {
		for _, idx := range indices {
			if idx < 0 {
				return errors.New("negative chunk index")
			}
			if err := rediscontent.WriteRange(ctx, c.rdb, stage, int64(idx)*int64(chunkSize), chunks[idx]); err != nil {
				return err
			}
		}
		if newSize < inode.Size {
			if err := rediscontent.Truncate(ctx, c.rdb, stage, inode.Size, newSize); err != nil {
				return err
			}
		}
	} else {
		pipe := c.rdb.Pipeline()
		for _, idx := range indices {
			if idx < 0 {
				return errors.New("negative chunk index")
			}
			pipe.SetRange(ctx, stage, int64(idx)*int64(chunkSize), string(chunks[idx]))
		}
		pipe.Expire(ctx, stage, publicationTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		if newSize < inode.Size {
			data := ""
			if newSize > 0 {
				data, err = c.rdb.GetRange(ctx, stage, 0, newSize-1).Result()
				if err != nil {
					return err
				}
			}
			if err := c.rdb.Set(ctx, stage, data, publicationTTL).Err(); err != nil {
				return err
			}
		}
	}
	// COPY of an empty file may have no source key; truncating an Array to
	// zero deletes its stage. Keep an empty marker for the publication to consume.
	if newSize == 0 {
		if err := c.rdb.Set(ctx, stage, "", publicationTTL).Err(); err != nil {
			return err
		}
	} else if err := c.rdb.Expire(ctx, stage, publicationTTL).Err(); err != nil {
		return err
	}
	fields := map[string]interface{}{}
	hashJSON, _ := encodeChunkHashes(hashes)
	fields["chunk_size"] = chunkSize
	fields["chunk_hashes"] = hashJSON
	inode.Size = newSize
	inode.MtimeMs = nowMs()
	inode.AtimeMs = inode.MtimeMs
	if err := c.publishStagedFile(ctx, resolved, inode, stage, creating, fields); err != nil {
		return err
	}
	c.invalidateInode(ctx, resolved)
	if creating {
		c.invalidateDirListing(ctx, parentOf(resolved))
	}
	after, err := c.versionedSnapshotFromResolved(ctx, resolved, inode)
	if err != nil {
		return err
	}
	return c.recordVersionMutation(ctx, before, after)
}

var deleteInodeScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
if ARGV[4] ~= '' and redis.call('GET',KEYS[7]) ~= ARGV[4] then return -2 end
if KEYS[11] ~= KEYS[7] and redis.call('EXISTS',KEYS[11]) == 0 then return -5 end
local streamtype=redis.call('TYPE',KEYS[9]).ok
if streamtype~='none' and streamtype~='stream' then return -4 end
local linked = redis.call('HGET',KEYS[3],ARGV[2])
if not linked and redis.call('EXISTS',KEYS[1]) == 0 then return 2 end
if linked ~= ARGV[1] or (redis.call('HGET',KEYS[1],'revision') or '') ~= ARGV[3] then return -1 end
local kind = redis.call('HGET',KEYS[1],'type')
if kind == 'dir' then
 local children=redis.call('HGETALL',KEYS[4])
 for i=1,#children,2 do
  if redis.call('EXISTS',ARGV[6]..children[i+1]) == 1 then return -3 end
 end
end
local size=tonumber(redis.call('HGET',KEYS[1],'size') or '0')
local counter='files'
if kind=='dir' then counter='directories' elseif kind=='symlink' then counter='symlinks' end
publication_counter(KEYS[5],counter,-1)
publication_counter(KEYS[5],'total_data_bytes',-size)
history_type(KEYS[6],'hash')
local changes={
 {id=ARGV[1],path=ARGV[9],after=false,operation='delete'}
}
local prefix=publication_prefix(KEYS[1])
local tracked=history_capture(prefix,ARGV[8],publication_origin(ARGV[7]),changes)
ARGV[7]=publication_payload(ARGV[7],prefix,changes,tracked)
redis.call('DEL',KEYS[1],KEYS[2],KEYS[4])
redis.call('HDEL',KEYS[3],ARGV[2])
redis.call('HSET',KEYS[6],'mtime_ms',ARGV[5],'ctime_ms',ARGV[5])
redis.call('HINCRBY',KEYS[5],counter,-1)
-- Redis 7 serializes negative zero as '-0', which HINCRBY rejects.
if kind=='file' and size>0 then redis.call('HINCRBY',KEYS[5],'total_data_bytes',-size) end
redis.call('SET',KEYS[8],'1')
if ARGV[7] ~= '' then
 redis.call('XADD',KEYS[9],'MAXLEN','~',10000,'*','payload',ARGV[7])
 redis.call('PUBLISH',KEYS[10],ARGV[7])
end
return tracked and 3 or 1
`)

func (c *nativeClient) deletePublishedInode(ctx context.Context, p string, inode *inodeData) error {
	if err := checkExpectedParent(ctx, p, inode.Parent); err != nil {
		return err
	}
	if err := checkWriteCondition(ctx, inode, false); err != nil {
		return err
	}
	generation, _ := ctx.Value(workspaceGenerationKey{}).(string)
	operationID := newOriginID()
	code, err := c.runPreparedMutation(ctx, deleteInodeScript, false, operationID, []filehistory.PrepareRequest{{InodeID: inode.ID, ExpectedRevision: inode.Revision, Path: p}}, []string{c.keys.inode(inode.ID), c.keys.content(inode.ID), c.keys.dirents(inode.Parent), c.keys.dirents(inode.ID), c.keys.info(), c.keys.inode(inode.Parent), c.keys.generation(), c.keys.rootDirty(), c.keys.changesStream(), c.keys.invalidateChannel(), c.leaseGuardKey(ctx)}, inode.ID, inode.Name, inode.Revision, generation, nowMs(), c.keys.inodePrefix(), c.invalidationPayload(InvalidateOpInode, p), operationID, p)
	if err != nil {
		return err
	}
	switch code {
	case -1:
		return ErrWriteConflict
	case -2:
		return ErrWorkspaceChanged
	case -3:
		return ErrDirNotEmpty
	case -4:
		return errors.New("workspace change journal has wrong Redis type")
	case -5:
		return ErrNativeSessionLost
	}
	c.pruneHistory(ctx, code)
	return nil
}

var updateInodeScript = redis.NewScript(filehistory.CaptureLua + historyPublicationLua + `
if ARGV[4] ~= '' and redis.call('GET',KEYS[3]) ~= ARGV[4] then return -2 end
if KEYS[6] ~= KEYS[3] and redis.call('EXISTS',KEYS[6]) == 0 then return -5 end
local streamtype=redis.call('TYPE',KEYS[4]).ok
if streamtype~='none' and streamtype~='stream' then return -4 end
local current=redis.call('HGET',KEYS[1],'revision') or ''
if current==ARGV[5] then return 2 end
if redis.call('EXISTS',KEYS[1]) == 0 then return -1 end
if ARGV[1] ~= '1' and redis.call('HGET',KEYS[2],ARGV[2]) ~= ARGV[1] then return -1 end
if current~=ARGV[3] then return -1 end
local after=history_hash(KEYS[1])
for i=7,#ARGV,2 do after[ARGV[i]]=ARGV[i+1] end
after.revision=ARGV[5]
local changes={
 {id=ARGV[1],path=after.path,after=after,operation='metadata'}
}
local prefix=publication_prefix(KEYS[1])
local tracked=history_capture(prefix,ARGV[5],publication_origin(ARGV[6]),changes)
ARGV[6]=publication_payload(ARGV[6],prefix,changes,tracked)
for i=7,#ARGV,2 do redis.call('HSET',KEYS[1],ARGV[i],ARGV[i+1]) end
redis.call('HSET',KEYS[1],'revision',ARGV[5])
if ARGV[6] ~= '' then
 redis.call('XADD',KEYS[4],'MAXLEN','~',10000,'*','payload',ARGV[6])
 redis.call('PUBLISH',KEYS[5],ARGV[6])
end
return tracked and 3 or 1
`)

func (c *nativeClient) updatePublishedInode(ctx context.Context, p string, inode *inodeData, fields map[string]interface{}) error {
	if err := checkExpectedParent(ctx, p, inode.Parent); err != nil {
		return err
	}
	if err := checkWriteCondition(ctx, inode, false); err != nil {
		return err
	}
	revision := newOriginID()
	generation, _ := ctx.Value(workspaceGenerationKey{}).(string)
	args := []interface{}{inode.ID, inode.Name, inode.Revision, generation, revision, c.invalidationPayload(InvalidateOpInode, p)}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		args = append(args, name, fields[name])
	}
	code, err := c.runPreparedMutation(ctx, updateInodeScript, true, revision, []filehistory.PrepareRequest{{InodeID: inode.ID, ExpectedRevision: inode.Revision, Path: p, AfterType: inode.Type}}, []string{c.keys.inode(inode.ID), c.keys.dirents(inode.Parent), c.keys.generation(), c.keys.changesStream(), c.keys.invalidateChannel(), c.leaseGuardKey(ctx)}, args...)
	if err != nil {
		return err
	}
	switch code {
	case -1:
		return ErrWriteConflict
	case -2:
		return ErrWorkspaceChanged
	case -4:
		return errors.New("workspace change journal has wrong Redis type")
	case -5:
		return ErrNativeSessionLost
	}
	inode.Revision = revision
	c.pruneHistory(ctx, code)
	return nil
}
