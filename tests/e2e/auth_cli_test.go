//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func authResult(t *testing.T, c *cli, input []byte, secrets []string, args ...string) map[string]any {
	t.Helper()
	out, diagnostics, err := c.runTimeout(30*time.Second, input, append([]string{"--json", "auth"}, args...)...)
	assertAuthOutputHasNoSecrets(t, append(bytes.Clone(out), diagnostics...), secrets...)
	if err != nil {
		t.Fatalf("auth %v: %v\nstdout=%s\nstderr=%s", args, err, out, diagnostics)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("auth %v did not produce JSON: %v", args, err)
	}
	return result
}

func assertAuthOutputHasNoSecrets(t *testing.T, output []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		for _, candidate := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), url.UserPassword("", secret).String()} {
			if secret != "" && bytes.Contains(output, []byte(candidate)) {
				t.Fatal("auth command disclosed a credential")
			}
		}
	}
}

func readAuthConfig(t *testing.T, path string) ([]byte, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return raw, document
}

func assertAuthConfigPrivate(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("login config permissions must be 0600: %v %v", info, err)
	}
}

func TestAuthCLIURLLoginMountAndOfflineLogout(t *testing.T) {
	const password = "auth-server-redis-secret:@/with-specials"
	r := newRedis(t)
	if err := r.client.ConfigSet(context.Background(), "requirepass", password).Err(); err != nil {
		t.Fatal(err)
	}
	_ = r.client.Close()
	r.client = redis.NewClient(&redis.Options{Addr: r.addr, Password: password})
	redisURL, err := url.Parse(r.url())
	if err != nil {
		t.Fatal(err)
	}
	redisURL.User = url.UserPassword("default", password)
	p := newManagementProcess(t, r, redisURL.String())
	endpoint := "http://" + p.addr
	c := newCLI(t, r)
	c.managed = true // Auth must not inherit the standalone helper's --redis.
	c.environment = map[string]string{}
	if err := os.Remove(c.config); err != nil {
		t.Fatal(err)
	}
	secrets := []string{p.token, password}
	status := authResult(t, c, nil, secrets, "status")
	if status["mode"] != "standalone" || status["connectivity"] != "not_checked" {
		t.Fatalf("new installation auth status: %v", status)
	}
	failedOut, failedDiagnostics, failedErr := c.runTimeout(15*time.Second, []byte("invalid-first-token\n"), "auth", "login", "--url", endpoint, "--token-stdin")
	assertAuthOutputHasNoSecrets(t, append(bytes.Clone(failedOut), failedDiagnostics...), append(secrets, "invalid-first-token")...)
	if failedErr == nil {
		t.Fatal("first login accepted an invalid team token")
	}
	if _, err := os.Stat(c.config); !os.IsNotExist(err) {
		t.Fatal("failed first login created a configuration")
	}
	result := authResult(t, c, []byte(p.token+"\n"), secrets, "login", "--url", endpoint, "--token-stdin")
	if result["logged_in"] != true || result["url"] != endpoint || result["token_saved"] != true {
		t.Fatalf("login result: %v", result)
	}
	assertAuthConfigPrivate(t, c.config)
	loggedIn, config := readAuthConfig(t, c.config)
	settings, ok := config["controlPlane"].(map[string]any)
	if !ok || settings["url"] != endpoint || settings["token"] != p.token {
		t.Fatal("login did not save its server URL and team token")
	}
	assertAuthOutputHasNoSecrets(t, loggedIn, password)
	status = authResult(t, c, nil, secrets, "status")
	if status["mode"] != "managed" || status["url"] != endpoint || status["url_source"] != "config" || status["token_present"] != true || status["token_source"] != "config" || status["connectivity"] != "not_checked" {
		t.Fatalf("saved managed auth status: %v", status)
	}

	// An explicitly supplied bad token must not fall back to the saved token,
	// and a failed validation must leave the existing configuration untouched.
	out, diagnostics, err := c.runTimeout(15*time.Second, []byte("invalid-team-token\n"), "auth", "login", "--url", endpoint, "--token-stdin")
	assertAuthOutputHasNoSecrets(t, append(bytes.Clone(out), diagnostics...), append(secrets, "invalid-team-token")...)
	if err == nil {
		t.Fatal("login accepted an invalid supplied token")
	}
	if current, _ := readAuthConfig(t, c.config); !bytes.Equal(current, loggedIn) {
		t.Fatal("failed login changed the saved configuration")
	}

	c.run(nil, "create", "auth-managed")
	if listing := c.run(nil, "--json", "list"); !bytes.Contains(listing, []byte("auth-managed")) {
		t.Fatal("login did not select managed CLI operations")
	}
	root := filepath.Join(t.TempDir(), "mount")
	c.mount("auth-managed", root)
	body := []byte("auth login supplied the protected Redis mount connection\n")
	write(t, filepath.Join(root, "proof"), body)
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	awaitRemote(t, c, "auth-managed", "proof", body)
	assertManagedSecretAbsent(t, c, password)
	c.unmount(root)

	p.stop()
	out, diagnostics, err = c.runTimeout(15*time.Second, []byte(p.token+"\n"), "auth", "login", "--url", endpoint, "--token-stdin")
	assertAuthOutputHasNoSecrets(t, append(bytes.Clone(out), diagnostics...), secrets...)
	if err == nil {
		t.Fatal("login succeeded while its control plane was unavailable")
	}
	if current, _ := readAuthConfig(t, c.config); !bytes.Equal(current, loggedIn) {
		t.Fatal("offline login changed the saved configuration")
	}
	status = authResult(t, c, nil, secrets, "status")
	if status["mode"] != "managed" || status["connectivity"] != "not_checked" {
		t.Fatalf("offline auth status did not describe the saved selection: %v", status)
	}
	result = authResult(t, c, nil, secrets, "logout")
	if result["logged_out"] != true || result["existing_mounts_unchanged"] != true {
		t.Fatalf("offline logout: %v", result)
	}
	loggedOut, _ := readAuthConfig(t, c.config)
	if bytes.Contains(loggedOut, []byte(p.token)) || bytes.Contains(loggedOut, []byte(endpoint)) {
		t.Fatal("logout retained saved control-plane credentials")
	}
	authResult(t, c, nil, secrets, "logout")
	if current, _ := readAuthConfig(t, c.config); !bytes.Equal(current, loggedOut) {
		t.Fatal("repeated logout changed the already logged-out configuration")
	}
	status = authResult(t, c, nil, secrets, "status")
	if status["mode"] != "standalone" || status["token_present"] != false {
		t.Fatalf("logged-out status: %v", status)
	}

	standalone := newRedis(t)
	c.run(nil, "config", "set", "redis", standalone.url())
	c.run(nil, "create", "auth-standalone")
	listing := c.run(nil, "--json", "list")
	if !bytes.Contains(listing, []byte("auth-standalone")) || bytes.Contains(listing, []byte("auth-managed")) {
		t.Fatal("logout did not restore saved standalone Redis selection")
	}
}

func TestAuthCLILegacyAliasPreservesConfigAndReportsEnvironment(t *testing.T) {
	r := newRedis(t)
	p := newManagementProcess(t, r)
	endpoint := "http://" + p.addr
	c := newCLI(t, r)
	c.managed = true
	c.environment = map[string]string{}
	original := map[string]any{
		"redis":        "redis://saved-user:saved-password@standalone.example:6379/3",
		"sync":         map[string]any{"fileSizeCapMB": float64(321), "watcherQueueCapacity": float64(2048)},
		"futureOption": map[string]any{"preserve": true},
	}
	raw, _ := json.Marshal(original)
	if err := os.WriteFile(c.config, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.config, 0644); err != nil {
		t.Fatal(err)
	}
	secrets := []string{p.token, "saved-password"}
	authResult(t, c, []byte(p.token+"\n"), secrets, "login", "--self-hosted", "--control-plane-url", endpoint, "--token-stdin")
	assertAuthConfigPrivate(t, c.config)
	_, saved := readAuthConfig(t, c.config)
	for key, expected := range original {
		if !reflect.DeepEqual(saved[key], expected) {
			t.Fatalf("login changed unrelated %s setting", key)
		}
	}
	c.environment["AFS_CONTROL_PLANE_URL"] = endpoint
	c.environment["AFS_CONTROL_PLANE_TOKEN"] = p.token
	status := authResult(t, c, nil, secrets, "status")
	if status["environment_override"] != true || status["url_source"] != "environment" || status["token_source"] != "environment" {
		t.Fatalf("auth status did not identify effective environment selection: %v", status)
	}
	result := authResult(t, c, nil, secrets, "logout")
	if result["environment_override"] != true {
		t.Fatal("logout did not report the remaining environment override")
	}
	_, saved = readAuthConfig(t, c.config)
	for key, expected := range original {
		if !reflect.DeepEqual(saved[key], expected) {
			t.Fatalf("logout changed unrelated %s setting", key)
		}
	}
	if settings, ok := saved["controlPlane"].(map[string]any); ok && (settings["url"] != nil && settings["url"] != "" || settings["token"] != nil && settings["token"] != "") {
		t.Fatal("environment override prevented logout from clearing saved credentials")
	}
	// Human output also needs a notice; a successful logout cannot imply the
	// still-effective environment variables were removed from the shell.
	out, diagnostics, err := c.runTimeout(10*time.Second, nil, "auth", "logout")
	assertAuthOutputHasNoSecrets(t, append(bytes.Clone(out), diagnostics...), secrets...)
	if err != nil || !strings.Contains(strings.ToLower(string(out)+string(diagnostics)), "environment") {
		t.Fatal("logout did not explain the environment override")
	}
	status = authResult(t, c, nil, secrets, "status")
	if status["mode"] != "managed" || status["url"] != endpoint {
		t.Fatal("environment override was not effective after clearing saved credentials")
	}
	jsonEqual(t, c.run(nil, "--json", "list"), []any{})
}
