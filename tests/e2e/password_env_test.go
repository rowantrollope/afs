//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisPasswordEnvironment(t *testing.T) {
	const password = "test-env-password:@/with-specials"
	t.Setenv("AFS_REDIS_PASSWORD", password)
	r := newRedis(t)
	if err := r.client.ConfigSet(context.Background(), "requirepass", password).Err(); err != nil {
		t.Fatal(err)
	}
	_ = r.client.Close()
	r.client = redis.NewClient(&redis.Options{Addr: r.addr, Password: password})
	c := newCLI(t, r)
	passwordURL := func(secret string) string {
		u, err := url.Parse(r.url())
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword("default", secret)
		return u.String()
	}
	save := func(endpoint string) {
		t.Helper()
		data, err := json.Marshal(map[string]string{"redis": endpoint})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.config, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var environmentURL string
	run := func(secret *string, wantSuccess bool, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--config", c.config}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "AFS_REDIS_PASSWORD=") && !strings.HasPrefix(entry, "AFS_REDIS_URL=") && !strings.HasPrefix(entry, "AFS_STATE_DIR=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "AFS_STATE_DIR="+c.state, "AFS_REDIS_URL="+environmentURL)
		if secret != nil {
			cmd.Env = append(cmd.Env, "AFS_REDIS_PASSWORD="+*secret)
		}
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("afs %v success=%v, wanted %v: %s", args, err == nil, wantSuccess, out)
		}
		if bytes.Contains(out, []byte(password)) {
			t.Fatal("command output exposed password")
		}
		return out
	}
	correct, wrong, empty := password, "incorrect-password", ""
	save(passwordURL(password))
	run(nil, true, "create", "password-env") // Saved JSON remains sufficient.
	run(&wrong, false, "list")               // Environment wins over saved password.
	run(&empty, false, "list")               // Explicit empty clears saved password.
	save(passwordURL(wrong))
	run(nil, false, "list")
	run(&correct, true, "list")
	run(&correct, true, "--redis", passwordURL(wrong), "list")
	environmentURL = passwordURL(password)
	run(nil, true, "list")                                 // Environment URL overrides saved JSON.
	run(nil, false, "--redis", passwordURL(wrong), "list") // Explicit flag wins.
	environmentURL = passwordURL(wrong)
	run(&correct, true, "list") // Password override also applies to environment URL.
	// The workspace exists only in DB 0: both the URL and password must reach
	// the background daemon, despite JSON pointing at a different database.
	environmentURL = r.url()
	save(strings.TrimSuffix(r.url(), "/0") + "/1")
	root := t.TempDir()
	mount := func(path string) {
		t.Helper()
		out := run(&correct, true, "--json", "mount", "password-env", path)
		var result struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.PID <= 0 {
			t.Fatalf("mount result %s: %v", out, err)
		}
		c.mounts[path] = result.PID
	}
	mount(root)
	content := []byte("authenticated background sync")
	write(t, filepath.Join(root, "proof.txt"), content)
	run(&correct, true, "unmount", root)
	delete(c.mounts, root)
	restored := t.TempDir()
	mount(restored)
	if got, err := os.ReadFile(filepath.Join(restored, "proof.txt")); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("background sync did not persist file: %q %v", got, err)
	}
	run(&correct, true, "unmount", restored)
	delete(c.mounts, restored)
	for _, base := range []string{c.state, c.config} {
		if err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err == nil && (bytes.Contains(data, []byte(password)) || bytes.Contains(data, []byte(url.QueryEscape(password)))) {
				t.Errorf("environment password persisted in %s", path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
