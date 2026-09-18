package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
	"github.com/rowantrollope/afs/internal/managedclient"
)

func TestManagedCLIListsThroughHTTPWithoutRedis(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("management token missing")
		}
		var request struct {
			Operation string `json:"operation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Operation != "workspace.list" {
			t.Errorf("unexpected management request: %+v %v", request, err)
		}
		_ = json.NewEncoder(w).Encode([]controlplane.WorkspaceMeta{{ID: "ws_managed", Name: "managed"}})
	}))
	defer server.Close()
	a := &app{config: config{Redis: "redis://127.0.0.1:1/0", ControlPlane: &managedclient.Settings{URL: server.URL, Token: "test-token"}}}
	out, err := captureStdout(t, func() error { return a.workspace([]string{"list"}) })
	if err != nil {
		t.Fatal(err)
	}
	if a.rdb != nil || requests != 1 {
		t.Fatalf("managed list opened Redis or sent unexpected requests: redis=%v requests=%d", a.rdb != nil, requests)
	}
	if !strings.Contains(out, "CONTROL PLANE: "+server.URL) || !strings.Contains(out, "managed") || strings.Contains(out, "REDIS:") || strings.Contains(out, "test-token") {
		t.Fatalf("incorrect managed context: %s", out)
	}
}

func TestManagedCLIRejectsAuthenticationWithoutRedisFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
	}))
	defer server.Close()
	a := &app{config: config{Redis: "redis://127.0.0.1:1/0", ControlPlane: &managedclient.Settings{URL: server.URL, Token: "private-token"}}, options: cliOptions{json: true}}
	out, err := captureStdout(t, func() error { return a.workspace([]string{"list"}) })
	if err == nil || a.rdb != nil {
		t.Fatalf("authentication failure fell back to Redis: %v", err)
	}
	if out != "" || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "Cannot connect to Redis") {
		t.Fatalf("incorrect authentication error/output: %q %v", out, err)
	}
}

func TestExplicitRedisKeepsCLIStandalone(t *testing.T) {
	redisServer := miniredis.RunT(t)
	managementRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managementRequests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	redisURL := "redis://" + redisServer.Addr()
	a := &app{config: config{Redis: redisURL, ControlPlane: &managedclient.Settings{URL: server.URL}}, options: cliOptions{redisURL: redisURL}}
	defer func() {
		if a.rdb != nil {
			_ = a.rdb.Close()
		}
	}()
	out, err := captureStdout(t, func() error { return a.workspace([]string{"list"}) })
	if err != nil {
		t.Fatal(err)
	}
	if a.rdb == nil || managementRequests != 0 || !strings.Contains(out, "REDIS:") || strings.Contains(out, "CONTROL PLANE:") {
		t.Fatalf("explicit Redis did not select standalone: redis=%v requests=%d output=%s", a.rdb != nil, managementRequests, out)
	}
}

func TestManagedHistoryListPreservesLiveLookupFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no file history","code":"not_found"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
	}))
	defer server.Close()
	a := &app{config: config{ControlPlane: &managedclient.Settings{URL: server.URL}}, options: cliOptions{json: true}}
	_, err := captureStdout(t, func() error {
		return a.historyCommand([]string{"list", "managed", "file"})
	})
	if err == nil || !strings.Contains(err.Error(), "authentication") || requests != 2 {
		t.Fatalf("live lookup failure was lost: %v; requests=%d", err, requests)
	}
}

func TestManagedDestructiveGuardFindsStandaloneMountWithoutRedis(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/connection" {
			_ = json.NewEncoder(w).Encode(controlplane.ConnectionInfo{RedisURL: "redis://new-user:new-password@127.0.0.1:1/3"})
			return
		}
		_ = json.NewEncoder(w).Encode(controlplane.WorkspaceMeta{ID: "ws_managed", Name: "managed"})
	}))
	defer server.Close()
	standalone := config{Redis: "redis://old-user:old-password@127.0.0.1:1/3"}
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{{WorkspaceID: "ws_managed", LocalPath: "/local/standalone-mount", RedisIdentity: redisIdentity(standalone)}}}); err != nil {
		t.Fatal(err)
	}
	a := &app{config: config{Redis: "redis://127.0.0.1:1/0", ControlPlane: &managedclient.Settings{URL: server.URL}}, options: cliOptions{json: true}}
	if err := a.connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := a.requireUnmounted(context.Background(), "managed")
	if err == nil || !strings.Contains(err.Error(), "/local/standalone-mount") || a.rdb != nil {
		t.Fatalf("managed operation missed standalone mount: %v; redis=%v", err, a.rdb != nil)
	}
}

func TestManagedHistoryPolicyAndExportUseHTTP(t *testing.T) {
	pruneCalls := 0
	var patch controlplane.FileHistoryPolicyPatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Operation string                              `json:"operation"`
			Workspace string                              `json:"workspace"`
			Path      string                              `json:"path"`
			Ref       string                              `json:"ref"`
			FileID    string                              `json:"file_id"`
			Limit     int                                 `json:"limit"`
			Patch     controlplane.FileHistoryPolicyPatch `json:"policy_patch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Workspace != "managed" {
			t.Errorf("workspace=%q", request.Workspace)
		}
		var result any
		switch request.Operation {
		case "workspace.get":
			result = controlplane.WorkspaceMeta{ID: "ws_managed", Name: "managed"}
		case "history.policy.update":
			patch = request.Patch
			result = filehistory.Policy{Mode: "all", MaxVersions: 5}
		case "history.prune":
			pruneCalls++
			if request.Limit != 100 {
				t.Errorf("unbounded prune batch: %d", request.Limit)
			}
			result = filehistory.PruneResult{Removed: 2, More: pruneCalls == 1}
		case "history.export":
			if request.Path != "/binary" || request.Ref != "3" || request.FileID != "f_old" {
				t.Errorf("export selector changed: %+v", request)
			}
			result = map[string]any{"record": filehistory.Record{ID: "v_test", Type: "file", ContentRef: "body", Mode: 0o751}, "data": []byte{0, 255, 128, 10}}
		default:
			t.Errorf("unexpected operation %q", request.Operation)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	a := &app{config: config{Redis: "redis://127.0.0.1:1/0", ControlPlane: &managedclient.Settings{URL: server.URL}}, options: cliOptions{json: true}}
	out, err := captureStdout(t, func() error {
		return a.historyCommand([]string{"policy", "managed", "--mode", "all", "--include=", "--max-bytes", "0", "--prune"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if patch.Mode == nil || *patch.Mode != "all" || patch.Include == nil || len(*patch.Include) != 0 || patch.MaxBytes == nil || *patch.MaxBytes != 0 || patch.MaxVersions != nil || pruneCalls != 2 {
		t.Fatalf("partial policy patch/clear/prune lost: %+v; prune calls=%d", patch, pruneCalls)
	}
	if !strings.Contains(out, `"pruned":4`) {
		t.Fatalf("pruning totals changed: %s", out)
	}
	destination := filepath.Join(t.TempDir(), "binary")
	_, err = captureStdout(t, func() error {
		return a.historyCommand([]string{"export", "managed", "binary", "--version", "3", "--file-id", "f_old", "--to", destination})
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != string([]byte{0, 255, 128, 10}) {
		t.Fatalf("export bytes: %v %v", content, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o751 || a.rdb != nil {
		t.Fatalf("export mode/connection: %v %v; redis=%v", info, err, a.rdb != nil)
	}
}
