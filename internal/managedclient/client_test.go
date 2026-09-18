package managedclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("presence state did not converge")
}

func TestLifecycleRegistersVerifiesRetriesAndClosesSameSession(t *testing.T) {
	var registrations, heartbeats, closes atomic.Int32
	var available atomic.Bool
	available.Store(true)
	var forget atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing bearer token")
			w.WriteHeader(401)
			return
		}
		if !available.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/workspaces/workspace/sessions":
			var input Registration
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.SessionID != "sess_test" || input.AgentID != "agent_test" || input.WorkspaceGeneration != "generation" {
				t.Errorf("registration identity changed: %+v", input)
			}
			registrations.Add(1)
			_ = json.NewEncoder(w).Encode(Session{SessionID: input.SessionID, AgentID: input.AgentID, WorkspaceID: "workspace", StorageProbeKey: "afs:management:probe:sess_test", StorageProbeValue: "proof"})
		case "POST /v1/sessions/sess_test/heartbeat":
			if forget.CompareAndSwap(true, false) {
				w.WriteHeader(404)
				return
			}
			var input Registration
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if heartbeats.Load() == 0 && input.StorageProbeValue != "proof" {
				t.Error("initial heartbeat did not confirm Redis verification")
			}
			heartbeats.Add(1)
			w.Write([]byte(`{}`))
		case "DELETE /v1/sessions/sess_test":
			closes.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	registration := Registration{SessionID: "sess_test", AgentID: "agent_test", WorkspaceID: "workspace", WorkspaceGeneration: "generation"}
	lifecycle := start(context.Background(), Settings{URL: server.URL, Token: "private-token"}, registration, func(_ context.Context, key string) (string, error) {
		if key != "afs:management:probe:sess_test" {
			t.Errorf("unexpected Redis probe: %s", key)
		}
		return "proof", nil
	}, nil, 10*time.Millisecond, 100*time.Millisecond)
	if status := lifecycle.Snapshot(); !status.Registered || status.LastSeenAt == "" {
		t.Fatalf("registration failed: %+v", status)
	}
	available.Store(false)
	eventually(t, func() bool { return !lifecycle.Snapshot().Registered })
	if strings.Contains(lifecycle.Snapshot().LastError, "private-token") {
		t.Fatal("token leaked")
	}
	available.Store(true)
	eventually(t, func() bool { return lifecycle.Snapshot().Registered && registrations.Load() >= 2 })
	forget.Store(true)
	eventually(t, func() bool { return registrations.Load() >= 3 && lifecycle.Snapshot().Registered })
	lifecycle.Close()
	lifecycle.Close()
	if closes.Load() != 1 {
		t.Fatalf("close requests=%d", closes.Load())
	}
}

func TestLifecycleStorageMismatchNeverConfirmsPresence(t *testing.T) {
	var confirmed, closed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			closed.Add(1)
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			confirmed.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode(Session{SessionID: "sess_test", WorkspaceID: "workspace", StorageProbeKey: "afs:management:probe:sess_test", StorageProbeValue: "server-store"})
	}))
	defer server.Close()
	lifecycle := start(context.Background(), Settings{URL: server.URL}, Registration{SessionID: "sess_test", WorkspaceID: "workspace"}, func(context.Context, string) (string, error) { return "other-store", nil }, nil, time.Hour, 100*time.Millisecond)
	defer lifecycle.Close()
	status := lifecycle.Snapshot()
	if status.Registered || !strings.Contains(status.LastError, "same Redis") || confirmed.Load() != 0 || closed.Load() != 1 {
		t.Fatalf("mismatch handling: %+v confirmed=%d closed=%d", status, confirmed.Load(), closed.Load())
	}
}

func TestSettingsRejectCredentialsAndRemotePlaintext(t *testing.T) {
	for _, endpoint := range []string{"https://user:secret@example.com", "https://example.com?token=secret", "http://example.com", "ftp://example.com", "not a URL"} {
		if err := (Settings{URL: endpoint}).Validate(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe endpoint accepted or exposed: %v", err)
		}
	}
	for _, endpoint := range []string{"", "http://127.0.0.1:8091", "http://[::1]:8091", "https://example.com/afs"} {
		if err := (Settings{URL: endpoint}).Validate(); err != nil {
			t.Errorf("valid endpoint: %v", err)
		}
	}
}
