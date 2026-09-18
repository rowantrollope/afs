package controlplane

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLegacyRedisNamespaceDiscovery(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	service := NewService(NewStore(rdb))

	// These are literal records from the old schema, deliberately not encoded
	// through the current key builders or the smaller WorkspaceMeta struct.
	strings := map[string]string{
		"afs:{legacy-local}:workspace:meta":            `{"version":1,"name":"legacy-local","description":"Name-keyed local tree","created_at":"2026-05-01T12:00:00Z","updated_at":"2026-05-02T12:00:00Z","head_savepoint":"saved-local","default_savepoint":"initial","dirty_hint":true}`,
		"afs:{ws_legacy}:workspace:meta":               `{"version":1,"id":"ws_legacy","name":"legacy-volume","description":"Opaque-ID volume","database_id":"db_old","database_name":"Old Redis","cloud_account":"old-account","region":"old-region","source":"import","tags":["retained"],"created_at":"2026-05-01T12:00:00Z","updated_at":"2026-05-02T12:00:00Z","head_savepoint":"saved-volume","default_savepoint":"initial","dirty_hint":false}`,
		"afs:{ws_composed}:workspace:composition:meta": `{"version":1,"id":"ws_composed","name":"agent-workspace","mounts":[{"volume_id":"ws_legacy","mount_path":"/shared","readonly":true,"volume_token_id":"old-token"}]}`,
		"afs-lite:{retired}:workspace:meta":            `{"version":1,"id":"retired","name":"retired-namespace"}`,
		"afs:{ws_legacy}:content:2":                    "uncheckpointed live content",
	}
	for key, value := range strings {
		if err := rdb.Set(ctx, key, value, 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	indexes := map[string]map[string]string{
		"afs:workspace:index:names":             {"legacy-volume": "ws_legacy"},
		"afs:workspace-composition:index:names": {"agent-workspace": "ws_composed"},
		"afs-lite:workspace:index:names":        {"retired-namespace": "retired"},
	}
	for key, values := range indexes {
		if err := rdb.HSet(ctx, key, values).Err(); err != nil {
			t.Fatal(err)
		}
	}
	beforeKeys := mr.Keys()

	listed, err := service.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "legacy-local" || listed[1].Name != "legacy-volume" {
		t.Fatalf("legacy trees = %+v; want local and volume only", listed)
	}
	for _, tc := range []struct {
		ref, name, id, head string
		dirty               bool
	}{
		{ref: "legacy-local", name: "legacy-local", id: "legacy-local", head: "saved-local", dirty: true},
		{ref: "legacy-volume", name: "legacy-volume", id: "ws_legacy", head: "saved-volume"},
		{ref: "ws_legacy", name: "legacy-volume", id: "ws_legacy", head: "saved-volume"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			meta, err := service.GetWorkspace(ctx, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Name != tc.name || WorkspaceStorageID(meta) != tc.id || meta.HeadSavepoint != tc.head || meta.DirtyHint != tc.dirty || meta.Version != 1 {
				t.Fatalf("decoded legacy workspace = %+v", meta)
			}
		})
	}
	for _, ref := range []string{"agent-workspace", "ws_composed", "retired-namespace", "retired"} {
		if _, err := service.GetWorkspace(ctx, ref); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("GetWorkspace(%q) = %v; want not found", ref, err)
		}
	}

	// Discovery must not adopt the old write protocol, rewrite old metadata
	// (which contains fields absent from WorkspaceMeta), or materialize files.
	if afterKeys := mr.Keys(); !reflect.DeepEqual(afterKeys, beforeKeys) {
		t.Fatalf("discovery changed keys: before %v, after %v", beforeKeys, afterKeys)
	}
	for key, want := range strings {
		got, err := rdb.Get(ctx, key).Result()
		if err != nil || got != want {
			t.Errorf("discovery changed %s: %q, %v", key, got, err)
		}
	}
	for key, want := range indexes {
		got, err := rdb.HGetAll(ctx, key).Result()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("discovery changed index %s: %v, %v", key, got, err)
		}
	}
}

func TestLegacyRedisWorkspaceMissingGenerationIsNotAdopted(t *testing.T) {
	service, rdb := serviceFixture(t)
	ctx := context.Background()
	const metadata = `{"version":1,"id":"ws_legacy","name":"legacy-volume","head_savepoint":"initial","default_savepoint":"initial"}`
	if err := rdb.Set(ctx, "afs:{ws_legacy}:workspace:meta", metadata, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetWorkspace(ctx, "ws_legacy"); err != nil {
		t.Fatalf("legacy metadata should be readable: %v", err)
	}
	if generation, err := service.WorkspaceGeneration(ctx, "ws_legacy"); !errors.Is(err, redis.Nil) || generation != "" {
		t.Fatalf("legacy generation = %q, %v; want missing generation", generation, err)
	}
	if exists, err := rdb.Exists(ctx, "afs:{ws_legacy}:generation").Result(); err != nil || exists != 0 {
		t.Fatalf("legacy workspace was silently adopted: generation exists = %d, %v", exists, err)
	}
	if got, err := rdb.Get(ctx, "afs:{ws_legacy}:workspace:meta").Result(); err != nil || got != metadata {
		t.Fatalf("generation lookup changed legacy metadata: %q, %v", got, err)
	}
}
