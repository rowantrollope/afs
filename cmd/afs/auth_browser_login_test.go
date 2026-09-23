package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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

const testCLIRequestID = "cli_0123456789abcdef0123456789abcdef"

type browserLoginFake struct {
	challenge string
	status    string
	path      string
	polled    bool
	transient bool
}

func (f *browserLoginFake) StartCLILogin(_ context.Context, _, challenge string) (controlplane.CLILoginStart, error) {
	f.challenge = challenge
	path := f.path
	if path == "" {
		path = "/connect-cli?request=" + testCLIRequestID
	}
	return controlplane.CLILoginStart{ID: testCLIRequestID, DeviceCode: "private-device-secret", UserCode: "ABCD-EFGH", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), Interval: 1, VerificationPath: path}, nil
}

func (f *browserLoginFake) PollCLILogin(_ context.Context, deviceCode, verifier string) (controlplane.CLILoginPoll, error) {
	f.polled = true
	if f.transient {
		f.transient = false
		return controlplane.CLILoginPoll{}, errors.New("temporary connection failure")
	}
	digest := sha256.Sum256([]byte(verifier))
	if deviceCode != "private-device-secret" || base64.RawURLEncoding.EncodeToString(digest[:]) != f.challenge || len(verifier) != 43 {
		return controlplane.CLILoginPoll{}, errors.New("invalid PKCE proof")
	}
	return controlplane.CLILoginPoll{Status: f.status, Token: "private-issued-key"}, nil
}

func TestBrowserLoginExchangesProofWithoutExposingSecrets(t *testing.T) {
	for _, status := range []string{"complete", "denied", "expired"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			fake := &browserLoginFake{status: status}
			var out bytes.Buffer
			var opened string
			key, err := runBrowserLogin(context.Background(), fake, "https://afs.example/databases/db_123", "Test CLI", false, &out, func(u string) error { opened = u; return errors.New("no browser") })
			if (status == "complete") != (err == nil) {
				t.Fatalf("status %s: %v", status, err)
			}
			if status == "complete" && key != "private-issued-key" {
				t.Fatal("missing issued key")
			}
			if opened != "https://afs.example/connect-cli?request="+testCLIRequestID || !strings.Contains(out.String(), "ABCD-EFGH") || !strings.Contains(out.String(), "Open the link") {
				t.Fatalf("bad approval instructions: %s", out.String())
			}
			for _, secret := range []string{"private-device-secret", "private-issued-key", fake.challenge} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("secret exposed")
				}
			}
		})
	}
}

func TestBrowserLoginCancellationAndUntrustedLink(t *testing.T) {
	for _, mode := range []string{"cancel", "external-link"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &browserLoginFake{}
			if mode == "cancel" {
				cancel()
			} else {
				fake.path = "https://attacker.example/approve"
			}
			var out bytes.Buffer
			_, err := runBrowserLogin(ctx, fake, "https://afs.example", "Test", true, &out, func(string) error { t.Fatal("opened browser in no-browser mode"); return nil })
			if err == nil || fake.polled {
				t.Fatal("invalid or canceled login polled")
			}
		})
	}
	for _, endpoint := range []string{"http://afs.example", "https://user:password@afs.example", "https://afs.example?token=secret", "https://afs.example#fragment", "file:///tmp/afs"} {
		if _, err := browserApprovalURL(endpoint, testCLIRequestID); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://localhost:8091", "http://127.0.0.1:8091/databases/db_test", "http://[::1]:8091", "https://afs.example"} {
		if _, err := browserApprovalURL(endpoint, testCLIRequestID); err != nil {
			t.Fatalf("rejected %s: %v", endpoint, err)
		}
	}
}

func TestBrowserLoginRetriesInterruptedPolling(t *testing.T) {
	fake := &browserLoginFake{status: "complete", transient: true}
	var out bytes.Buffer
	key, err := runBrowserLogin(context.Background(), fake, "https://afs.example", "Test", true, &out, nil)
	if err != nil || key != "private-issued-key" || !strings.Contains(out.String(), "Retrying while you approve") {
		t.Fatalf("transient polling failed: %v", err)
	}
}

func TestAuthBrowserLoginSavesScopedKeyAndPreservesOtherConfig(t *testing.T) {
	clearAuthEnvironment(t)
	var challenge string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/databases/db_test/v1/auth/") {
			t.Errorf("lost selected database: %s", r.URL.Path)
		}
		switch strings.TrimPrefix(r.URL.Path, "/databases/db_test/v1/auth/") {
		case "config":
			_, _ = w.Write([]byte(`{"enabled":true,"browser_login":true}`))
		case "cli/start":
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			challenge = input["code_challenge"]
			if r.Header.Get("Authorization") != "" {
				t.Error("sent old key to public exchange")
			}
			_ = json.NewEncoder(w).Encode(controlplane.CLILoginStart{ID: testCLIRequestID, DeviceCode: "device-secret", UserCode: "ABCD-EFGH", Interval: 1, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), VerificationPath: "/connect-cli?request=" + testCLIRequestID})
		case "cli/token":
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			digest := sha256.Sum256([]byte(input["code_verifier"]))
			if input["device_code"] != "device-secret" || base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
				t.Error("invalid proof exchange")
			}
			_, _ = w.Write([]byte(`{"status":"complete","token":"issued-private-key"}`))
		case "verify":
			if r.Header.Get("Authorization") != "Bearer issued-private-key" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"authenticated":true}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"future":true,"controlPlane":{"url":"https://old.example","token":"old-key"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := server.URL + "/databases/db_test"
	out, err := captureStdout(t, func() error {
		return authCommand(cliOptions{configPath: file}, []string{"login", "--url", endpoint, "--no-browser", "--name", "Test computer"})
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := storedAuthSettings(file)
	if err != nil || settings != (managedclient.Settings{URL: endpoint, Token: "issued-private-key"}) {
		t.Fatal("scoped key not saved")
	}
	raw, _ := os.ReadFile(file)
	info, _ := os.Stat(file)
	if !strings.Contains(string(raw), `"future": true`) || info.Mode().Perm() != 0600 || strings.Contains(out, "issued-private-key") {
		t.Fatal("configuration privacy/preservation failed")
	}
}

func TestAuthBrowserFlagsRejectConflictingCredentials(t *testing.T) {
	clearAuthEnvironment(t)
	for _, args := range [][]string{{"--browser", "--no-browser"}, {"--browser", "--token-stdin"}, {"--name", "test", "--token-stdin"}} {
		if err := authLogin(cliOptions{}, args); err == nil {
			t.Fatal("accepted conflicting options")
		}
	}
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", "runtime-secret")
	if err := authLogin(cliOptions{}, []string{"--browser"}); err == nil || !strings.Contains(err.Error(), "unset AFS_CONTROL_PLANE_TOKEN") {
		t.Fatal("browser login ignored overriding token")
	}
}
