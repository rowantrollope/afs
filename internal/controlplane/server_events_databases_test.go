package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func databaseEventsFixture(t *testing.T) (*DatabaseHandler, *redis.Client, *redis.Client, string) {
	t.Helper()
	primary, primaryRedis := serviceFixture(t)
	_, secondaryRedis := serviceFixture(t)
	h, err := NewDatabaseHandler(primary, HandlerOptions{AuthToken: "test-secret"}, filepath.Join(t.TempDir(), "databases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	response := serverTestCall(t, h, http.MethodPost, "/v1/databases", map[string]any{
		"name": "Secondary", "redis_addr": secondaryRedis.Options().Addr,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create database HTTP %d: %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return h, primaryRedis, secondaryRedis, result["id"].(string)
}

func appendDatabaseTestEvent(t *testing.T, rdb *redis.Client, id string) {
	t.Helper()
	body, err := json.Marshal(serverEvent{Kind: "shared", Op: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: managementEventsKey, ID: id, Values: map[string]any{"event": string(body)}}).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseEventsPaginationPreservesIdenticalStreamIDs(t *testing.T) {
	h, primary, secondary, secondaryID := databaseEventsFixture(t)
	for _, rdb := range []*redis.Client{primary, secondary} {
		appendDatabaseTestEvent(t, rdb, "1000-0")
		appendDatabaseTestEvent(t, rdb, "2000-0")
	}
	for _, direction := range []string{"asc", "desc"} {
		t.Run(direction, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 4; page++ {
				endpoint := "/v1/events?kind=shared&limit=1&direction=" + direction
				if cursor != "" {
					endpoint += "&cursor=" + url.QueryEscape(cursor)
				}
				result := serverTestJSON(t, serverTestCall(t, h, http.MethodGet, endpoint, nil))
				items := result["items"].([]any)
				if len(items) != 1 {
					t.Fatalf("page %d: %+v", page, result)
				}
				item := items[0].(map[string]any)
				databaseID := item["database_id"].(string)
				if databaseID != "local" && databaseID != secondaryID {
					t.Fatalf("unexpected database %q", databaseID)
				}
				key := databaseID + "/" + item["id"].(string)
				if seen[key] {
					t.Fatalf("repeated event %q", key)
				}
				seen[key] = true
				cursor, _ = result["next_cursor"].(string)
				if (cursor == "") != (page == 3) {
					t.Fatalf("page %d unexpected cursor %q", page, cursor)
				}
			}
			if len(seen) != 4 {
				t.Fatalf("lost events: %+v", seen)
			}
		})
	}
	result := serverTestJSON(t, serverTestCall(t, h, http.MethodGet, "/v1/databases/"+secondaryID+"/events?kind=shared&limit=1", nil))
	cursor, err := decodeEventCursor(result["next_cursor"].(string))
	if err != nil || cursor.stream != managementEventsKey {
		t.Fatalf("scoped cursor compatibility: %+v %v", cursor, err)
	}
	if item := result["items"].([]any)[0].(map[string]any); item["database_id"] != secondaryID {
		t.Fatalf("scoped event came from another database: %+v", item)
	}
}

func TestDatabaseMonitorAndEventsContinueWithUnavailableBackend(t *testing.T) {
	h, primary, secondary, secondaryID := databaseEventsFixture(t)
	appendDatabaseTestEvent(t, primary, "1000-0")
	appendDatabaseTestEvent(t, secondary, "1000-0")
	root := h.root
	ctx := context.Background()
	healthy, err := root.monitorSignature(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.lookup(secondaryID).service.store.rdb.Close(); err != nil {
		t.Fatal(err)
	}
	unavailable, err := root.monitorSignature(ctx)
	if err != nil || unavailable == healthy || !strings.Contains(unavailable, secondaryID+"\x00unavailable") {
		t.Fatalf("backend availability missing: %q %v", unavailable, err)
	}
	appendDatabaseTestEvent(t, primary, "2000-0")
	changed, err := root.monitorSignature(ctx)
	if err != nil || changed == unavailable {
		t.Fatalf("unavailable backend masked healthy changes: %q %v", changed, err)
	}
	result := serverTestJSON(t, serverTestCall(t, h, http.MethodGet, "/v1/events?kind=shared", nil))
	if len(result["items"].([]any)) != 2 {
		t.Fatalf("healthy events unavailable: %+v", result)
	}
	for _, raw := range result["items"].([]any) {
		if raw.(map[string]any)["database_id"] != "local" {
			t.Fatalf("unexpected event: %+v", raw)
		}
	}
}
