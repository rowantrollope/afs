package filehistory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/rediscontent"
)

func testHistory(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// seedRecord uses the immutable capture schema without exercising the mutation
// layer, so query and retention regressions remain independently reproducible.
func seedRecord(t *testing.T, rdb *redis.Client, r Record, seq int64, body string) Record {
	t.Helper()
	ctx := context.Background()
	prefix := Prefix("test")
	if r.ID == "" {
		r.ID = fmt.Sprintf("%s-v%d", r.FileID, r.Version)
	}
	if r.Type == "" {
		r.Type = "file"
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().UnixMilli()
	}
	if r.ContentRef == "" {
		r.ContentRef = rediscontent.RefExternal
	}
	if !r.Deleted && r.Type == "file" {
		r.Size = int64(len(body))
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, prefix+"records", r.ID, data)
	pipe.HSet(ctx, prefix+"file:"+r.FileID, "next", r.Version, "head", r.ID)
	pipe.ZAdd(ctx, prefix+"versions:"+r.FileID, redis.Z{Score: float64(r.Version), Member: r.ID})
	pipe.ZAdd(ctx, pathKey(prefix, r.Path), redis.Z{Score: float64(seq), Member: r.FileID})
	pipe.ZAdd(ctx, prefix+"retention", redis.Z{Score: float64(seq), Member: r.ID})
	if !r.Deleted {
		pipe.HSet(ctx, prefix+"file:"+r.FileID, "recoverable", r.ID)
		if r.Type == "file" {
			pipe.Set(ctx, prefix+"body:"+r.ID, body, 0)
			pipe.IncrBy(ctx, prefix+"bytes", r.Size)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPolicyValidationAndMatching(t *testing.T) {
	p := Policy{Mode: ModePaths, Include: []string{"src/**/*.go", "README.?d", "café/**"}, Exclude: []string{"**/vendor/**", "**/*_test.go"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name string
		want bool
	}{
		{"src/main.go", true}, {"/src/nested/main.go", true}, {"src/main_test.go", false}, {"src/vendor/lib.go", false}, {"main.go", false}, {"README.md", true}, {"café/a", true},
	} {
		if got := p.Matches(item.name); got != item.want {
			t.Errorf("Matches(%q)=%v, want %v", item.name, got, item.want)
		}
	}
	for _, invalid := range []Policy{{Mode: "invalid"}, {Mode: ModePaths}, {MaxVersions: -1}, {MaxBytes: -1}, {Include: []string{"[ab"}}, {Exclude: []string{"../a"}}} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("accepted invalid policy %+v", invalid)
		}
	}
	if err := (Policy{Include: []string{strings.Repeat("a", 1025)}}).Validate(); err == nil {
		t.Fatal("accepted oversized pattern")
	}
	if err := (Policy{Include: make([]string, 129)}).Validate(); err == nil {
		t.Fatal("accepted oversized pattern set")
	}
	for _, name := range []string{"", "/", "a/../b", "../a", "x\x00y"} {
		if _, err := NormalizePath(name); err == nil {
			t.Errorf("accepted path %q", name)
		}
	}
	rdb := testHistory(t)
	ctx := context.Background()
	defaultPolicy, err := GetPolicy(ctx, rdb, "test")
	if err != nil || defaultPolicy.Mode != ModeOff {
		t.Fatalf("default = %+v, %v", defaultPolicy, err)
	}
	if err := SetPolicy(ctx, rdb, "test", p); err != nil {
		t.Fatal(err)
	}
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: "bad"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	stored, err := GetPolicy(ctx, rdb, "test")
	if err != nil || !stored.Matches("src/main.go") {
		t.Fatalf("policy changed on failed validation: %+v, %v", stored, err)
	}
}

func TestListPaginationRenamesAndPathReuse(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/old"}, 1, "one")
	seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/new", PreviousPath: "/old", Operation: "rename"}, 2, "two")
	seedRecord(t, rdb, Record{FileID: "a", Version: 3, Path: "/new", Operation: "delete", Deleted: true}, 3, "")
	seedRecord(t, rdb, Record{FileID: "b", Version: 1, Path: "/new"}, 4, "replacement")
	latest, err := List(ctx, rdb, "test", "new", 10, 0, "")
	if err != nil || latest.FileID != "b" || len(latest.Versions) != 1 {
		t.Fatalf("latest=%+v, %v", latest, err)
	}
	first, err := List(ctx, rdb, "test", "/new", 2, 0, "a")
	if err != nil || len(first.Versions) != 2 || first.NextBefore != 2 {
		t.Fatalf("first=%+v, %v", first, err)
	}
	second, err := List(ctx, rdb, "test", "/new", 2, first.NextBefore, "a")
	if err != nil || len(second.Versions) != 1 || second.Versions[0].Path != "/old" || second.NextBefore != 0 {
		t.Fatalf("second=%+v, %v", second, err)
	}
	lineages, err := Lineages(ctx, rdb, "test", "/new", 1, 0)
	if err != nil || len(lineages.Files) != 1 || lineages.Files[0].FileID != "b" || lineages.NextBefore != 4 {
		t.Fatalf("lineages=%+v, %v", lineages, err)
	}
	older, err := Lineages(ctx, rdb, "test", "/new", 1, lineages.NextBefore)
	if err != nil || len(older.Files) != 1 || older.Files[0].FileID != "a" {
		t.Fatalf("older=%+v, %v", older, err)
	}
}

func TestGetSelectorsAndRecoveryAfterDeletion(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	one := seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/file", Mode: 0600}, 1, "one")
	seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/file", Mode: 0640}, 2, "two")
	seedRecord(t, rdb, Record{FileID: "a", Version: 3, Path: "/file", Deleted: true}, 3, "")
	seedRecord(t, rdb, Record{FileID: "other", Version: 1, Path: "/other"}, 4, "other")
	for _, item := range []struct {
		selector, body string
		version        int64
	}{{"latest", "two", 2}, {"", "two", 2}, {"1", "one", 1}, {one.ID, "one", 1}, {"3", "", 3}} {
		record, body, err := Get(ctx, rdb, "test", "file", item.selector, "")
		if err != nil || string(body) != item.body || record.Version != item.version {
			t.Fatalf("Get(%q)=%+v,%q,%v", item.selector, record, body, err)
		}
	}
	for _, selector := range []string{"999", "other-v1"} {
		if _, _, err := Get(ctx, rdb, "test", "file", selector, ""); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Get(%q): %v", selector, err)
		}
	}
	if _, _, err := Get(ctx, rdb, "test", "file", "1", "other"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign lineage: %v", err)
	}
	keys, err := rdb.Keys(ctx, Prefix("test")+"read:*").Result()
	if err != nil || len(keys) != 0 {
		t.Fatalf("read pins leaked: %v,%v", keys, err)
	}
	if err := rdb.Del(ctx, Prefix("test")+"body:"+one.ID).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get(ctx, rdb, "test", "file", "1", ""); err == nil {
		t.Fatal("missing body was returned as empty content")
	}
}

func TestDirectoryRenameResolvesLiveLineage(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	record := seedRecord(t, rdb, Record{FileID: "live", Version: 1, Path: "/old/file"}, 5, "body")
	seedRecord(t, rdb, Record{FileID: "prior", Version: 1, Path: "/new/file", Deleted: true}, 3, "")
	fs := "afs-lite:{test}:"
	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, fs+"dirents:1", "new", "2")
	pipe.HSet(ctx, fs+"dirents:2", "file", "3")
	pipe.HSet(ctx, fs+"inode:3", "history_id", "live")
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := List(ctx, rdb, "test", "new/file", 10, 0, "")
	if err != nil || page.FileID != "live" {
		t.Fatalf("List=%+v,%v", page, err)
	}
	got, body, err := Get(ctx, rdb, "test", "new/file", "latest", "")
	if err != nil || got.ID != record.ID || string(body) != "body" {
		t.Fatalf("Get=%+v,%q,%v", got, body, err)
	}
	lineages, err := Lineages(ctx, rdb, "test", "new/file", 1, 0)
	if err != nil || len(lineages.Files) != 1 || lineages.Files[0].FileID != "live" || lineages.NextBefore != 5 {
		t.Fatalf("Lineages=%+v,%v", lineages, err)
	}
	second, err := Lineages(ctx, rdb, "test", "new/file", 1, lineages.NextBefore)
	if err != nil || len(second.Files) != 1 || second.Files[0].FileID != "prior" {
		t.Fatalf("Lineages second=%+v,%v", second, err)
	}
}

func TestPrunePreservesRecoveryAndPinnedBody(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	prefix := Prefix("test")
	one := seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/file"}, 1, "one")
	seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/file"}, 2, "two")
	seedRecord(t, rdb, Record{FileID: "a", Version: 3, Path: "/file", Deleted: true}, 3, "")
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	pin := prefix + "read:test-pin"
	if err := pinScript.Run(ctx, rdb, []string{pathKey(prefix, "/file"), prefix + "records", pin}, prefix, "a", "1", 300000, "/file").Err(); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(ctx, rdb, "test", 100)
	if err != nil || n != 1 {
		t.Fatalf("Prune=%d,%v", n, err)
	}
	if exists := rdb.Exists(ctx, prefix+"body:"+one.ID).Val(); exists != 0 {
		t.Fatal("pruned body remains")
	}
	if got := rdb.Get(ctx, pin).Val(); got != "one" {
		t.Fatalf("pin lost: %q", got)
	}
	if got := rdb.Get(ctx, prefix+"bytes").Val(); got != "3" {
		t.Fatalf("retained bytes=%q", got)
	}
	if ttl := rdb.PTTL(ctx, pin).Val(); ttl <= 0 {
		t.Fatalf("pin lacks expiry: %v", ttl)
	}
	page, err := List(ctx, rdb, "test", "file", 10, 0, "")
	if err != nil || len(page.Versions) != 2 || !page.Versions[0].Deleted {
		t.Fatalf("protected records=%+v,%v", page, err)
	}
	_, body, err := Get(ctx, rdb, "test", "file", "latest", "")
	if err != nil || string(body) != "two" {
		t.Fatalf("undelete body=%q,%v", body, err)
	}
}

func TestPruneBoundedCursorAgeAndBytes(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	prefix := Prefix("test")
	seedRecord(t, rdb, Record{FileID: "protected", Version: 1, Path: "/protected"}, 1, "head")
	old := time.Now().Add(-72 * time.Hour).UnixMilli()
	seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/a", CreatedAt: old}, 2, "old")
	seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/a"}, 3, "recent")
	seedRecord(t, rdb, Record{FileID: "a", Version: 3, Path: "/a"}, 4, "head")
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeOff, MaxAgeDays: 1, MaxBytes: 8}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, 1, 1, 0} {
		n, err := Prune(ctx, rdb, "test", 1)
		if err != nil || n != want {
			t.Fatalf("Prune step%d=%d,%v want%d", i, n, err, want)
		}
	}
	if got := rdb.Get(ctx, prefix+"bytes").Val(); got != "8" {
		t.Fatalf("retained bytes=%q", got)
	}
	if n, err := Prune(ctx, rdb, "test", 1); err != nil || n != 0 {
		t.Fatalf("wrap=%d,%v", n, err)
	}
	if got := rdb.Get(ctx, prefix+"prune_cursor").Val(); got != "0" {
		t.Fatalf("cursor failed to wrap: %q", got)
	}
}

func TestPrunePreflightsWholeBatch(t *testing.T) {
	for _, corruption := range []string{"record", "lineage_type", "byte_counter", "version_index", "missing_recoverable"} {
		t.Run(corruption, func(t *testing.T) {
			rdb := testHistory(t)
			ctx := context.Background()
			prefix := Prefix("test")
			first := seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/a"}, 1, "one")
			seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/a"}, 2, "two")
			later := seedRecord(t, rdb, Record{FileID: "b", Version: 1, Path: "/b"}, 3, "three")
			seedRecord(t, rdb, Record{FileID: "b", Version: 2, Path: "/b"}, 4, "four")
			if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxVersions: 1}); err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "record":
				if err := rdb.HSet(ctx, prefix+"records", later.ID, "{").Err(); err != nil {
					t.Fatal(err)
				}
			case "lineage_type":
				if err := rdb.Set(ctx, prefix+"file:b", "wrong-type", 0).Err(); err != nil {
					t.Fatal(err)
				}
			case "byte_counter":
				if err := rdb.Set(ctx, prefix+"bytes", "not-an-integer", 0).Err(); err != nil {
					t.Fatal(err)
				}
			case "version_index":
				if err := rdb.Set(ctx, prefix+"versions:b", "wrong-type", 0).Err(); err != nil {
					t.Fatal(err)
				}
			case "missing_recoverable":
				if err := rdb.HDel(ctx, prefix+"file:b", "recoverable").Err(); err != nil {
					t.Fatal(err)
				}
			}
			before := rdb.Get(ctx, prefix+"bytes").Val()
			if _, err := Prune(ctx, rdb, "test", 100); err == nil {
				t.Fatal("accepted corrupt retention metadata")
			}
			if got := rdb.Get(ctx, prefix+"body:"+first.ID).Val(); got != "one" {
				t.Fatal("deleted an earlier snapshot before rejecting later corrupt metadata")
			}
			if exists := rdb.HExists(ctx, prefix+"records", first.ID).Val(); !exists {
				t.Fatal("deleted record before validation finished")
			}
			if got := rdb.Get(ctx, prefix+"bytes").Val(); got != before {
				t.Fatalf("byte accounting changed from %q to %q", before, got)
			}
		})
	}
}

func checkLuaGlobContract(t *testing.T, rdb *redis.Client) {
	t.Helper()
	ctx := context.Background()
	patterns := []string{"*", "**", "?.txt", "é*.txt", "café/**", "src/**/*.go", "**/vendor/**", "**/*_test.go", "a?c", "*é?", "a/**/z", "a**z", "**/**/?.txt"}
	paths := []string{"é.txt", "a.txt", "猫.txt", "café/a", "src/main.go", "src/nested/main.go", "src/main_test.go", "src/vendor/a.go", "abc", "aéc", "aé猫", "a/z", "a/b/c/z", "a\nz", "a\n/b/z", strings.Repeat("a/", 2000) + "é.txt"}
	script := redis.NewScript(CaptureLua + `return history_glob(ARGV[1],ARGV[2]) and 1 or 0`)
	for _, pattern := range patterns {
		p := Policy{Mode: ModePaths, Include: []string{pattern}}
		for _, name := range paths {
			got, err := script.Run(ctx, rdb, nil, pattern, "/"+name).Int()
			if err != nil {
				t.Fatalf("glob(%q, path length%d): %v", pattern, len(name), err)
			}
			if (got == 1) != p.Matches(name) {
				t.Fatalf("Go/Lua glob mismatch: pattern %q path %q Go=%v Lua=%d", pattern, name, p.Matches(name), got)
			}
		}
	}
}

func TestLuaGlobMatchesPolicy(t *testing.T) { checkLuaGlobContract(t, testHistory(t)) }

func TestCaptureCounterAndCopyPreflight(t *testing.T) {
	for _, counter := range []string{"100000000000000", "99999999999998", "body_collision"} {
		t.Run(counter, func(t *testing.T) {
			rdb := testHistory(t)
			ctx := context.Background()
			prefix := Prefix("test")
			fs := "afs-lite:{test}:"
			if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll}); err != nil {
				t.Fatal(err)
			}
			before := map[string]string{"type": "file", "path": "/file", "name": "file", "parent": "1", "content_ref": "ext", "size": "3", "mode": "420", "revision": "old", "history_id": "a"}
			if err := rdb.HSet(ctx, fs+"inode:2", before).Err(); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, fs+"content:2", "old", 0).Err(); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, fs+"stage", "new", 0).Err(); err != nil {
				t.Fatal(err)
			}
			after := map[string]string{"type": "file", "path": "/file", "name": "file", "parent": "1", "content_ref": "ext", "size": "3", "mode": "420", "revision": "operation"}
			if counter == "body_collision" {
				if err := rdb.Set(ctx, prefix+"body:operation-2-a", "collision", 0).Err(); err != nil {
					t.Fatal(err)
				}
			} else if err := rdb.HSet(ctx, prefix+"file:a", "next", counter).Err(); err != nil {
				t.Fatal(err)
			}
			oldHash := fmt.Sprintf("%x", sha256.Sum256([]byte("old")))
			newHash := fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
			changes, _ := json.Marshal([]any{map[string]any{"id": "2", "path": "/file", "after": after, "body": fs + "stage", "operation": "write", "before_hash": oldHash, "after_hash": newHash}})
			script := CaptureLua + `return history_capture(ARGV[1], 'operation', 'test', cjson.decode(ARGV[2]))`
			err := rdb.Eval(ctx, script, nil, prefix, string(changes)).Err()
			if err == nil {
				t.Fatal("invalid history metadata was accepted")
			}
			wantError := "history version counter is invalid or exhausted"
			if counter == "body_collision" {
				wantError = "history operation identity already exists"
			}
			if !strings.Contains(err.Error(), wantError) {
				t.Fatalf("capture error = %v, want %q", err, wantError)
			}
			if got := rdb.Get(ctx, fs+"content:2").Val(); got != "old" {
				t.Fatalf("live content changed: %q", got)
			}
			if n := rdb.HLen(ctx, prefix+"records").Val(); n != 0 {
				t.Fatalf("published %d records before validation", n)
			}
			if exists := rdb.Exists(ctx, prefix+"blob:"+oldHash).Val(); exists != 0 {
				t.Fatal("orphan baseline copied before destination validation")
			}
		})
	}
}
