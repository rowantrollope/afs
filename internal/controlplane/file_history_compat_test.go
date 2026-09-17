package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func historyHTTPCall(t *testing.T, handler http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func decodeHistoryHTTP[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFileHistoryOriginalUIHTTPContractsAndAtomicActions(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "ui")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewFileHistoryHandler(s, FileHistoryHTTPOptions{DatabaseID: "selected", AllowedOrigins: []string{"http://localhost:5173"}})
	base := "/v1/databases/selected/workspaces/ui"
	policy := decodeHistoryHTTP[WorkspaceVersioningPolicy](t, historyHTTPCall(t, handler, http.MethodPut, base+"/versioning", map[string]any{
		"mode": "all", "max_versions_per_file": 100, "max_total_bytes": 1048576,
	}, nil))
	if policy.Mode != "all" || policy.MaxVersionsPerFile != 100 {
		t.Fatalf("policy contract: %+v", policy)
	}
	c := afsclient.New(rdb, meta.ID)
	if err := c.EchoCreate(ctx, "/file.txt", []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "old"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file.txt", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "new"); err != nil {
		t.Fatal(err)
	}
	first := decodeHistoryHTTP[FileHistoryResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/history?path=/file.txt&direction=asc&limit=1", nil, nil))
	if len(first.Lineages) != 1 || len(first.Lineages[0].Versions) != 1 || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	version := first.Lineages[0].Versions[0]
	if version.Ordinal != 1 || version.Kind != "file" || version.Op != "put" || version.ContentHash == "" || version.CreatedAt.IsZero() {
		t.Fatalf("version contract: %+v", version)
	}
	second := decodeHistoryHTTP[FileHistoryResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/history?path=/file.txt&direction=asc&limit=1&cursor="+url.QueryEscape(first.NextCursor), nil, nil))
	if len(second.Lineages) != 1 || second.Lineages[0].Versions[0].Ordinal != 2 {
		t.Fatalf("second page: %+v", second)
	}
	content := decodeHistoryHTTP[FileVersionContentResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/version-content?path=/file.txt&file_id="+version.FileID+"&ordinal=1", nil, nil))
	if content.Content != "old\n" || content.Encoding != "utf-8" || content.VersionID != version.VersionID {
		t.Fatalf("content contract: %+v", content)
	}
	if _, err := time.Parse(time.RFC3339Nano, content.CreatedAt); err != nil {
		t.Fatal(err)
	}
	diff := decodeHistoryHTTP[FileVersionDiffResponse](t, historyHTTPCall(t, handler, http.MethodPost, base+"/files/diff", map[string]any{
		"path": "/file.txt", "from": map[string]any{"file_id": version.FileID, "ordinal": 1}, "to": map[string]any{"ref": "head"},
	}, nil))
	if diff.Binary || !strings.Contains(diff.Diff, "-old") || !strings.Contains(diff.Diff, "+new") {
		t.Fatalf("diff contract: %+v", diff)
	}
	checkpoints := decodeHistoryHTTP[FileVersionCheckpointsResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/version-checkpoints?version_id="+version.VersionID, nil, nil))
	if len(checkpoints.Checkpoints) != 1 || checkpoints.Checkpoints[0].ID != "old" {
		t.Fatalf("checkpoint membership: %+v", checkpoints)
	}
	restored := decodeHistoryHTTP[FileVersionRestoreResponse](t, historyHTTPCall(t, handler, http.MethodPost, base+":restore-version", map[string]any{
		"path": "/file.txt", "file_id": version.FileID, "ordinal": 1,
	}, map[string]string{"X-AFS-Agent-ID": "review-agent", "X-AFS-Session-ID": "review-session", "X-AFS-User": "review-user"}))
	if restored.RestoredFromVersionID != version.VersionID || restored.FileID != version.FileID || restored.VersionID == "" || !restored.Dirty {
		t.Fatalf("restore contract: %+v", restored)
	}
	if body, err := c.Cat(ctx, "/file.txt"); err != nil || string(body) != "old\n" {
		t.Fatalf("restored body %q %v", body, err)
	}
	restoredRecord, err := filehistory.RecordByID(ctx, rdb, meta.ID, restored.VersionID)
	if err != nil || restoredRecord.Source != "version_restore" || restoredRecord.AgentID != "review-agent" || restoredRecord.User != "review-user" {
		t.Fatalf("restore attribution: %+v %v", restoredRecord, err)
	}
	conflict := historyHTTPCall(t, handler, http.MethodPost, base+":undelete", map[string]any{"path": "/file.txt"}, nil)
	if conflict.Code == http.StatusOK {
		t.Fatal("undelete replaced a live path")
	}
	if err := c.Rm(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	undeleted := decodeHistoryHTTP[FileVersionUndeleteResponse](t, historyHTTPCall(t, handler, http.MethodPost, "/v1/workspaces/ui:undelete", map[string]any{"path": "/file.txt"}, nil))
	if undeleted.FileID != version.FileID || undeleted.UndeletedFromVersionID == "" || undeleted.VersionID == "" {
		t.Fatalf("undelete contract: %+v", undeleted)
	}
	activity := decodeHistoryHTTP[FileHistoryChangesResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/changes?path=/file.txt&direction=desc&limit=25", nil, nil))
	found := false
	for _, entry := range activity.Entries {
		if entry.VersionID == restored.VersionID && entry.AgentID == "review-agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity omitted attribution: %+v", activity)
	}
}

func TestFileHistoryHTTPBinaryMetadataOnlyAndRequestBoundaries(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "boundaries")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	binary := []byte{0, 255, 1, 128}
	if err := c.Echo(ctx, "/binary", binary); err != nil {
		t.Fatal(err)
	}
	record := historyPage(t, rdb, meta.ID, "/binary").Versions[0]
	handler := NewFileHistoryHandler(s, FileHistoryHTTPOptions{})
	base := "/v1/workspaces/boundaries"
	content := decodeHistoryHTTP[FileVersionContentResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/version-content?version_id="+record.ID, nil, nil))
	decoded, err := base64.StdEncoding.DecodeString(content.DataBase64)
	if err != nil || !bytes.Equal(decoded, binary) || !content.Binary {
		t.Fatalf("binary response: %+v %v", content, err)
	}
	if err := filehistory.SetPolicy(ctx, rdb, meta.ID, filehistory.Policy{Mode: "all", MaxFileBytes: 2}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/binary", []byte("oversized metadata")); err != nil {
		t.Fatal(err)
	}
	metadata := historyPage(t, rdb, meta.ID, "/binary").Versions[0]
	content = decodeHistoryHTTP[FileVersionContentResponse](t, historyHTTPCall(t, handler, http.MethodGet, base+"/files/version-content?version_id="+metadata.ID, nil, nil))
	if !content.MetadataOnly || content.Content != "" || content.DataBase64 != "" {
		t.Fatalf("metadata-only content: %+v", content)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		headers      map[string]string
		status       int
	}{
		{http.MethodGet, base + "/files/history?path=/binary&direction=invalid", nil, nil, 400},
		{http.MethodGet, base + "/files/history?path=/binary&limit=-1", nil, nil, 400},
		{http.MethodGet, base + "/files/history?path=../outside", nil, nil, 400},
		{http.MethodGet, base + "/files/history?path=/binary", nil, map[string]string{"Origin": "https://untrusted.example"}, 403},
		{http.MethodPut, base + "/files/history?path=/binary", nil, nil, 405},
		{http.MethodGet, "/v1/databases/other/workspaces/boundaries/files/history?path=/binary", nil, nil, 404},
		{http.MethodGet, base + "/files/content?path=/absent&view=working-copy", nil, nil, 404},
		{http.MethodPost, base + ":restore-version", map[string]any{"path": "/binary", "version_id": metadata.ID}, nil, 400},
		{http.MethodPost, base + ":restore-version", map[string]any{"path": "/binary", "version_id": record.ID, "unexpected": true}, nil, 400},
	} {
		response := historyHTTPCall(t, handler, tc.method, tc.path, tc.body, tc.headers)
		if response.Code != tc.status {
			t.Fatalf("%s %s status %d want %d: %s", tc.method, tc.path, response.Code, tc.status, response.Body.String())
		}
	}
}

func TestFileHistoryDiffHeadFallsBackToWorkingCopy(t *testing.T) {
	for _, existingCheckpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_checkpoint", true: "checkpoint_lacks_path"}[existingCheckpoint], func(t *testing.T) {
			s, rdb := serviceFixture(t)
			ctx := context.Background()
			meta, err := s.CreateWorkspace(ctx, "head-fallback")
			if err != nil {
				t.Fatal(err)
			}
			if existingCheckpoint {
				if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "empty"); err != nil {
					t.Fatal(err)
				}
			}
			enableWorkspaceHistory(t, rdb, meta.ID)
			c := afsclient.New(rdb, meta.ID)
			if err := c.Echo(ctx, "/later.txt", []byte("first\n")); err != nil {
				t.Fatal(err)
			}
			first := historyPage(t, rdb, meta.ID, "/later.txt").Versions[0]
			if err := c.Echo(ctx, "/later.txt", []byte("working\n")); err != nil {
				t.Fatal(err)
			}
			handler := NewFileHistoryHandler(s, FileHistoryHTTPOptions{})
			base := "/v1/workspaces/head-fallback"
			missingHead := historyHTTPCall(t, handler, http.MethodGet, base+"/files/content?path=/later.txt&view=head", nil, nil)
			if missingHead.Code != http.StatusNotFound {
				t.Fatalf("missing checkpoint file HTTP %d: %s", missingHead.Code, missingHead.Body.String())
			}
			diff := decodeHistoryHTTP[FileVersionDiffResponse](t, historyHTTPCall(t, handler, http.MethodPost, base+"/files/diff", map[string]any{
				"path": "/later.txt", "from": map[string]any{"version_id": first.ID}, "to": map[string]any{"ref": "head"},
			}, nil))
			if diff.To != "head" || diff.Binary || !strings.Contains(diff.Diff, "-first") || !strings.Contains(diff.Diff, "+working") {
				t.Fatalf("head diff fallback: %+v", diff)
			}
		})
	}
}

func TestFileHistoryPolicyResponseIdentifiesCommittedWrite(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "policy-response")
	if err != nil {
		t.Fatal(err)
	}
	var raced atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		isUpdate := false
		for _, cmd := range cmds {
			if cmd.Name() == "set" && len(cmd.Args()) > 1 && cmd.Args()[1] == filehistory.Prefix(meta.ID)+"policy" {
				isUpdate = true
			}
		}
		err := next(ctx, cmds)
		if err == nil && isUpdate && raced.CompareAndSwap(false, true) {
			if err := filehistory.SetPolicy(ctx, rdb, meta.ID, filehistory.Policy{Mode: "off"}); err != nil {
				return err
			}
		}
		return err
	}})
	committed, err := s.UpdateWorkspaceVersioningPolicy(ctx, meta.ID, WorkspaceVersioningPolicy{Mode: "all"})
	if err != nil || committed.Mode != "all" || !raced.Load() {
		t.Fatalf("committed response %+v %v", committed, err)
	}
	current, err := s.GetWorkspaceVersioningPolicy(ctx, meta.ID)
	if err != nil || current.Mode != "off" {
		t.Fatalf("concurrent policy %+v %v", current, err)
	}
	response := httptest.NewRecorder()
	historyHTTPError(response, afsclient.ErrWorkspaceChanged)
	if response.Code != http.StatusConflict {
		t.Fatalf("generation conflict HTTP %d", response.Code)
	}
}

func TestFileHistoryRestoreAcceptsRenamedLineageAndRejectsOtherPath(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "restore-alias")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/old", []byte("first")); err != nil {
		t.Fatal(err)
	}
	first := historyPage(t, rdb, meta.ID, "/old").Versions[0]
	if err := c.Rename(ctx, "/old", "/new", 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/new", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreFileVersion(ctx, meta.ID, "/new", FileVersionSelector{VersionID: first.ID}); err != nil {
		t.Fatal(err)
	}
	if body, err := c.Cat(ctx, "/new"); err != nil || string(body) != "first" {
		t.Fatalf("renamed restore: %q %v", body, err)
	}
	if _, err := s.RestoreFileVersion(ctx, meta.ID, "/unrelated", FileVersionSelector{VersionID: first.ID}); err == nil {
		t.Fatal("restored into unrelated absent path")
	}
}
