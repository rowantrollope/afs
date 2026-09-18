package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func TestRedisNamespaceIncludesHistoryForkAndRestore(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "namespace-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateWorkspaceVersioningPolicy(ctx, meta.ID, WorkspaceVersioningPolicy{Mode: WorkspaceVersioningModeAll}); err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("saved content")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("later content")); err != nil {
		t.Fatal(err)
	}

	// Assert the public storage contract with literal names, independently of
	// the key builders shared by publication, history queries and checkpoints.
	assertNamespace := func(id string, versions int64) {
		t.Helper()
		base := "afs:{" + id + "}:"
		for _, suffix := range []string{"workspace:meta", "generation", "inode:1", "dirents:1", "history:policy", "history:records"} {
			if exists, err := rdb.Exists(ctx, base+suffix).Result(); err != nil || exists != 1 {
				t.Fatalf("expected Redis key %s: exists=%d, err=%v", base+suffix, exists, err)
			}
		}
		inode, err := rdb.HGet(ctx, base+"dirents:1", "file").Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists, err := rdb.Exists(ctx, base+"inode:"+inode, base+"content:"+inode).Result(); err != nil || exists != 2 {
			t.Fatalf("live file escaped afs: namespace: exists=%d, err=%v", exists, err)
		}
		if count, err := rdb.HLen(ctx, base+"history:records").Result(); err != nil || count != versions {
			t.Fatalf("literal history records = %d, %v; want %d", count, err, versions)
		}
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, "*", 128).Result()
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range keys {
				if !strings.HasPrefix(key, "afs:") {
					t.Fatalf("operation wrote outside afs: namespace: %s", key)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	assertHistory := func(id string, want []string) string {
		t.Helper()
		page, err := s.GetFileHistory(ctx, id, "/file", false)
		if err != nil || len(page.Lineages) != 1 || len(page.Lineages[0].Versions) != len(want) {
			t.Fatalf("history = %+v, %v; want one lineage with %d versions", page, err, len(want))
		}
		lineage := page.Lineages[0]
		if path, err := filehistory.CurrentPath(ctx, rdb, id, lineage.FileID); err != nil || path != "/file" {
			t.Fatalf("history/live inode lookup = %q, %v", path, err)
		}
		for i, version := range lineage.Versions {
			content, err := s.GetFileVersionContent(ctx, id, version.VersionID)
			if err != nil || content.Content != want[i] {
				t.Fatalf("version %d content = %q, %v; want %q", i+1, content.Content, err, want[i])
			}
		}
		assertNamespace(id, int64(len(want)))
		return lineage.Versions[len(want)-1].VersionID
	}
	assertActivity := func(id, versionID string) {
		t.Helper()
		if count, err := rdb.XLen(ctx, "afs:{"+id+"}:changes").Result(); err != nil || count == 0 {
			t.Fatalf("literal activity stream = %d, %v; want published events", count, err)
		}
		activity, err := s.GetFileHistoryChanges(ctx, id, FileHistoryChangesRequest{Path: "/file", NewestFirst: true, Limit: 1})
		if err != nil || len(activity.Entries) != 1 || activity.Entries[0].VersionID != versionID {
			t.Fatalf("latest activity does not reference the latest version: %+v, %v", activity, err)
		}
	}
	assertActivity(meta.ID, assertHistory(meta.ID, []string{"saved content", "later content"}))

	if err := s.ForkWorkspace(ctx, meta.ID, "namespace-fork", "saved"); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetWorkspace(ctx, "namespace-fork")
	if err != nil {
		t.Fatal(err)
	}
	assertHistory(fork.ID, []string{"saved content", "later content", "saved content"})
	if body, err := afsclient.New(rdb, fork.ID).Cat(ctx, "/file"); err != nil || string(body) != "saved content" {
		t.Fatalf("fork live content = %q, %v", body, err)
	}

	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	assertActivity(meta.ID, assertHistory(meta.ID, []string{"saved content", "later content", "saved content"}))
	if body, err := c.Cat(ctx, "/file"); err != nil || string(body) != "saved content" {
		t.Fatalf("restored live content = %q, %v", body, err)
	}
}
