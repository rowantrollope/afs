//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mountReadOnly(t *testing.T, c *cli, workspace, root string) {
	t.Helper()
	raw := c.run(nil, "--json", "mount", workspace, root, "--readonly")
	var result struct {
		PID      int  `json:"pid"`
		ReadOnly bool `json:"read_only"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.PID <= 0 || !result.ReadOnly {
		t.Fatalf("read-only mount: %s, %v", raw, err)
	}
	c.mounts[root] = result.PID
	c.mountWorkspaces[root] = workspace
}

func TestReadOnlyUnmountDoesNotRequireRedis(t *testing.T) {
	r := newRedis(t)
	reader := newCLI(t, r)
	seed := t.TempDir()
	write(t, filepath.Join(seed, "file"), []byte("remote bytes"))
	reader.run(nil, "create", "readonly-offline", "--from", seed)
	root := filepath.Join(t.TempDir(), "reader")
	mountReadOnly(t, reader, "readonly-offline", root)
	write(t, filepath.Join(root, "local-only"), []byte("never upload"))
	r.signal(syscall.SIGSTOP)
	defer r.signal(syscall.SIGCONT)
	out, diagnostic, err := reader.runTimeout(10*time.Second, nil, "--json", "unmount", root)
	var result map[string]any
	parseErr := json.Unmarshal(out, &result)
	if err != nil || parseErr != nil || result["read_only"] != true || result["synchronized"] != false || result["save"] != nil {
		t.Fatalf("reader unmount tried to save during outage: %v %s %s", err, out, diagnostic)
	}
	delete(reader.mounts, root)
	delete(reader.mountWorkspaces, root)
	awaitFile(t, filepath.Join(root, "local-only"), []byte("never upload"))
}

func unmountReadOnly(t *testing.T, c *cli, root string) {
	t.Helper()
	raw := c.run(nil, "--json", "unmount", root)
	var result struct {
		Unmounted    bool            `json:"unmounted"`
		Synchronized bool            `json:"synchronized"`
		ReadOnly     bool            `json:"read_only"`
		Save         json.RawMessage `json:"save"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.Unmounted || result.Synchronized || !result.ReadOnly || !bytes.Equal(result.Save, []byte("null")) {
		t.Fatalf("reader unmount claimed a save: %s, %v", raw, err)
	}
	delete(c.mounts, root)
	delete(c.mountWorkspaces, root)
}

func TestReadOnlyMountLifecycleAndCheckpoint(t *testing.T) {
	r := newRedis(t)
	writer, reader := newCLI(t, r), newCLI(t, r)
	seed := t.TempDir()
	write(t, filepath.Join(seed, "edited"), []byte("published"))
	write(t, filepath.Join(seed, "removed"), []byte("still remote"))
	writer.run(nil, "create", "readonly", "--from", seed)
	writable, observer := filepath.Join(t.TempDir(), "writer"), filepath.Join(t.TempDir(), "reader")
	writer.mount("readonly", writable)
	mountReadOnly(t, reader, "readonly", observer)
	awaitFile(t, filepath.Join(observer, "edited"), []byte("published"))
	if err := os.Chmod(filepath.Join(observer, "edited"), 0644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(observer, "edited"), []byte("reader edit"))
	write(t, filepath.Join(observer, "local-only", "file"), []byte("reader only"))
	if err := os.Symlink("edited", filepath.Join(observer, "local-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(observer, "removed")); err != nil {
		t.Fatal(err)
	}
	// Receiving a subsequent peer publication exercises the live watcher and
	// reconciliation paths after the observer's local mutations.
	write(t, filepath.Join(writable, "after-edits"), []byte("peer publication"))
	awaitFile(t, filepath.Join(observer, "after-edits"), []byte("peer publication"))
	reader.run(nil, "cp", "create", "readonly", "--name", "observer-checkpoint")
	reader.run(nil, "fork", "readonly", "reader-snapshot", "--checkpoint", "observer-checkpoint")
	for _, workspace := range []string{"readonly", "reader-snapshot"} {
		for name, want := range map[string]string{"edited": "published", "removed": "still remote", "after-edits": "peer publication"} {
			if got := string(reader.published(workspace, name)); got != want {
				t.Fatalf("reader changed %s/%s: %q", workspace, name, got)
			}
		}
		for _, path := range []string{"local-only", "local-link"} {
			reader.awaitMissingPublished(workspace, path)
		}
	}
	unmountReadOnly(t, reader, observer)
	awaitFile(t, filepath.Join(observer, "local-only", "file"), []byte("reader only"))
	write(t, filepath.Join(observer, "stopped-only"), []byte("offline reader edit"))
	write(t, filepath.Join(writable, "after-remount"), []byte("new peer bytes"))
	mountReadOnly(t, reader, "readonly", observer)
	awaitFile(t, filepath.Join(observer, "after-remount"), []byte("new peer bytes"))
	reader.run(nil, "cp", "create", "readonly", "--name", "after-reader-remount")
	reader.awaitMissingPublished("readonly", "stopped-only")
	unmountReadOnly(t, reader, observer)
	if message := reader.mustFail("mount", "readonly", observer); !strings.Contains(message, "different read-only setting") {
		t.Fatalf("unsafe access-mode switch was not explained: %s", message)
	}
}
