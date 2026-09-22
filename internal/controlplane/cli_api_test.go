package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	afsclient "github.com/rowantrollope/afs/mount/client"
)

func TestCLIBootstrapAuthenticationAndDisclosure(t *testing.T) {
	s, _ := serviceFixture(t)
	secret := "redis://default:redis-password@redis.internal:6379/4"
	h := NewHandler(s, HandlerOptions{AuthToken: "test-secret", RedisURL: secret})
	for _, path := range []string{"/v1/connection", "/v1/cli", "/v1/cli/import"} {
		response := historyHTTPCall(t, h, "GET", path, nil, nil)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "redis-password") {
			t.Fatalf("unauthenticated endpoint %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := serverTestCall(t, h, "GET", "/v1/connection", nil)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("bootstrap response must not be cached")
	}
	var connection ConnectionInfo
	if err := json.Unmarshal(response.Body.Bytes(), &connection); err != nil || connection.RedisURL != secret || connection.DatabaseID != "" {
		t.Fatalf("bootstrap mismatch: %v", err)
	}
	for _, path := range []string{"/v1/auth/config", "/v1/version", "/v1/databases", "/v1/workspaces", "/v1/agents"} {
		response := serverTestCall(t, h, "GET", path, nil)
		if response.Code != 200 || strings.Contains(response.Body.String(), "redis-password") || strings.Contains(response.Body.String(), "redis_url") {
			t.Fatalf("credential disclosure at %s", path)
		}
	}
	if response := serverTestCall(t, NewHandler(s, HandlerOptions{AuthToken: "test-secret"}), "GET", "/v1/connection", nil); response.Code != http.StatusNotFound {
		t.Fatal("unconfigured bootstrap must be unavailable")
	}
	if response := serverTestCall(t, h, "POST", "/v1/cli", cliRequest{Operation: "arbitrary.Redis.Command"}); response.Code != http.StatusBadRequest {
		t.Fatal("unrecognized operation accepted")
	}
	response = historyHTTPCall(t, h, "GET", "/v1/connection", nil, map[string]string{"Authorization": "Bearer test-secret", "Origin": "https://other.example"})
	if response.Code != http.StatusForbidden {
		t.Fatal("bootstrap bypasses origin guard")
	}
}

func TestCLIBootstrapAdvertisesSelectedDatabaseIdentity(t *testing.T) {
	metadata := metadataTestStore(t)
	handler, err := NewMetadataDatabaseHandler(metadata, HandlerOptions{AuthToken: "test-secret"}, "", "redis://127.0.0.1:1/0")
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	for _, path := range []string{"/v1/connection", "/databases/local/v1/connection"} {
		response := serverTestCall(t, handler, http.MethodGet, path, nil)
		var connection ConnectionInfo
		if err := json.Unmarshal(response.Body.Bytes(), &connection); err != nil || response.Code != http.StatusOK || connection.DatabaseID != "local" {
			t.Fatalf("bootstrap omitted database identity at %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestCLIClientManagementUsesRetainedEngine(t *testing.T) {
	s, rdb := serviceFixture(t)
	server := httptest.NewServer(NewHandler(s, HandlerOptions{AuthToken: "test-secret", RedisURL: "redis://example:6379/0"}))
	defer server.Close()
	client, err := NewCLIClient(server.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	meta, err := client.CreateWorkspace(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetWorkspace(ctx, "managed")
	if err != nil || !reflect.DeepEqual(meta, stored) {
		t.Fatalf("engine mismatch: %v", err)
	}
	files := afsclient.New(rdb, meta.ID)
	if err := files.Echo(ctx, "/note", []byte("checkpoint bytes")); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := client.SaveCheckpointFromLive(ctx, "managed", "saved")
	if err != nil {
		t.Fatal(err)
	}
	fetched, manifest, err := client.GetCheckpoint(ctx, "managed", checkpoint.ID)
	if err != nil || !reflect.DeepEqual(checkpoint, fetched) || manifest.Entries["/note"].Type != "file" {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := files.Echo(ctx, "/note", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RestoreCheckpoint(ctx, "managed", "saved"); err != nil {
		t.Fatal(err)
	}
	body, err := files.Cat(ctx, "/note")
	if err != nil || string(body) != "checkpoint bytes" {
		t.Fatalf("restored %q: %v", body, err)
	}
	if err := client.ForkWorkspace(ctx, "managed", "copy", "saved"); err != nil {
		t.Fatal(err)
	}
	workspaces, err := client.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("workspace listing: %v", err)
	}
	generation, err := client.WorkspaceGeneration(ctx, "copy")
	if err != nil || generation == "" {
		t.Fatalf("generation: %v", err)
	}
	if err := client.DeleteWorkspace(ctx, "copy"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetWorkspace(ctx, "copy"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing error identity: %v", err)
	}
	mode, versions, bytesLimit := "all", 8, int64(4096)
	if _, err := client.UpdateFileHistoryPolicy(ctx, "managed", FileHistoryPolicyPatch{Mode: &mode, MaxVersions: &versions}); err != nil {
		t.Fatal(err)
	}
	policy, err := client.UpdateFileHistoryPolicy(ctx, "managed", FileHistoryPolicyPatch{MaxBytes: &bytesLimit})
	if err != nil || policy.Mode != "all" || policy.MaxVersions != 8 || policy.MaxBytes != 4096 {
		t.Fatalf("patch lost other fields: %+v %v", policy, err)
	}
	zero := 0
	policy, err = client.UpdateFileHistoryPolicy(ctx, "managed", FileHistoryPolicyPatch{MaxVersions: &zero})
	if err != nil || policy.MaxVersions != 0 || policy.MaxBytes != 4096 {
		t.Fatalf("explicit zero patch: %+v %v", policy, err)
	}
}

func TestCLIClientDoesNotFollowRedirectsOrDiscloseToken(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	client, err := NewCLIClient(server.URL, "very-private-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connection(context.Background()); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect: %v", err)
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target received a request")
	}
	server.Close()
	if _, err := client.Connection(context.Background()); err == nil || strings.Contains(err.Error(), "very-private-token") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("transport error disclosure: %v", err)
	}
	for _, invalid := range []string{"http://user:secret@example.com", "http://example.com?secret=value", "ftp://example.com", "http://example.com/#fragment"} {
		if _, err := NewCLIClient(invalid, "token"); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid URL disclosure: %v", err)
		}
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverJSON(w, 400, map[string]string{"error": "invalid very-private-token"})
	}))
	defer server.Close()
	client, _ = NewCLIClient(server.URL, "very-private-token")
	if _, err := client.Connection(context.Background()); err == nil || strings.Contains(err.Error(), "very-private-token") {
		t.Fatalf("response token disclosure: %v", err)
	}
}

func TestCLIImportStreamsBodiesAndDoesNotPublishFailures(t *testing.T) {
	s, rdb := serviceFixture(t)
	server := httptest.NewServer(NewHandler(s, HandlerOptions{AuthToken: "test-secret"}))
	defer server.Close()
	client, _ := NewCLIClient(server.URL, "test-secret")
	ctx := context.Background()
	body := bytes.Repeat([]byte("streamed-body"), 1<<20) // Exceeds a Redis write batch.
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	manifest := Manifest{Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0750}, "/large": {Type: "file", Mode: 0640, BlobID: id, Size: int64(len(body))}}}
	meta, err := client.ImportWorkspace(ctx, "streamed", func(sink BlobSink) (Manifest, error) {
		if err := sink.Submit(ctx, id, body, int64(len(body))); err != nil {
			return Manifest{}, err
		}
		if _, err := s.GetWorkspace(ctx, "streamed"); !errors.Is(err, os.ErrNotExist) {
			return Manifest{}, errors.New("import published before manifest completion")
		}
		return manifest, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := afsclient.New(rdb, meta.ID).Cat(ctx, "/large")
	if err != nil || !bytes.Equal(read, body) {
		t.Fatalf("streamed content differs: %v", err)
	}
	failure := errors.New("source file changed while scanning")
	_, err = client.ImportWorkspace(ctx, "aborted", func(sink BlobSink) (Manifest, error) {
		if err := sink.Submit(ctx, id, body, int64(len(body))); err != nil {
			return Manifest{}, err
		}
		return Manifest{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("builder error lost: %v", err)
	}
	if _, err := s.GetWorkspace(ctx, "aborted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aborted import published")
	}
	_, err = client.ImportWorkspace(ctx, "missing", func(BlobSink) (Manifest, error) { return manifest, nil })
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing blob accepted: %v", err)
	}
	if _, err := s.GetWorkspace(ctx, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing blob import published")
	}
	_, err = client.ImportWorkspace(ctx, "bad-hash", func(sink BlobSink) (Manifest, error) {
		err := sink.Submit(ctx, strings.Repeat("a", 64), body, int64(len(body)))
		return manifest, err
	})
	if err == nil {
		t.Fatal("invalid blob hash accepted")
	}
	if _, err := s.GetWorkspace(ctx, "bad-hash"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bad hash import published")
	}
}

func TestCLIErrorRedactsServerCredentials(t *testing.T) {
	h := &serverHandler{options: HandlerOptions{AuthToken: "team-token", RedisURL: "redis://default:redis-password@host:6379/0"}}
	response := httptest.NewRecorder()
	h.cliError(response, errors.New("failed team-token redis-password redis://default:redis-password@host:6379/0"))
	body, _ := io.ReadAll(response.Result().Body)
	if strings.Contains(string(body), "team-token") || strings.Contains(string(body), "redis-password") {
		t.Fatal("management error disclosed a credential")
	}
}
