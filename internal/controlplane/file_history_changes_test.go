package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func TestFileHistoryActivityWorksWithoutVersionCapture(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "activity")
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	for _, mutation := range []func() error{
		func() error { return c.Echo(ctx, "/a", []byte("body")) },
		func() error { return c.Chmod(ctx, "/a", 0600) },
		func() error { return c.Mv(ctx, "/a", "/b") },
		func() error { return c.Rm(ctx, "/b") },
	} {
		if err := mutation(); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewFileHistoryHandler(s, FileHistoryHTTPOptions{})
	base := "/v1/workspaces/activity/changes"
	all := decodeHistoryHTTP[FileHistoryChangesResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"?direction=asc", nil, nil))
	want := []string{"put", "chmod", "rename", "delete"}
	if len(all.Entries) != len(want) {
		t.Fatalf("activity includes duplicated cache notifications: %+v", all)
	}
	for i, entry := range all.Entries {
		if entry.Op != want[i] || entry.VersionID != "" || entry.Source != "mount" || entry.Origin == "" || entry.OccurredAt.IsZero() {
			t.Fatalf("disabled activity entry: %+v", entry)
		}
	}
	if n := rdb.HLen(ctx, filehistory.Prefix(meta.ID)+"records").Val(); n != 0 {
		t.Fatalf("disabled capture created %d versions", n)
	}
	first := decodeHistoryHTTP[FileHistoryChangesResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"?path=/a&direction=asc&limit=2", nil, nil))
	if len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first activity page: %+v", first)
	}
	next := decodeHistoryHTTP[FileHistoryChangesResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"?path=/a&direction=asc&since="+url.QueryEscape(first.NextCursor), nil, nil))
	if len(next.Entries) != 1 || next.Entries[0].Op != "rename" || next.Entries[0].PrevPath != "/a" || next.Entries[0].Path != "/b" {
		t.Fatalf("rename path and exclusive pagination: %+v", next)
	}
	reverse := decodeHistoryHTTP[FileHistoryChangesResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"?direction=desc&until="+url.QueryEscape(all.Entries[2].ID), nil, nil))
	if len(reverse.Entries) != 2 || reverse.Entries[0].Op != "chmod" || reverse.Entries[1].Op != "put" {
		t.Fatalf("reverse exclusive activity: %+v", reverse)
	}
	for _, query := range []string{"?cursor=invalid", "?since=(12-0", "?until=+", "?cursor=1-0&since=2-0", "?path=../bad"} {
		if response := historyHTTPCall(t, handler, http.MethodGet, base+query, nil, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid activity query %s: %d %s", query, response.Code, response.Body.String())
		}
	}
}

func TestFileHistoryActivitySparsePaginationAndLegacyFallback(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "sparse-activity")
	if err != nil {
		t.Fatal(err)
	}
	stream := "afs-lite:{" + meta.ID + "}:changes"
	pipe := rdb.Pipeline()
	for i := 1; i <= 4200; i++ {
		payload := map[string]any{"op": "content", "paths": []string{"/other"}, "origin": "legacy"}
		if i == 4199 {
			payload["paths"] = []string{"/old", "/selected"}
			payload["op"] = "prefix"
		}
		if i == 4200 {
			payload["activity"] = false
			payload["change"] = FileHistoryChange{Op: "chmod", Path: "/selected", SessionID: "review", Mode: 0600}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		pipe.XAdd(ctx, &redis.XAddArgs{Stream: stream, ID: fmt.Sprintf("%d-0", i), Values: map[string]any{"payload": string(body)}})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Path: "/selected", Limit: 10})
	if err != nil || len(page.Entries) != 0 || page.NextCursor != "4096-0" {
		t.Fatalf("bounded scan must advance across unmatched rows: %+v %v", page, err)
	}
	page, err = s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Path: "/selected", Cursor: page.NextCursor, Limit: 10})
	if err != nil || len(page.Entries) != 2 || page.NextCursor != "" {
		t.Fatalf("matching rows after sparse cursor: %+v %v", page, err)
	}
	if page.Entries[0].Op != "prefix" || page.Entries[0].PrevPath != "/old" || page.Entries[0].Origin != "legacy" || page.Entries[1].Op != "chmod" {
		t.Fatalf("legacy and structured metadata: %+v", page)
	}
	page, err = s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Path: "/selected", SessionID: "review", NewestFirst: true, Limit: 1})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != "4200-0" {
		t.Fatalf("session filter: %+v %v", page, err)
	}
}
