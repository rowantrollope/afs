//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A relocated CLI alone must recover native registration without Redis, mount
// drivers, a sibling helper, or a PATH entry. No kernel filesystem is mounted.
func TestSingleExecutableNativeRecovery(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "renamed-afs")
	if err := os.WriteFile(executable, raw, 0700); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"fuse", "nfs"} {
		t.Run(backend, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state, mountpoint := filepath.Join(root, "state"), filepath.Join(root, "mount")
			runtimeDir := filepath.Join(state, "native", "stale")
			for _, path := range []string{state, mountpoint} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			config := filepath.Join(root, "config.json")
			if err := os.WriteFile(config, []byte(`{"redis":"redis://unused.invalid:1/0"}`), 0600); err != nil {
				t.Fatal(err)
			}
			registryPath := filepath.Join(state, "mounts.json")
			registry, err := json.Marshal(map[string]any{"version": 1, "mounts": []any{map[string]any{
				"id": "stale", "backend": backend, "workspace_id": "owned-test",
				"local_path": mountpoint, "runtime_dir": runtimeDir,
				"sync_log": filepath.Join(runtimeDir, "native.log"), "token": "test-token",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(registryPath, registry, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "--config", config, "--json", "unmount", mountpoint, "--force")
			cmd.Env = append(os.Environ(), "HOME="+root, "PATH="+dir, "AFS_STATE_DIR="+state,
				"AFS_NATIVE_HELPER="+filepath.Join(root, "obsolete-helper"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("standalone native recovery: %v\n%s", err, out)
			}
			var result struct {
				Unmounted    bool `json:"unmounted"`
				Synchronized bool `json:"synchronized"`
			}
			if err := json.Unmarshal(out, &result); err != nil || !result.Unmounted || result.Synchronized {
				t.Fatalf("recovery result = %s, error %v", out, err)
			}
			updated, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var reg struct{ Mounts []json.RawMessage }
			if err := json.Unmarshal(updated, &reg); err != nil || len(reg.Mounts) != 0 {
				t.Fatalf("recovery left registration: %s, %v", updated, err)
			}
			if info, err := os.Stat(mountpoint); err != nil || !info.IsDir() {
				t.Fatalf("recovery removed preexisting mountpoint: %v", err)
			}
		})
	}
}
