//go:build integration

package filehistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRealRedisPreparationConflictClassification(t *testing.T) {
	rdb := isolatedHistoryRedis(t)
	ctx := context.Background()
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll}); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, "afs:{test}:inode:2", "revision", "winner").Err(); err != nil {
		t.Fatal(err)
	}
	err := Prepare(ctx, rdb, "test", "attempt", []PrepareRequest{{InodeID: "2", ExpectedRevision: "old", Path: "/file"}})
	if !errors.Is(err, ErrPreparationConflict) {
		t.Fatalf("conflict classification: %v", err)
	}
}

func publishRealHistory(t *testing.T, rdb *redis.Client, operation, inode, body string, mode int) {
	t.Helper()
	ctx := context.Background()
	prefix := Prefix("test")
	fs := "afs:{test}:"
	stage := fs + "stage:" + operation
	if err := rdb.Set(ctx, stage, body, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	afterHash := hex.EncodeToString(sum[:])
	beforeHash := ""
	if old, err := rdb.Get(ctx, fs+"content:"+inode).Bytes(); err == nil {
		sum := sha256.Sum256(old)
		beforeHash = hex.EncodeToString(sum[:])
	}
	after := map[string]string{"type": "file", "name": inode, "parent": "1", "path": "/" + inode, "size": fmt.Sprint(len(body)), "mode": fmt.Sprint(mode), "revision": operation, "content_ref": "ext"}
	changes, _ := json.Marshal([]any{map[string]any{"id": inode, "path": "/" + inode, "after": after, "body": stage, "before_hash": beforeHash, "after_hash": afterHash, "operation": "write"}})
	script := CaptureLua + `
local changes=cjson.decode(ARGV[2])
local tracked=history_capture(ARGV[1],ARGV[3],'test',changes)
for _,change in ipairs(changes) do
 for field,value in pairs(change.after) do redis.call('HSET',ARGV[4]..'inode:'..change.id,field,value) end
 redis.call('COPY',change.body,ARGV[4]..'content:'..change.id,'REPLACE')
 redis.call('HSET',ARGV[4]..'dirents:1',change.after.name,change.id)
end
return tracked and 1 or 0`
	if err := rdb.Eval(ctx, script, nil, prefix, string(changes), operation, fs).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Unlink(ctx, stage).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRealRedisSharedHistoryOwnershipAndPruning(t *testing.T) {
	rdb := isolatedHistoryRedis(t)
	ctx := context.Background()
	prefix := Prefix("test")
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll}); err != nil {
		t.Fatal(err)
	}
	publishRealHistory(t, rdb, "one", "2", "shared", 0644)
	publishRealHistory(t, rdb, "two", "3", "shared", 0644)
	publishRealHistory(t, rdb, "noop", "2", "shared", 0644)
	publishRealHistory(t, rdb, "mode", "2", "shared", 0600)
	page, err := List(ctx, rdb, "test", "/2", 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Versions) != 2 {
		t.Fatalf("equivalent write or chmod capture: %+v", page)
	}
	shared := page.Versions[0].BlobID
	if got := rdb.HGet(ctx, prefix+"blobrefs", shared).Val(); got != "3" {
		t.Fatalf("shared refs=%s", got)
	}
	if got := rdb.Get(ctx, prefix+"physical_bytes").Val(); got != "6" {
		t.Fatalf("physical=%s", got)
	}
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(ctx, rdb, "test", 100); err != nil || n != 1 {
		t.Fatalf("prune=%d,%v", n, err)
	}
	for _, name := range []string{"/2", "/3"} {
		_, data, err := Get(ctx, rdb, "test", name, "latest", "")
		if err != nil || string(data) != "shared" {
			t.Fatalf("recover %s=%q,%v", name, data, err)
		}
	}
	publishRealHistory(t, rdb, "change2", "2", "new", 0600)
	publishRealHistory(t, rdb, "change3", "3", "new", 0644)
	if n, err := Prune(ctx, rdb, "test", 100); err != nil || n != 2 {
		t.Fatalf("last refs prune=%d,%v", n, err)
	}
	if n := rdb.Exists(ctx, prefix+"blob:"+shared).Val(); n != 0 {
		t.Fatal("unreferenced shared body remains")
	}
	if got := rdb.Get(ctx, prefix+"physical_bytes").Val(); got != "3" {
		t.Fatalf("reclaimed physical=%s", got)
	}
}

func TestRealRedisPrunesMetadataOnlyLineageWithoutRecoverableHead(t *testing.T) {
	rdb := isolatedHistoryRedis(t)
	ctx := context.Background()
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxFileBytes: 4}); err != nil {
		t.Fatal(err)
	}
	publishRealHistory(t, rdb, "large-one", "2", "oversized first", 0644)
	publishRealHistory(t, rdb, "large-two", "2", "oversized second", 0644)
	publishRealHistory(t, rdb, "small-one", "3", "old", 0644)
	publishRealHistory(t, rdb, "small-two", "3", "new", 0644)
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxFileBytes: 4, MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	if removed, err := Prune(ctx, rdb, "test", 100); err != nil || removed != 2 {
		t.Fatalf("prune metadata-only lineage and ordinary history: removed=%d err=%v", removed, err)
	}
	page, err := List(ctx, rdb, "test", "/2", 100, 0, "")
	if err != nil || len(page.Versions) != 1 || !page.Versions[0].MetadataOnly {
		t.Fatalf("metadata-only head: %+v err=%v", page, err)
	}
	_, body, err := Get(ctx, rdb, "test", "/3", "latest", "")
	if err != nil || string(body) != "new" {
		t.Fatalf("ordinary recoverable head: %q err=%v", body, err)
	}
}
