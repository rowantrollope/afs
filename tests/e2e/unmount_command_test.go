//go:build integration

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUnmountWorkspaceNameFlushesPendingChanges(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "named-unmount")
	root := filepath.Join(t.TempDir(), "different-directory-name")
	c.mount("named-unmount", root)
	content := []byte("flush pending bytes when unmounting by workspace name\n")
	write(t, filepath.Join(root, "pending.txt"), content)

	c.run(nil, "unmount", "named-unmount")
	delete(c.mounts, root)
	delete(c.mountWorkspaces, root)
	jsonEqual(t, c.run(nil, "--json", "status"), []any{})
	if got := c.published("named-unmount", "pending.txt"); string(got) != string(content) {
		t.Fatalf("workspace-name unmount did not flush pending bytes: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(root, "pending.txt")); err != nil || string(got) != string(content) {
		t.Fatalf("workspace-name unmount changed local bytes: %q %v", got, err)
	}
}

func TestUnmountWorkspaceNameRequiresDirectoryWhenAmbiguous(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "shared-unmount")
	left := filepath.Join(t.TempDir(), "left")
	right := filepath.Join(t.TempDir(), "right")
	leftPID := c.mount("shared-unmount", left)
	rightPID := c.mount("shared-unmount", right)

	for _, flags := range [][]string{nil, {"--force"}} {
		args := append([]string{"unmount", "shared-unmount"}, flags...)
		failure := c.mustFail(args...)
		if !strings.Contains(failure, "multiple registered mounts") || !strings.Contains(failure, "exact directory") {
			t.Fatalf("ambiguous name did not request a directory: %s", failure)
		}
		var rows []struct {
			PID       int    `json:"pid"`
			State     string `json:"state"`
			Workspace string `json:"workspace"`
		}
		raw := c.run(nil, "--json", "status")
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("status after ambiguous unmount: %v\n%s", err, raw)
		}
		if len(rows) != 2 {
			t.Fatalf("ambiguous unmount changed registered mounts: %s", raw)
		}
		seen := map[int]bool{}
		for _, row := range rows {
			if row.Workspace != "shared-unmount" || row.State != "running" {
				t.Fatalf("ambiguous unmount changed mount state: %s", raw)
			}
			seen[row.PID] = true
		}
		for _, pid := range []int{leftPID, rightPID} {
			if !seen[pid] {
				t.Fatalf("ambiguous unmount removed PID %d: %s", pid, raw)
			}
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatalf("ambiguous unmount stopped PID %d: %v", pid, err)
			}
		}
	}

	// An exact local directory selects one mount; the name then resolves to
	// the remaining mount without requiring its directory.
	c.unmount(left)
	var remaining []struct {
		PID int `json:"pid"`
	}
	raw := c.run(nil, "--json", "status")
	if err := json.Unmarshal(raw, &remaining); err != nil || len(remaining) != 1 || remaining[0].PID != rightPID {
		t.Fatalf("directory unmount did not preserve the other mount: %v\n%s", err, raw)
	}
	c.run(nil, "unmount", "shared-unmount")
	delete(c.mounts, right)
	delete(c.mountWorkspaces, right)
	jsonEqual(t, c.run(nil, "--json", "status"), []any{})
}
