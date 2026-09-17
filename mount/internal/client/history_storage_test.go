package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/filehistory"
)

func contentSHA(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func TestHistorySharedSHA256BlobsAndEquivalentWrites(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-dedup"
	c := New(rdb, id)
	prefix := filehistory.Prefix(id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/a", "/b", "/a"} {
		if err := c.Echo(ctx, path, []byte("shared")); err != nil {
			t.Fatal(err)
		}
	}
	a := historyRecords(t, ctx, rdb, id, "/a")
	b := historyRecords(t, ctx, rdb, id, "/b")
	if len(a) != 1 || len(b) != 1 || a[0].BlobID != contentSHA("shared") || a[0].BlobID != b[0].BlobID {
		t.Fatalf("dedup/equivalence: %+v %+v", a, b)
	}
	if got := rdb.HGet(ctx, prefix+"blobrefs", a[0].BlobID).Val(); got != "2" {
		t.Fatalf("refs=%s", got)
	}
	if got := rdb.Get(ctx, prefix+"physical_bytes").Val(); got != "6" {
		t.Fatalf("unique bytes=%s", got)
	}
	if got := rdb.Get(ctx, prefix+"bytes").Val(); got != "12" {
		t.Fatalf("logical retained bytes=%s", got)
	}
	if err := c.Chmod(ctx, "/a", 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Mv(ctx, "/a", "/renamed"); err != nil {
		t.Fatal(err)
	}
	a = historyRecords(t, ctx, rdb, id, "/renamed")
	if len(a) != 3 || a[0].BlobID != a[1].BlobID || a[0].PrevHash != contentSHA("shared") || a[0].DeltaBytes != 0 {
		t.Fatalf("metadata/rename history %+v", a)
	}
	if got := rdb.HGet(ctx, prefix+"blobrefs", a[0].BlobID).Val(); got != "4" {
		t.Fatalf("refs after metadata=%s", got)
	}
	if got := rdb.Get(ctx, prefix+"physical_bytes").Val(); got != "6" {
		t.Fatalf("metadata duplicated bytes=%s", got)
	}
	for _, record := range a {
		requireHistoryBytes(t, ctx, rdb, id, record, []byte("shared"))
	}
}

func TestHistoryInitialEquivalentWriteDoesNotSeedBaseline(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-initial-noop"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/f", []byte("same")); err != nil {
		t.Fatal(err)
	}
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("same")); err != nil {
		t.Fatal(err)
	}
	if records := historyRecords(t, ctx, rdb, id, "/f"); len(records) != 0 {
		t.Fatalf("equivalent write captured %+v", records)
	}
	if err := c.Echo(ctx, "/f", []byte("different")); err != nil {
		t.Fatal(err)
	}
	records := historyRecords(t, ctx, rdb, id, "/f")
	if len(records) != 2 || records[1].ContentHash != contentSHA("same") || records[0].PrevHash != contentSHA("same") || records[0].DeltaBytes != 5 {
		t.Fatalf("baseline %+v", records)
	}
}

func TestHistoryMetadataOnlyAndRecoverableRetention(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-large-metadata"
	c := New(rdb, id)
	prefix := filehistory.Prefix(id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all", MaxFileBytes: 4, MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("tiny")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/f", []byte("too large")); err != nil {
		t.Fatal(err)
	}
	records := historyRecords(t, ctx, rdb, id, "/f")
	if len(records) != 2 || !records[0].MetadataOnly || records[0].BlobID != "" || records[0].ContentHash != contentSHA("too large") || records[0].Size != 9 {
		t.Fatalf("metadata-only %+v", records)
	}
	_, body, err := filehistory.Get(ctx, rdb, id, "/f", records[0].ID, records[0].FileID)
	if err != nil || body != nil {
		t.Fatalf("metadata-only body=%q err=%v", body, err)
	}
	_, body, err = filehistory.Get(ctx, rdb, id, "/f", "latest", "")
	if err != nil || string(body) != "tiny" {
		t.Fatalf("recoverable=%q,%v", body, err)
	}
	if got := rdb.Get(ctx, prefix+"bytes").Val(); got != "4" {
		t.Fatalf("metadata charged bytes=%s", got)
	}
	if err := c.Rm(ctx, "/f"); err != nil {
		t.Fatal(err)
	}
	records = historyRecords(t, ctx, rdb, id, "/f")
	if len(records) != 2 || !records[0].Deleted || records[0].DeltaBytes != -9 || records[0].PrevHash != contentSHA("too large") {
		t.Fatalf("tombstone %+v", records)
	}
}

func TestHistorySharedBlobPruneReclaimsOnlyLastReference(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-shared-prune"
	c := New(rdb, id)
	prefix := filehistory.Prefix(id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all", MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/a", "/b"} {
		if err := c.Echo(ctx, path, []byte("old")); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Echo(ctx, "/a", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := rdb.HGet(ctx, prefix+"blobrefs", contentSHA("old")).Val(); got != "1" {
		t.Fatalf("remaining shared refs=%s", got)
	}
	requireHistoryBytes(t, ctx, rdb, id, historyRecords(t, ctx, rdb, id, "/b")[0], []byte("old"))
	if err := c.Echo(ctx, "/b", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if n := rdb.Exists(ctx, prefix+"blob:"+contentSHA("old")).Val(); n != 0 {
		t.Fatal("unreferenced body not reclaimed")
	}
	if got := rdb.HGet(ctx, prefix+"blobrefs", contentSHA("new")).Val(); got != "2" {
		t.Fatalf("new refs=%s", got)
	}
	if got := rdb.Get(ctx, prefix+"physical_bytes").Val(); got != "3" {
		t.Fatalf("physical bytes=%s", got)
	}
}

func TestHistoryCorruptSharedOwnershipFailsBeforeCommit(t *testing.T) {
	for _, fault := range []string{"refs", "refs_exhausted", "backend", "physical", "physical_new"} {
		t.Run(fault, func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			id := "history-ownership-" + fault
			c := New(rdb, id)
			prefix := filehistory.Prefix(id)
			if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all", MaxBytes: 8}); err != nil {
				t.Fatal(err)
			}
			if err := c.Echo(ctx, "/a", []byte("aaaa")); err != nil {
				t.Fatal(err)
			}
			if err := c.Echo(ctx, "/b", []byte("bbbb")); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "refs":
				rdb.HSet(ctx, prefix+"blobrefs", contentSHA("bbbb"), "bad")
			case "refs_exhausted":
				rdb.HSet(ctx, prefix+"blobrefs", contentSHA("bbbb"), "99999999999999")
			case "backend":
				rdb.HSet(ctx, prefix+"blobmeta", contentSHA("bbbb"), `{"size":4,"ref":"unknown"}`)
			case "physical", "physical_new":
				rdb.Set(ctx, prefix+"physical_bytes", "0", 0)
			}
			body := "bbbb"
			if fault == "physical_new" {
				body = "cccc"
			}
			if err := c.Echo(ctx, "/a", []byte(body)); err == nil {
				t.Fatal("corrupt blob state accepted")
			}
			if got, err := c.Cat(ctx, "/a"); err != nil || string(got) != "aaaa" {
				t.Fatalf("live changed=%q,%v", got, err)
			}
			if n := rdb.Exists(ctx, prefix+"blob:"+contentSHA("aaaa")).Val(); n != 1 {
				t.Fatal("old blob removed before rejecting corruption")
			}
		})
	}
}

func TestHistoryPathTimelinePaginationAndCurrentPath(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-path-timeline"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"one", "two"} {
		if err := c.Echo(ctx, "/dir/f", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	old := historyRecords(t, ctx, rdb, id, "/dir/f")[0]
	if err := c.Mv(ctx, "/dir", "/moved"); err != nil {
		t.Fatal(err)
	}
	if path, err := filehistory.CurrentPath(ctx, rdb, id, old.FileID); err != nil || path != "/moved/f" {
		t.Fatalf("current path=%q,%v", path, err)
	}
	page, err := filehistory.PathPage(ctx, rdb, id, "/moved/f", 1, "", false)
	if err != nil || len(page.Versions) != 1 || page.Versions[0].Version != 1 || page.NextCursor == "" {
		t.Fatalf("first page=%+v,%v", page, err)
	}
	next, err := filehistory.PathPage(ctx, rdb, id, "/moved/f", 1, page.NextCursor, false)
	if err != nil || len(next.Versions) != 1 || next.Versions[0].Version != 2 || next.NextCursor != "" {
		t.Fatalf("second page=%+v,%v", next, err)
	}
	if err := c.Mv(ctx, "/moved/f", "/other"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/moved/f", []byte("recreated")); err != nil {
		t.Fatal(err)
	}
	page, err = filehistory.PathPage(ctx, rdb, id, "/moved/f", 10, "", true)
	if err != nil || len(page.Versions) != 2 || page.Versions[0].FileID == page.Versions[1].FileID {
		t.Fatalf("rename/recreated path page=%+v,%v", page, err)
	}
}

func TestHistoryRangeWritesHaveFullSHA256(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-range-sha"
	c := New(rdb, id).(*nativeClient)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("abcdef", 10000)
	if err := c.Echo(ctx, "/f", []byte(content)); err != nil {
		t.Fatal(err)
	}
	stat, err := c.Stat(ctx, "/f")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteInodeAt(ctx, stat.Inode, []byte("NEW"), 12345); err != nil {
		t.Fatal(err)
	}
	want := content[:12345] + "NEW" + content[12348:]
	records := historyRecords(t, ctx, rdb, id, "/f")
	if records[0].ContentHash != contentSHA(want) || records[0].PrevHash != contentSHA(content) {
		t.Fatal(fmt.Sprintf("incorrect range hashes: %+v", records))
	}
	requireHistoryBytes(t, ctx, rdb, id, records[0], []byte(want))
}

func TestHistoryActivityContinuesWithCaptureDisabled(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-off-activity"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/a", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := c.Chmod(ctx, "/a", 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Mv(ctx, "/a", "/b"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rm(ctx, "/b"); err != nil {
		t.Fatal(err)
	}
	entries, err := rdb.XRange(ctx, newKeyBuilder(id).changesStream(), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"put", "chmod", "rename", "delete"}
	structured := 0
	for _, entry := range entries {
		var payload struct {
			Op       string   `json:"op"`
			Paths    []string `json:"paths"`
			Origin   string   `json:"origin"`
			Activity *bool    `json:"activity"`
			Change   struct {
				Op        string `json:"op"`
				Path      string `json:"path"`
				PrevPath  string `json:"prev_path"`
				VersionID string `json:"version_id"`
			} `json:"change"`
		}
		if err := json.Unmarshal([]byte(fmt.Sprint(entry.Values["payload"])), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Change.Op == "" {
			if payload.Activity == nil || *payload.Activity {
				t.Fatal("supplemental native invalidation was not excluded from activity")
			}
			continue
		}
		i := structured
		structured++
		if i >= len(want) {
			t.Fatal("duplicated structured activity")
		}
		if payload.Change.Op != want[i] || payload.Op == "" || len(payload.Paths) == 0 || payload.Origin == "" || payload.Change.VersionID != "" {
			t.Fatalf("activity payload %+v", payload)
		}
		if i == 2 && (payload.Change.Path != "/b" || payload.Change.PrevPath != "/a") {
			t.Fatalf("rename activity %+v", payload.Change)
		}
	}
	if structured != len(want) {
		t.Fatalf("structured activity=%d", structured)
	}
	if n := rdb.HLen(ctx, filehistory.Prefix(id)+"records").Val(); n != 0 {
		t.Fatal("disabled activity unexpectedly captured history")
	}
}
