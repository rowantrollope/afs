package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/managedclient"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func serverTestCall(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return historyHTTPCall(t, h, method, path, body, map[string]string{"Authorization": "Bearer test-secret"})
}
func serverTestJSON(t *testing.T, r *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestServerCoreUIContracts(t *testing.T) {
	s, rdb := serviceFixture(t)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	ctx := context.Background()
	if r := historyHTTPCall(t, h, "GET", "/v1/workspaces", nil, nil); r.Code != 401 {
		t.Fatalf("unauthenticated %d", r.Code)
	}
	if r := historyHTTPCall(t, h, "POST", "/v1/workspaces", map[string]string{"name": "bad"}, map[string]string{"Authorization": "Bearer test-secret", "Origin": "https://evil.invalid"}); r.Code != 403 {
		t.Fatalf("cross origin %d", r.Code)
	}
	created := serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/workspaces", map[string]any{"name": "browser", "description": "a tree"}))
	id := created["id"].(string)
	base := "/v1/databases/local/workspaces/" + id
	c := afsclient.New(rdb, id)
	if err := c.Echo(ctx, "/nested/file.txt", []byte("before\n")); err != nil {
		t.Fatal(err)
	}
	tree := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/tree?view=working-copy&path=/&depth=2", nil))
	if len(tree["items"].([]any)) != 2 {
		t.Fatalf("tree: %+v", tree)
	}
	cp := serverTestJSON(t, serverTestCall(t, h, "POST", base+":save-from-live", map[string]any{"checkpoint_id": "one", "description": "saved in browser", "source": "web", "allow_unchanged": true}))
	if cp["checkpoint_id"] != "one" {
		t.Fatalf("checkpoint %+v", cp)
	}
	content := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/files/content?view=checkpoint:one&path=/nested/file.txt", nil))
	if content["content"] != "before\n" || content["view"] != "checkpoint:one" || content["revision"] == "" {
		t.Fatalf("content %+v", content)
	}
	if err := c.Echo(ctx, "/nested/file.txt", []byte("after\n")); err != nil {
		t.Fatal(err)
	}
	diff := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/diff?base=checkpoint:one&head=working-copy", nil))
	if diff["summary"].(map[string]any)["updated"] != float64(1) {
		t.Fatalf("diff %+v", diff)
	}
	serverTestJSON(t, serverTestCall(t, h, "POST", base+":restore", map[string]string{"checkpoint_id": "one"}))
	content = serverTestJSON(t, serverTestCall(t, h, "GET", base+"/files/content?view=working-copy&path=/nested/file.txt", nil))
	if content["content"] != "before\n" {
		t.Fatalf("restore %+v", content)
	}
	updated := serverTestJSON(t, serverTestCall(t, h, "PUT", base, map[string]string{"name": "renamed", "description": "updated"}))
	if updated["id"] != id || updated["name"] != "renamed" {
		t.Fatalf("rename %+v", updated)
	}
	serverTestJSON(t, serverTestCall(t, h, "PUT", base+"/config", map[string]any{"versioning": map[string]string{"mode": "all"}}))
	serverTestJSON(t, serverTestCall(t, h, "POST", base+":fork", map[string]string{"new_workspace": "copy"}))
	listing := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/workspaces", nil))
	if len(listing["items"].([]any)) != 2 {
		t.Fatalf("list %+v", listing)
	}
	databases := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/databases", nil))
	db := databases["items"].([]any)[0].(map[string]any)
	if db["workspace_count"] != float64(2) || db["redis_password"] != nil {
		t.Fatalf("database %+v", db)
	}
}

func TestServerManagedClientWireContractAndDurableSessions(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	generation, _ := s.WorkspaceGeneration(ctx, meta.ID)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	input := managedclient.Registration{SessionID: "sess_stable", AgentID: "agent1", AgentName: "Test agent", SessionName: "mount", ClientKind: "sync", AFSVersion: "test", Hostname: "testhost", OS: "linux", LocalPath: "/tmp/test", Label: "friendly", User: "alice", Readonly: true, WorkspaceID: meta.ID, WorkspaceGeneration: generation}
	base := "/v1/workspaces/" + meta.ID + "/sessions"
	registration := serverTestJSON(t, serverTestCall(t, h, "POST", base, input))
	if registration["state"] != "starting" || registration["workspace_id"] != meta.ID {
		t.Fatalf("registration %+v", registration)
	}
	probe := registration["storage_probe_value"].(string)
	key := registration["storage_probe_key"].(string)
	if actual := rdb.Get(ctx, key).Val(); actual != probe || probe == "" {
		t.Fatal("probe not stored on backing Redis")
	}
	if r := serverTestCall(t, h, "POST", "/v1/sessions/sess_stable/heartbeat", input); r.Code != 400 {
		t.Fatalf("unverified heartbeat %d %s", r.Code, r.Body.String())
	}
	input.StorageProbeValue = probe
	active := serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/sessions/sess_stable/heartbeat", input))
	if active["state"] != "active" || active["storage_probe_value"] != nil {
		t.Fatalf("active %+v", active)
	}
	// New handler represents a server restart; identity remains in Redis.
	h = NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	agents := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/agents", nil))["items"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["session_id"] != "sess_stable" {
		t.Fatalf("agents %+v", agents)
	}
	registration = serverTestJSON(t, serverTestCall(t, h, "POST", base, input))
	input.StorageProbeValue = registration["storage_probe_value"].(string)
	serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/sessions/sess_stable/heartbeat", input))
	agents = serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/agents", nil))["items"].([]any)
	if len(agents) != 1 {
		t.Fatalf("duplicate retry sessions %+v", agents)
	}
	other, _ := s.CreateWorkspace(ctx, "other")
	input.WorkspaceID = other.ID
	input.WorkspaceGeneration, _ = s.WorkspaceGeneration(ctx, other.ID)
	if r := serverTestCall(t, h, "POST", "/v1/workspaces/"+other.ID+"/sessions", input); r.Code != 409 {
		t.Fatalf("cross-workspace session %d", r.Code)
	}
	stored, err := getJSON[ManagedSession](ctx, rdb, managementSessionKey("sess_stable"))
	if err != nil {
		t.Fatal(err)
	}
	stored.LeaseExpiresAt = serverTime(time.Now().Add(-time.Minute))
	if err := setJSON(ctx, rdb, managementSessionKey("sess_stable"), stored); err != nil {
		t.Fatal(err)
	}
	agents = serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/agents", nil))["items"].([]any)
	if agents[0].(map[string]any)["state"] != "stale" {
		t.Fatalf("expiry %+v", agents)
	}
	closed := serverTestJSON(t, serverTestCall(t, h, "DELETE", "/v1/sessions/sess_stable", nil))
	if closed["state"] != "closed" {
		t.Fatalf("closed %+v", closed)
	}
	if r := serverTestCall(t, h, "POST", "/v1/sessions/sess_stable/heartbeat", nil); r.Code != 404 {
		t.Fatalf("closed heartbeat %d", r.Code)
	}
}

func TestServerMergedHistoryPaginationAndDeletedWorkspace(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	a, _ := s.CreateWorkspace(ctx, "a")
	b, _ := s.CreateWorkspace(ctx, "b")
	for _, meta := range []WorkspaceMeta{a, b} {
		c := afsclient.New(rdb, meta.ID)
		for _, name := range []string{"one", "two", "three"} {
			if err := c.Echo(ctx, "/"+name, []byte(name)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "cp"); err != nil {
			t.Fatal(err)
		}
	}
	expected := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/events?limit=1000", nil))["items"].([]any)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 30; page++ {
		route := "/v1/events?limit=2"
		if cursor != "" {
			route += "&until=" + url.QueryEscape(cursor)
		}
		result := serverTestJSON(t, serverTestCall(t, h, "GET", route, nil))
		for _, raw := range result["items"].([]any) {
			item := raw.(map[string]any)
			identity := item["id"].(string) + item["workspace_id"].(string) + item["kind"].(string)
			if seen[identity] {
				t.Fatalf("duplicate %s", identity)
			}
			seen[identity] = true
		}
		cursor, _ = result["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("pagination read %d expected %d", len(seen), len(expected))
	}
	if err := s.DeleteWorkspace(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	result := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/events?kind=workspace", nil))
	found := false
	for _, raw := range result["items"].([]any) {
		item := raw.(map[string]any)
		if item["workspace_id"] == a.ID && item["op"] == "delete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deleted workspace lifecycle absent %+v", result)
	}
}

func TestServerBrowseNeverMaterializesMissingRoot(t *testing.T) {
	s, rdb := serviceFixture(t)
	meta, _ := s.CreateWorkspace(context.Background(), "missing")
	key := "afs:{" + meta.ID + "}:inode:1"
	if err := rdb.Del(context.Background(), key).Err(); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, HandlerOptions{})
	for _, suffix := range []string{"/tree?view=working-copy", "/files/content?view=working-copy&path=/file", "/diff?base=head&head=working-copy"} {
		r := historyHTTPCall(t, h, "GET", "/v1/workspaces/"+meta.ID+suffix, nil, nil)
		if r.Code != 404 {
			t.Fatalf("%s HTTP%d %s", suffix, r.Code, r.Body.String())
		}
		if n := rdb.Exists(context.Background(), key).Val(); n != 0 {
			t.Fatal("read materialized root")
		}
	}
}

func TestCheckpointCommitRejectsJournalChangesBeforeDirtyClear(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "racing")
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("first")); err != nil {
		t.Fatal(err)
	}
	changes, _ := latestServerChange(ctx, rdb, meta.ID)
	generation, _ := s.WorkspaceGeneration(ctx, meta.ID)
	m, blobs, files, dirs, size, err := BuildManifestFromWorkspaceRoot(ctx, rdb, meta.ID, "stale")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("second")); err != nil {
		t.Fatal(err)
	}
	_, err = s.saveCheckpoint(ctx, SaveCheckpointRequest{ExpectedChangesID: changes, ExpectedGeneration: generation, Workspace: meta.ID, ExpectedHead: meta.HeadSavepoint, CheckpointID: "stale", Manifest: m, Blobs: blobs, FileCount: files, DirCount: dirs, TotalBytes: size, SkipWorkspaceRootSync: true, AllowUnchanged: true})
	if !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("racing commit %v", err)
	}
	if dirty, _, err := WorkspaceRootDirtyState(ctx, s.store, meta.ID); err != nil || !dirty {
		t.Fatalf("dirty %v %v", dirty, err)
	}
	if exists := rdb.Exists(ctx, savepointMetaKey(meta.ID, "stale")).Val(); exists != 0 {
		t.Fatal("stale checkpoint committed")
	}
}

func TestServerMonitorObservesDirectEngineChanges(t *testing.T) {
	s, _ := serviceFixture(t)
	server := httptest.NewServer(NewHandler(s, HandlerOptions{AuthToken: "test-secret"}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/monitor/stream", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("SSE %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "ready") {
		t.Fatalf("ready %q %v", line, err)
	}
	if _, err := s.CreateWorkspace(ctx, "direct-cli"); err != nil {
		t.Fatal(err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "event: monitor") {
			break
		}
	}
}

func TestServerSparseHistoryFilterScansRetainedStream(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "sparse")
	stream := "afs:{" + meta.ID + "}:changes"
	pipe := rdb.Pipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"payload": `{"op":"put","paths":["/older-match"]}`}})
	for i := 0; i < 10020; i++ {
		pipe.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"payload": `{"op":"put","paths":["/noise"]}`}})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	result := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/events?path=/older-match&limit=2", nil))
	if len(result["items"].([]any)) != 1 {
		t.Fatalf("older retained match lost: %+v", result)
	}
}

func TestServerHeartbeatRejectsRetiredGeneration(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "retired")
	generation, _ := s.WorkspaceGeneration(ctx, meta.ID)
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	input := managedclient.Registration{SessionID: "sess_retired", WorkspaceID: meta.ID, WorkspaceGeneration: generation}
	registration := serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/workspaces/"+meta.ID+"/sessions", input))
	input.StorageProbeValue = registration["storage_probe_value"].(string)
	serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/sessions/sess_retired/heartbeat", input))
	if err := rdb.Set(ctx, WorkspaceGenerationKey(meta.ID), "g_replacement", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if response := serverTestCall(t, h, "POST", "/v1/sessions/sess_retired/heartbeat", input); response.Code != 409 {
		t.Fatalf("retired heartbeat HTTP%d %s", response.Code, response.Body.String())
	}
	stored, err := getJSON[ManagedSession](ctx, rdb, managementSessionKey(input.SessionID))
	if err != nil || stored.State != "stale" {
		t.Fatalf("retired state %q %v", stored.State, err)
	}
}

func TestServerUnverifiedExpiryCannotActivateWithoutStorageProof(t *testing.T) {
	for _, view := range []string{"/v1/agents", "/v1/sessions/sess_unverified"} {
		t.Run(view, func(t *testing.T) {
			s, rdb := serviceFixture(t)
			ctx := context.Background()
			meta, _ := s.CreateWorkspace(ctx, "unverified")
			h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
			input := managedclient.Registration{SessionID: "sess_unverified", WorkspaceID: meta.ID}
			serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/workspaces/"+meta.ID+"/sessions", input))
			stored, err := getJSON[ManagedSession](ctx, rdb, managementSessionKey(input.SessionID))
			if err != nil {
				t.Fatal(err)
			}
			stored.LeaseExpiresAt = serverTime(time.Now().Add(-time.Minute))
			if err := setJSON(ctx, rdb, managementSessionKey(input.SessionID), stored); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Del(ctx, managementProbeKey(input.SessionID)).Err(); err != nil {
				t.Fatal(err)
			}
			result := serverTestJSON(t, serverTestCall(t, h, "GET", view, nil))
			if view == "/v1/agents" {
				result = result["items"].([]any)[0].(map[string]any)
			}
			if result["state"] != "stale" {
				t.Fatalf("expired session: %+v", result)
			}
			if response := serverTestCall(t, h, "POST", "/v1/sessions/sess_unverified/heartbeat", input); response.Code != 404 {
				t.Fatalf("unverified expired session became active: HTTP%d %s", response.Code, response.Body.String())
			}
			stored, _ = getJSON[ManagedSession](ctx, rdb, managementSessionKey(input.SessionID))
			if stored.State == "active" || stored.StorageVerified {
				t.Fatalf("unverified presence: %+v", stored)
			}
		})
	}
}

func TestServerSessionDisplayTracksWorkspaceRename(t *testing.T) {
	s, _ := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "original")
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	input := managedclient.Registration{SessionID: "sess_rename", WorkspaceID: meta.ID}
	serverTestJSON(t, serverTestCall(t, h, "POST", "/v1/workspaces/"+meta.ID+"/sessions", input))
	serverTestJSON(t, serverTestCall(t, h, "PUT", "/v1/workspaces/"+meta.ID, map[string]string{"name": "renamed"}))
	for _, view := range []string{"/v1/agents", "/v1/sessions/sess_rename"} {
		result := serverTestJSON(t, serverTestCall(t, h, "GET", view, nil))
		if view == "/v1/agents" {
			result = result["items"].([]any)[0].(map[string]any)
		}
		if result["workspace_name"] != "renamed" || result["workspace_id"] != meta.ID {
			t.Fatalf("renamed display: %+v", result)
		}
	}
}

func TestServerChangesPreserveExactFileVersion(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "versioned")
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	base := "/v1/workspaces/" + meta.ID
	serverTestJSON(t, serverTestCall(t, h, "PUT", base+"/versioning", map[string]string{"mode": "all"}))
	if err := afsclient.New(rdb, meta.ID).Echo(ctx, "/file", []byte("historical content")); err != nil {
		t.Fatal(err)
	}
	result := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/changes?path=/file&limit=1", nil))
	entry := result["entries"].([]any)[0].(map[string]any)
	if entry["version_id"] == nil || entry["file_id"] == nil {
		t.Fatalf("version identity omitted: %+v", entry)
	}
	content := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/files/version-content?version_id="+entry["version_id"].(string), nil))
	if content["content"] != "historical content" {
		t.Fatalf("wrong historical content: %+v", content)
	}
	activity := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/activity?limit=1", nil))["items"].([]any)[0].(map[string]any)
	if activity["title"] == activity["detail"] || strings.HasPrefix(activity["detail"].(string), activity["title"].(string)+" ") {
		t.Fatalf("duplicate activity copy: %+v", activity)
	}
}

func TestServerExplicitRestoreRecoversMissingRoot(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "recoverable")
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret"})
	base := "/v1/workspaces/" + meta.ID
	serverTestJSON(t, serverTestCall(t, h, "PUT", base+"/versioning", map[string]string{"mode": "all"}))
	if err := afsclient.New(rdb, meta.ID).Echo(ctx, "/file", []byte("recover me")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Del(ctx, workspaceFSInodeKey(meta.ID, workspaceFSRootInodeID)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkspace(ctx, "unaffected"); err != nil {
		t.Fatal(err)
	}
	list := serverTestJSON(t, serverTestCall(t, h, "GET", "/v1/workspaces", nil))["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("unavailable workspace hid unaffected list: %+v", list)
	}
	for _, raw := range list {
		item := raw.(map[string]any)
		if item["id"] == meta.ID {
			if item["status"] != "unavailable" || item["live_root_available"] != false || item["file_count"] != float64(1) {
				t.Fatalf("unavailable summary: %+v", item)
			}
		} else if item["status"] == "unavailable" {
			t.Fatalf("healthy workspace unavailable: %+v", item)
		}
	}
	detail := serverTestJSON(t, serverTestCall(t, h, "GET", base, nil))
	capabilities := detail["capabilities"].(map[string]any)
	if detail["unavailable_reason"] == "" || detail["draft_state"] != "unavailable" || capabilities["browse_working_copy"] != false || capabilities["create_checkpoint"] != false || capabilities["restore_checkpoint"] != true || capabilities["browse_checkpoints"] != true {
		t.Fatalf("unavailable detail: %+v", detail)
	}
	serverTestJSON(t, serverTestCall(t, h, "GET", base+"/tree?view=checkpoint:saved", nil))
	saved := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/files/content?view=checkpoint:saved&path=/file", nil))
	if saved["content"] != "recover me" {
		t.Fatalf("checkpoint browse unavailable: %+v", saved)
	}
	if rdb.Exists(ctx, workspaceFSInodeKey(meta.ID, workspaceFSRootInodeID)).Val() != 0 {
		t.Fatal("metadata/checkpoint reads materialized root")
	}
	if response := serverTestCall(t, h, "POST", base+":save-from-live", map[string]string{"checkpoint_id": "invalid"}); response.Code != 404 {
		t.Fatalf("missing-root checkpoint HTTP%d: %s", response.Code, response.Body.String())
	}
	serverTestJSON(t, serverTestCall(t, h, "POST", base+":restore", map[string]string{"checkpoint_id": "saved"}))
	content := serverTestJSON(t, serverTestCall(t, h, "GET", base+"/files/content?view=working-copy&path=/file", nil))
	if content["content"] != "recover me" {
		t.Fatalf("recovered content: %+v", content)
	}
}

func TestServerWorkspaceSummaryDoesNotMaskRedisFailures(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, _ := s.CreateWorkspace(ctx, "failed-read")
	failure := errors.New("Redis read failed")
	rdb.AddHook(faultHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
		if cmd.Name() == "exists" {
			return failure
		}
		return next(ctx, cmd)
	}})
	h := &serverHandler{service: s, options: HandlerOptions{DatabaseID: "local"}}
	if _, err := h.workspace(ctx, meta, true); !errors.Is(err, failure) {
		t.Fatalf("Redis error was masked as missing root: %v", err)
	}
}

func TestServerMonitorRefreshesStatsAndIdenticalRecovery(t *testing.T) {
	now := time.Now()
	state := monitorRefreshState{signature: "unchanged", lastRefresh: now}
	if reason := state.update("unchanged", nil, now.Add(9*time.Second)); reason != "" {
		t.Fatalf("early refresh: %s", reason)
	}
	if reason := state.update("unchanged", nil, now.Add(10*time.Second)); reason != "databases" {
		t.Fatalf("stats not refreshed: %s", reason)
	}
	unavailable := errors.New("Redis is unavailable")
	if reason := state.update("", unavailable, now.Add(11*time.Second)); reason != "unavailable" {
		t.Fatalf("outage not reported: %s", reason)
	}
	if reason := state.update("", unavailable, now.Add(12*time.Second)); reason != "" {
		t.Fatalf("repeated outage refresh: %s", reason)
	}
	if reason := state.update("unchanged", nil, now.Add(13*time.Second)); reason != "recovered" {
		t.Fatalf("identical recovery not refreshed: %s", reason)
	}
	if reason := state.update("mutated", nil, now.Add(14*time.Second)); reason != "changed" {
		t.Fatalf("file mutation not refreshed: %s", reason)
	}
}
