package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func historyRecords(t *testing.T, ctx context.Context, rdb *redis.Client, id, path string) []filehistory.Record {
	t.Helper()
	page, err := filehistory.List(ctx, rdb, id, path, 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Versions
}

func requireHistoryBytes(t *testing.T, ctx context.Context, rdb *redis.Client, id string, r filehistory.Record, want []byte) {
	t.Helper()
	_, got, err := filehistory.Get(ctx, rdb, id, r.Path, r.ID, r.FileID)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("version %s bytes %q, err %v; want %q", r.ID, got, err, want)
	}
	if ttl := rdb.TTL(ctx, filehistory.BodyKey(id, r)).Val(); ttl != -1 {
		t.Fatalf("history body TTL %v", ttl)
	}
}

func TestFileHistoryPublishedMutations(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-mutations"
	c := New(rdb, id).(*nativeClient)
	if err := c.Echo(ctx, "/file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if n := rdb.Exists(ctx, filehistory.Prefix(id)+"records").Val(); n != 0 {
		t.Fatal("history must default off")
	}
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.createInodeAtPath(ctx, "/helper", &inodeData{Type: "file", Mode: 0o640, Size: 4, Content: "seed"}, false); err != nil {
		t.Fatal(err)
	}
	helperVersions := historyRecords(t, ctx, rdb, id, "/helper")
	if len(helperVersions) != 1 {
		t.Fatalf("helper creation versions %+v", helperVersions)
	}
	requireHistoryBytes(t, ctx, rdb, id, helperVersions[0], []byte("seed"))
	if err := c.WriteChunks(ctx, "/file", map[int][]byte{0: []byte("new")}, 3, 3, nil); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/file")
	if len(versions) != 2 || versions[1].Operation != "baseline" {
		t.Fatalf("versions %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte("new"))
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte("old"))
	stat, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteInodeAt(ctx, stat.Inode, []byte("XYZ"), 1); err != nil {
		t.Fatal(err)
	}
	if err := c.TruncateInode(ctx, stat.Inode, 2); err != nil {
		t.Fatal(err)
	}
	if err := c.Chmod(ctx, "/file", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Mv(ctx, "/file", "/renamed"); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/renamed")
	if len(versions) != 6 || versions[0].Operation != "rename" || versions[0].PreviousPath != "/file" || versions[0].Mode != 0o600 {
		t.Fatalf("versions %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte("nX"))
	requireHistoryBytes(t, ctx, rdb, id, versions[3], []byte("nXYZ"))
	lineage := versions[0].FileID
	if err := c.Rm(ctx, "/renamed"); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/renamed")
	if !versions[0].Deleted || versions[0].FileID != lineage {
		t.Fatalf("deletion %+v", versions)
	}
	if err := c.Echo(ctx, "/renamed", nil); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/renamed")
	if len(versions) != 1 || versions[0].FileID == lineage {
		t.Fatalf("recreation %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[0], nil)
}

func TestFileHistoryFirstDeleteAndReplacedDestination(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-first-delete"
	c := New(rdb, id)
	for path, data := range map[string]string{"/first": "first body", "/src": "source body", "/dst": "destination body"} {
		if err := c.Echo(ctx, path, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Rm(ctx, "/first"); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/first")
	if len(versions) != 2 || !versions[0].Deleted {
		t.Fatalf("first delete %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte("first body"))
	if err := c.Mv(ctx, "/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/dst")
	if len(versions) != 2 || versions[0].Operation != "rename" {
		t.Fatalf("renamed %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte("source body"))
	lineages, err := filehistory.Lineages(ctx, rdb, id, "/dst", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineages.Files) != 2 {
		t.Fatalf("lineages %+v", lineages)
	}
}

func TestFileHistorySymlinkAndDirectoryRename(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-links"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Ln(ctx, "../target", "/dir/link"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/dir/file", []byte{0, 1, 255, 10}); err != nil {
		t.Fatal(err)
	}
	if err := c.Mv(ctx, "/dir", "/moved"); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/moved/link")
	if len(versions) != 1 || versions[0].Type != "symlink" || versions[0].Target != "../target" {
		t.Fatalf("link %+v", versions)
	}
	versions = historyRecords(t, ctx, rdb, id, "/moved/file")
	if len(versions) != 1 {
		t.Fatalf("child history %+v", versions)
	}
	if err := c.Echo(ctx, "/moved/file", []byte("now")); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/moved/file")
	if versions[0].Path != "/moved/file" {
		t.Fatalf("canonical path %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte{0, 1, 255, 10})
}

func TestFileHistoryFailureCannotPublish(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	for _, fault := range []string{"records", "retention", "bytes", "sequence", "file-counter", "journal", "info-counter"} {
		t.Run(fault, func(t *testing.T) {
			id := "history-fault-" + fault
			c := New(rdb, id)
			if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
				t.Fatal(err)
			}
			if err := c.Echo(ctx, "/f", []byte("before")); err != nil {
				t.Fatal(err)
			}
			version := historyRecords(t, ctx, rdb, id, "/f")[0]
			prefix := filehistory.Prefix(id)
			switch fault {
			case "records", "retention":
				rdb.Del(ctx, prefix+fault)
				rdb.Set(ctx, prefix+fault, "wrong type", 0)
			case "bytes", "sequence":
				rdb.Set(ctx, prefix+fault, "9223372036854775807", 0)
			case "file-counter":
				rdb.HSet(ctx, prefix+"file:"+version.FileID, "next", "9223372036854775807")
			case "journal":
				rdb.Del(ctx, newKeyBuilder(id).changesStream())
				rdb.Set(ctx, newKeyBuilder(id).changesStream(), "wrong", 0)
			case "info-counter":
				rdb.HSet(ctx, newKeyBuilder(id).info(), "total_data_bytes", "9223372036854775807")
			}
			if err := c.Echo(ctx, "/f", []byte("after")); err == nil {
				t.Fatal("corrupt history accepted mutation")
			}
			got, err := c.Cat(ctx, "/f")
			if err != nil || string(got) != "before" {
				t.Fatalf("live %q %v", got, err)
			}
		})
	}
}

func TestFileHistoryConcurrentWritersAndLostAcknowledgment(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-concurrent"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("base")); err != nil {
		t.Fatal(err)
	}
	observed, err := c.Stat(ctx, "/f")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- New(rdb, id).Echo(WithExpectedStat(ctx, observed), "/f", []byte(fmt.Sprint(i)))
		}(i)
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrWriteConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners %d", winners)
	}
	versions := historyRecords(t, ctx, rdb, id, "/f")
	if len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("concurrent versions %+v", versions)
	}
	writerRedis := redis.NewClient(rdb.Options())
	defer writerRedis.Close()
	writerRedis.AddHook(&publicationCommandHook{after: func() {}})
	if err := New(writerRedis, id).Echo(ctx, "/f", []byte("lost ack")); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/f")
	if len(versions) != 3 {
		t.Fatalf("retry duplicated versions %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte("lost ack"))
}

func TestFileHistoryPolicyAndRetention(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-policy"
	c := New(rdb, id)
	policy := filehistory.Policy{Mode: "paths", Include: []string{"**/*.txt"}, Exclude: []string{"private/**"}, MaxVersions: 2}
	if err := filehistory.SetPolicy(ctx, rdb, id, policy); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/a.txt", "/dir/a.txt", "/private/a.txt", "/a.bin"} {
		if err := c.Echo(ctx, path, []byte("one")); err != nil {
			t.Fatal(err)
		}
	}
	if len(historyRecords(t, ctx, rdb, id, "/a.txt")) != 1 || len(historyRecords(t, ctx, rdb, id, "/dir/a.txt")) != 1 {
		t.Fatal("glob failed")
	}
	for i := 0; i < 5; i++ {
		if err := c.Echo(ctx, "/a.txt", []byte(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	versions := historyRecords(t, ctx, rdb, id, "/a.txt")
	if len(versions) != 2 || versions[0].Version != 6 {
		t.Fatalf("retention %+v", versions)
	}
	policy.Mode = "off"
	if err := filehistory.SetPolicy(ctx, rdb, id, policy); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/a.txt", []byte("during off")); err != nil {
		t.Fatal(err)
	}
	policy.Mode = "all"
	policy.MaxVersions = 0
	if err := filehistory.SetPolicy(ctx, rdb, id, policy); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/a.txt", []byte("enabled")); err != nil {
		t.Fatal(err)
	}
	versions = historyRecords(t, ctx, rdb, id, "/a.txt")
	if len(versions) != 4 || versions[1].Operation != "baseline" {
		t.Fatalf("reenable %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte("during off"))
}

func TestFileHistoryByteBudgetEvictsAtomically(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-budget"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all", MaxBytes: 4}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"aaaa", "bbbb", "cccc"} {
		if err := c.Echo(ctx, "/f", []byte(value)); err != nil {
			t.Fatal(err)
		}
		versions := historyRecords(t, ctx, rdb, id, "/f")
		if len(versions) != 1 {
			t.Fatalf("retained versions %+v", versions)
		}
		requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte(value))
		if got := rdb.Get(ctx, filehistory.Prefix(id)+"bytes").Val(); got != "4" {
			t.Fatalf("bytes %s", got)
		}
	}
	if err := c.Echo(ctx, "/f", []byte("too large")); err == nil {
		t.Fatal("oversize head accepted")
	}
	got, err := c.Cat(ctx, "/f")
	if err != nil || string(got) != "cccc" {
		t.Fatalf("rejected bytes %q %v", got, err)
	}
	if err := c.Rm(ctx, "/f"); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/f")
	if len(versions) != 2 || !versions[0].Deleted {
		t.Fatalf("deletion lost recoverable head %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte("cccc"))
	if err := c.Echo(ctx, "/g", []byte("next")); err == nil {
		t.Fatal("budget discarded a deleted file's recoverable head")
	}
}

func TestFileHistoryPolicyReadAtCommit(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-policy-race"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/f", []byte("off")); err != nil {
		t.Fatal(err)
	}
	writerRedis := redis.NewClient(rdb.Options())
	defer writerRedis.Close()
	writerRedis.AddHook(&publicationCommandHook{before: func() {
		if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
			t.Fatal(err)
		}
	}})
	if err := New(writerRedis, id).Echo(ctx, "/f", []byte("on")); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/f")
	if len(versions) != 2 {
		t.Fatalf("policy change missed publication: %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[1], []byte("off"))
	requireHistoryBytes(t, ctx, rdb, id, versions[0], []byte("on"))
}

func TestFileHistoryOversizedDeletionRetainsTombstone(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-excluded-delete"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all", MaxFileBytes: 4}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("tiny")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("excluded large contents")); err != nil {
		t.Fatal(err)
	}
	if err := c.Rm(ctx, "/f"); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/f")
	if len(versions) != 3 || !versions[0].Deleted || !versions[1].MetadataOnly {
		t.Fatalf("oversized current body lost metadata or deletion: %+v", versions)
	}
	requireHistoryBytes(t, ctx, rdb, id, versions[2], []byte("tiny"))
}
