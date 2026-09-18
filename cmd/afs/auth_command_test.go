package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/managedclient"
)

func clearAuthEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AFS_CONTROL_PLANE_URL", "AFS_CONTROL_PLANE_TOKEN"} {
		value, present := os.LookupEnv(name)
		_ = os.Unsetenv(name)
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func authTestStdin(t *testing.T, value string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(value); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = previous; _ = file.Close() })
}

func TestAuthHelpAndValidationStayOffline(t *testing.T) {
	for _, args := range [][]string{{"auth"}, {"auth", "help"}, {"auth", "--help"}, {"auth", "help", "login"}, {"auth", "login", "--help"}, {"auth", "help", "status"}, {"auth", "logout", "-h"}} {
		out, err := captureStdout(t, func() error {
			return runCLI(append([]string{"--config", "/missing/auth-config", "--redis", "invalid"}, args...))
		})
		if err != nil || !strings.Contains(out, "Usage: afs auth") {
			t.Fatalf("offline help %v: %q %v", args, out, err)
		}
	}
	for _, args := range [][]string{{"status"}, {"login"}, {"logout"}} {
		if err := authCommand(cliOptions{redisURL: "redis://localhost"}, args); err == nil || !strings.Contains(err.Error(), "do not accept --redis") {
			t.Fatalf("accepted Redis override: %v", err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"help", "unknown"}, {"status", "extra"}, {"logout", "extra"}, {"login", "extra"}, {"login", "--token", "secret"}} {
		if err := authCommand(cliOptions{configPath: "/missing/auth-config"}, args); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid args: %v", err)
		}
	}
}

func TestAuthLoginVerifiesAndAtomicallyPreservesConfig(t *testing.T) {
	clearAuthEnvironment(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connection" || r.Header.Get("Authorization") != "Bearer saved-team-token" {
			t.Errorf("unexpected login request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"redis_url":"rediss://default:redis-bootstrap-secret@unreachable.invalid:6380/2"}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.json")
	before := `{"redis":"unchanged-invalid-standalone","extra":{"integer":9007199254740993},"sync":{"future":true},"controlPlane":{"future":"retained"}}`
	if err := os.WriteFile(file, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	authTestStdin(t, "saved-team-token\n")
	out, err := captureStdout(t, func() error {
		return runCLI([]string{"--config", file, "--json", "auth", "login", "--self-hosted", "--control-plane-url", server.URL, "--token-stdin"})
	})
	if err != nil || strings.Contains(out, "saved-team-token") || strings.Contains(out, "redis-bootstrap-secret") {
		t.Fatalf("login output %q: %v", out, err)
	}
	settings, err := storedAuthSettings(file)
	if err != nil || settings.URL != server.URL || settings.Token != "saved-team-token" {
		t.Fatal("login settings were not saved")
	}
	raw, _ := os.ReadFile(file)
	for _, retained := range []string{"unchanged-invalid-standalone", "9007199254740993", `"future": true`, `"future": "retained"`} {
		if !strings.Contains(string(raw), retained) {
			t.Fatalf("lost unrelated config %q", retained)
		}
	}
	if strings.Contains(string(raw), "redis-bootstrap-secret") {
		t.Fatal("bootstrap Redis credential persisted")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("saved config is not private")
	}
}

func TestAuthLoginFailuresLeaveSavedConfigUnchanged(t *testing.T) {
	for _, mode := range []string{"unauthorized", "invalid-bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			clearAuthEnvironment(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "unauthorized" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"saved-secret"}`))
				} else {
					_, _ = w.Write([]byte(`{"redis_url":"redis://user:bootstrap-secret@/invalid-db"}`))
				}
			}))
			defer server.Close()
			file := filepath.Join(t.TempDir(), "config.json")
			before := `{"redis":"redis://localhost/4","controlPlane":{"url":"https://old.example","token":"saved-secret"}}`
			if err := os.WriteFile(file, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := captureStdout(t, func() error { return authCommand(cliOptions{configPath: file}, []string{"login", "--url", server.URL}) })
			if err == nil || out != "" || strings.Contains(err.Error(), "saved-secret") || strings.Contains(err.Error(), "bootstrap-secret") {
				t.Fatalf("failed login output=%q error=%v", out, err)
			}
			after, _ := os.ReadFile(file)
			if string(after) != before {
				t.Fatal("failed login changed config")
			}
		})
	}
}

func TestAuthLoginTokenEndpointBoundaries(t *testing.T) {
	for _, mode := range []string{"same-url", "explicit-new-url", "environment-new-url", "environment-token", "new-url-environment-token"} {
		t.Run(mode, func(t *testing.T) {
			clearAuthEnvironment(t)
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"redis_url":"redis://127.0.0.1:1/0"}`))
			}))
			defer server.Close()
			storedURL := server.URL
			args := []string{"login"}
			wantSent, wantSaved := "Bearer stored-secret", "stored-secret"
			if strings.Contains(mode, "new-url") {
				storedURL = "https://old.example"
				wantSent, wantSaved = "", ""
				if mode == "explicit-new-url" {
					args = append(args, "--url", server.URL)
				} else {
					t.Setenv("AFS_CONTROL_PLANE_URL", server.URL)
				}
			}
			if strings.Contains(mode, "environment-token") {
				t.Setenv("AFS_CONTROL_PLANE_TOKEN", "environment-secret")
				wantSent = "Bearer environment-secret"
			}
			file := filepath.Join(t.TempDir(), "config.json")
			if err := saveAuthSettings(file, managedclient.Settings{URL: storedURL, Token: "stored-secret"}); err != nil {
				t.Fatal(err)
			}
			out, err := captureStdout(t, func() error { return authCommand(cliOptions{configPath: file}, args) })
			if err != nil || strings.Contains(out, "secret") || authorization != wantSent {
				t.Fatalf("login token selection failed: %v", err)
			}
			settings, err := storedAuthSettings(file)
			if err != nil || settings.URL != server.URL || settings.Token != wantSaved {
				t.Fatal("wrong token persisted for selected endpoint")
			}
		})
	}
}

func TestAuthLoginRejectsConflictingEnvironmentOverrides(t *testing.T) {
	for _, mode := range []string{"url", "token"} {
		t.Run(mode, func(t *testing.T) {
			clearAuthEnvironment(t)
			file := filepath.Join(t.TempDir(), "missing.json")
			args := []string{"login", "--url", "https://selected.example"}
			if mode == "url" {
				t.Setenv("AFS_CONTROL_PLANE_URL", "https://environment.example")
			} else {
				t.Setenv("AFS_CONTROL_PLANE_TOKEN", "environment-secret")
				authTestStdin(t, "stdin-secret\n")
				args = append(args, "--token-stdin")
			}
			err := authCommand(cliOptions{configPath: file}, args)
			if err == nil || !strings.Contains(err.Error(), "unset AFS_CONTROL_PLANE_") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("conflicting override: %v", err)
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("conflicting override created a config")
			}
		})
	}
}

func TestAuthStatusAndLogoutAreOfflineAndPreserveStandaloneSettings(t *testing.T) {
	clearAuthEnvironment(t)
	file := filepath.Join(t.TempDir(), "config.json")
	before := `{"redis":"invalid-preserved","extra":true,"controlPlane":{"url":"http://127.0.0.1:1","token":"saved-secret","future":7}}`
	if err := os.WriteFile(file, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFS_CONTROL_PLANE_URL", "http://127.0.0.1:2")
	out, err := captureStdout(t, func() error { return authCommand(cliOptions{configPath: file, json: true}, []string{"status"}) })
	var status authStatusResult
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || status.Mode != "managed" || status.URLSource != "environment" || status.TokenPresent || status.Connectivity != "not_checked" || !status.EnvironmentOverride {
		t.Fatalf("offline effective status: %q %v", out, err)
	}
	out, err = captureStdout(t, func() error { return authCommand(cliOptions{configPath: file}, []string{"logout"}) })
	if err != nil || !strings.Contains(out, "Environment overrides remain") || strings.Contains(out, "saved-secret") {
		t.Fatalf("logout: %q %v", out, err)
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), "saved-secret") || !strings.Contains(string(raw), "invalid-preserved") || !strings.Contains(string(raw), `"future": 7`) || !strings.Contains(string(raw), `"extra": true`) {
		t.Fatal("logout failed to preserve unrelated settings or clear token")
	}
	_, err = captureStdout(t, func() error { return authCommand(cliOptions{configPath: file}, []string{"logout"}) })
	after, _ := os.ReadFile(file)
	if err != nil || string(after) != string(raw) {
		t.Fatal("logout is not idempotent")
	}
}

func TestAuthTokenInputLimitAndOneLine(t *testing.T) {
	for _, value := range []string{"first-secret\nsecond-secret\n", "\nsecret\n", strings.Repeat("x", 16385)} {
		if _, err := readControlPlaneToken(strings.NewReader(value)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid token accepted or disclosed: %v", err)
		}
	}
	for _, value := range []string{"token", "token\n", "token\r\n"} {
		if got, err := readControlPlaneToken(strings.NewReader(value)); err != nil || got != "token" {
			t.Fatalf("valid token rejected: %v", err)
		}
	}
}

func TestAuthAcceptsNullOptionalControlPlane(t *testing.T) {
	clearAuthEnvironment(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"redis_url":"redis://127.0.0.1:1/0"}`))
	}))
	defer server.Close()
	for _, action := range []string{"login", "logout"} {
		t.Run(action, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(`{"redis":"redis://localhost/3","controlPlane":null}`), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{action}
			if action == "login" {
				args = append(args, "--url", server.URL)
			}
			_, err := captureStdout(t, func() error { return authCommand(cliOptions{configPath: file}, args) })
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
