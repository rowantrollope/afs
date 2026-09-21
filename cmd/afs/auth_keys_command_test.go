package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/managedclient"
)

const testAPIKeyID = "key_0123456789abcdef0123456789abcdef"

func TestAPIKeyExpiry(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 123, time.UTC)
	for _, test := range []struct {
		value string
		want  string
	}{
		{"never", ""},
		{"30d", now.Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)},
		{"12h", now.Add(12 * time.Hour).Format(time.RFC3339Nano)},
		{"30m", now.Add(30 * time.Minute).Format(time.RFC3339Nano)},
		{"2026-10-01T12:00:00-07:00", "2026-10-01T19:00:00Z"},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := apiKeyExpiry(test.value, now)
			if err != nil || got != test.want {
				t.Fatalf("expiry %q: %q, %v; want %q", test.value, got, err, test.want)
			}
		})
	}
	for _, value := range []string{"", "0d", "-1d", "0s", "-1h", "99999999999999999999999d", "106752d", "1.5d", "2026-01-01T12:00:00Z", now.Format(time.RFC3339Nano), "credential-secret"} {
		if _, err := apiKeyExpiry(value, now); err == nil || strings.Contains(err.Error(), value) && value == "credential-secret" {
			t.Fatalf("invalid expiry accepted or disclosed: %v", err)
		}
	}
}

func TestAuthKeysHelpAndInvalidArgumentsStayOffline(t *testing.T) {
	clearAuthEnvironment(t)
	for _, args := range [][]string{
		{"auth", "keys"}, {"auth", "help", "keys"}, {"auth", "keys", "--help"},
		{"auth", "keys", "help", "create"}, {"auth", "keys", "create", "--help"},
		{"auth", "keys", "list", "-h"}, {"auth", "keys", "revoke", "--help"},
	} {
		out, err := captureStdout(t, func() error {
			return runCLI(append([]string{"--config", "/missing/auth-config", "--redis", "invalid"}, args...))
		})
		if err != nil || !strings.Contains(out, "Usage: afs auth keys") {
			t.Fatalf("offline help %v: %q %v", args, out, err)
		}
	}
	for _, args := range [][]string{{"create", "ci"}, {"list"}, {"revoke", testAPIKeyID}} {
		err := authKeysCommand(cliOptions{redisURL: "redis://localhost"}, args)
		if err == nil || !strings.Contains(err.Error(), "do not accept --redis") {
			t.Fatalf("accepted Redis override: %v", err)
		}
	}
	for _, args := range [][]string{
		{"create"}, {"create", "  "}, {"create", "name", "--expires", "secret-value"},
		{"create", "name", "--token", "secret-value"}, {"list", "secret-value"},
		{"revoke"}, {"revoke", ""}, {"help", "unknown"},
	} {
		if err := authKeysCommand(cliOptions{configPath: "/missing/auth-config"}, args); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid arguments %v: %v", args, err)
		}
	}
	err := authKeysCommand(cliOptions{configPath: filepath.Join(t.TempDir(), "config.json")}, []string{"list"})
	if err == nil || !strings.Contains(err.Error(), "require a control plane") {
		t.Fatalf("standalone API key operation: %v", err)
	}
}

func TestAuthKeysCreateListRevokeAndJSON(t *testing.T) {
	clearAuthEnvironment(t)
	key := controlplane.APIKey{ID: testAPIKeyID, Name: "build-agent", Status: "active", CreatedAt: "2026-09-21T12:00:00Z", LastUsedAt: "2026-09-21T13:00:00Z"}
	issuedToken := "afs_key_created-once-secret"
	var expiry string
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer bootstrap-secret" {
			t.Error("missing bootstrap authorization")
		}
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Name      string  `json:"name"`
				ExpiresAt *string `json:"expires_at"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name != key.Name || body.ExpiresAt == nil {
				t.Errorf("invalid create body: %+v, %v", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			expiry = *body.ExpiresAt
			key.ExpiresAt = expiry
			_ = json.NewEncoder(w).Encode(controlplane.APIKeyCreated{Key: key, Token: issuedToken})
		case http.MethodGet:
			// Even an unexpected token field in metadata must not survive decoding.
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "keys": []controlplane.APIKey{key}, "token": issuedToken})
		case http.MethodDelete:
			if r.URL.Path != "/v1/api-keys/"+testAPIKeyID {
				t.Error("wrong key revoked")
			}
			key.Status, key.RevokedAt = "revoked", "2026-09-21T14:00:00Z"
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key})
		}
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.json")
	if err := saveAuthSettings(file, managedclient.Settings{URL: server.URL, Token: "bootstrap-secret"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	start := time.Now()
	out, err := captureStdout(t, func() error {
		return runCLI([]string{"--config", file, "auth", "keys", "create", "build-agent"})
	})
	if err != nil || strings.Count(out, issuedToken) != 1 || strings.Contains(out, "bootstrap-secret") || !strings.Contains(out, "not be shown again") {
		t.Fatalf("create output %q: %v", out, err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || expiresAt.Before(start.Add(30*24*time.Hour)) || expiresAt.After(time.Now().Add(30*24*time.Hour)) {
		t.Fatalf("incorrect default expiry %q: %v", expiry, err)
	}
	for _, jsonOutput := range []bool{false, true} {
		out, err = captureStdout(t, func() error {
			return authKeysCommand(cliOptions{configPath: file, json: jsonOutput}, []string{"list"})
		})
		if err != nil || !strings.Contains(out, key.ID) || !strings.Contains(out, key.Name) || !strings.Contains(out, key.LastUsedAt) || strings.Contains(out, "secret") {
			t.Fatalf("list output %q: %v", out, err)
		}
		if jsonOutput && !json.Valid([]byte(out)) {
			t.Fatalf("invalid JSON: %q", out)
		}
	}
	out, err = captureStdout(t, func() error {
		return authKeysCommand(cliOptions{configPath: file, json: true}, []string{"create", "build-agent", "--expires", "never"})
	})
	var created controlplane.APIKeyCreated
	if err != nil || json.Unmarshal([]byte(out), &created) != nil || created.Token != issuedToken || expiry != "" {
		t.Fatalf("create never JSON output %q: %v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return authKeysCommand(cliOptions{configPath: file, json: true}, []string{"revoke", testAPIKeyID})
	})
	var revoked struct {
		Key controlplane.APIKey `json:"key"`
	}
	if err != nil || json.Unmarshal([]byte(out), &revoked) != nil || revoked.Key.Status != "revoked" || strings.Contains(out, "secret") {
		t.Fatalf("revoke output %q: %v", out, err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("key management modified saved credentials")
	}
	if len(calls) != 5 {
		t.Fatalf("unexpected requests: %v", calls)
	}
}

func TestAuthKeysEnvironmentDoesNotSendSavedTokenToDifferentServer(t *testing.T) {
	for _, envToken := range []string{"", "environment-secret"} {
		t.Run(envToken, func(t *testing.T) {
			clearAuthEnvironment(t)
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"enabled":true,"keys":[]}`))
			}))
			defer server.Close()
			file := filepath.Join(t.TempDir(), "config.json")
			if err := saveAuthSettings(file, managedclient.Settings{URL: "https://saved.example", Token: "saved-secret"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AFS_CONTROL_PLANE_URL", server.URL)
			if envToken != "" {
				t.Setenv("AFS_CONTROL_PLANE_TOKEN", envToken)
			}
			out, err := captureStdout(t, func() error { return authKeysCommand(cliOptions{configPath: file}, []string{"list"}) })
			want := ""
			if envToken != "" {
				want = "Bearer " + envToken
			}
			if err != nil || authorization != want || strings.Contains(out, "secret") {
				t.Fatalf("environment precedence: authorization %q, output %q, error %v", authorization, out, err)
			}
		})
	}
}

func TestAuthKeysRejectInvalidSavedSettingsWithoutSecrets(t *testing.T) {
	clearAuthEnvironment(t)
	for _, settings := range []string{
		`{"controlPlane":{"url":"http://secret:password@localhost:8091","token":"token-secret"}}`,
		`{"controlPlane":{"url":"https://example.com?token=secret","token":"token-secret"}}`,
		`{"controlPlane":{"url":"https://example.com","token":"token-secret\r\nInjected: value"}}`,
		`{"controlPlane":{"url":42,"token":"token-secret"}}`,
	} {
		file := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(file, []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := captureStdout(t, func() error { return authKeysCommand(cliOptions{configPath: file}, []string{"list"}) })
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(out, "secret") {
			t.Fatalf("invalid settings: output %q, error %v", out, err)
		}
	}
}

func TestAuthKeysDisabledAndUntrustedDisplay(t *testing.T) {
	if out := formatAPIKeyList(controlplane.APIKeyList{Enabled: false}); !strings.Contains(out, "AFS_CONTROL_PLANE_TOKEN") {
		t.Fatalf("missing enablement help: %q", out)
	}
	if out := formatAPIKeyList(controlplane.APIKeyList{Enabled: true}); out != "No API keys.\n" {
		t.Fatalf("empty list: %q", out)
	}
	key := controlplane.APIKey{ID: testAPIKeyID, Name: "agent\n\x1b[2Jhidden", Status: "active"}
	out := formatAPIKeyList(controlplane.APIKeyList{Enabled: true, Keys: []controlplane.APIKey{key}})
	if strings.Contains(out, key.Name) || strings.Contains(out, "\x1b") || !strings.Contains(out, "never") {
		t.Fatalf("unsafe table: %q", out)
	}
}
