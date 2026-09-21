//go:build integration

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSyncWaitVerifiesAndResumesWithoutCheckpoint(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "barrier")
	before := c.run(nil, "--json", "checkpoint", "list", "barrier")
	root := filepath.Join(t.TempDir(), "mounted")
	pid := c.mount("barrier", root)
	write(t, filepath.Join(root, "dir", "file"), []byte("verified bytes"))
	if err := os.Chmod(filepath.Join(root, "dir", "file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := c.run(nil, "--json", "sync", "--wait", "barrier", "--timeout", "20s")
	var receipt struct {
		Success, Verified bool
		Receipt           struct {
			Entries, Files int
			Bytes          int64
			TreeSHA256     string `json:"tree_sha256"`
		}
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Success || !receipt.Verified || receipt.Receipt.Files != 1 || receipt.Receipt.Entries != 2 || receipt.Receipt.Bytes != 14 || len(receipt.Receipt.TreeSHA256) != 64 {
		t.Fatalf("invalid receipt: %s", raw)
	}
	type receiptEntry struct {
		Type string `json:"type"`
		Mode uint32 `json:"mode"`
		Size int64  `json:"size,omitempty"`
		Hash string `json:"sha256,omitempty"`
	}
	expectedTree := map[string]receiptEntry{
		"dir":      {Type: "dir", Mode: 0o750},
		"dir/file": {Type: "file", Mode: 0o600, Size: 14, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte("verified bytes")))},
	}
	encoded, _ := json.Marshal(expectedTree)
	if want := fmt.Sprintf("%x", sha256.Sum256(encoded)); receipt.Receipt.TreeSHA256 != want {
		t.Fatalf("tree digest got %s want %s (tree %s)", receipt.Receipt.TreeSHA256, want, encoded)
	}
	ctx, reader, err := c.publishedReader(context.Background(), "barrier")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := reader.Stat(ctx, "/dir/file")
	if err != nil || stat.Mode&0o777 != 0o600 {
		t.Fatalf("mode: %+v %v", stat, err)
	}
	got, err := c.remote("barrier", "dir/file")
	if err != nil || string(got) != "verified bytes" {
		t.Fatalf("remote: %q %v", got, err)
	}
	if after := c.run(nil, "--json", "checkpoint", "list", "barrier"); !bytes.Equal(before, after) {
		t.Fatalf("sync created checkpoint: before %s after %s", before, after)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("daemon stopped", err)
	}
	write(t, filepath.Join(root, "dir", "file"), []byte("automatic after barrier"))
	eventually(t, 10*time.Second, "automatic sync resumes", func() bool {
		got, e := c.remote("barrier", "dir/file")
		return e == nil && string(got) == "automatic after barrier"
	})
	var rows []map[string]any
	if err := json.Unmarshal(c.run(nil, "--json", "sync", "status", root), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["state"] != "running" || rows[0]["sync"] == nil {
		t.Fatalf("status: %+v", rows)
	}
	// Suspend the daemon so a deadline failure is deterministic, then prove the
	// request is retracted and the next verification still works.
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGCONT)
	out, _, err := c.runTimeout(5*time.Second, nil, "--json", "sync", "--wait", root, "--timeout", "50ms")
	if err == nil {
		t.Fatal("suspended daemon unexpectedly verified")
	}
	var failure map[string]any
	if err := json.Unmarshal(out, &failure); err != nil {
		t.Fatalf("failure JSON: %s %v", out, err)
	}
	if failure["verified"] != false || failure["success"] != false || failure["error"] == nil {
		t.Fatalf("failure: %s", out)
	}
	pending, err := os.ReadDir(filepath.Join(root, ".afs-lite-sync", "requests"))
	if err != nil || len(pending) != 0 {
		t.Fatalf("expired request retained: %v %v", pending, err)
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
}
