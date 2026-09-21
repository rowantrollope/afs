//go:build integration

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestHistoryLifecycleCheckpointRestoreAndIndependentFork(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "history-source")
	c.run(nil, "history", "policy", "history-source", "--mode", "all")
	page := func(workspace, path string) controlplane.FileHistoryLineage {
		t.Helper()
		return historyLineage(t, readHistoryCLI(t, c, workspace, path), "")
	}
	recoverBytes := func(workspace, path, version string, want []byte) {
		t.Helper()
		destination := filepath.Join(t.TempDir(), "recovered")
		c.run(nil, "history", "export", workspace, path, "--version", version, "--to", destination)
		if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("recover %s/%s %s = %q, %v", workspace, path, version, got, err)
		}
	}
	first := bytes.Repeat([]byte{0, 255, 'a', '\n'}, 4096)
	second := bytes.Repeat([]byte{17, 0, 128, 'z'}, 5000)
	c.put("history-source", "file", first)
	c.put("history-source", "empty", []byte{})
	c.run(nil, "checkpoint", "create", "history-source", "--name", "first")
	c.put("history-source", "file", second)
	c.put("history-source", "later", []byte("created after checkpoint"))
	c.closeWriter("history-source")
	sourcePage := page("history-source", "file")
	if len(sourcePage.Versions) < 2 {
		t.Fatalf("source history = %+v", sourcePage)
	}
	secondID := sourcePage.Versions[0].VersionID

	c.run(nil, "fork", "history-source", "history-fork", "--checkpoint", "first")
	forkPage := page("history-fork", "file")
	if forkPage.FileID != sourcePage.FileID || len(forkPage.Versions) == 0 || forkPage.Versions[0].Source != "checkpoint_fork" || forkPage.Versions[0].Op != "put" {
		t.Fatalf("fork history = %+v", forkPage)
	}
	if got := c.published("history-fork", "file"); !bytes.Equal(got, first) {
		t.Fatal("fork did not select the requested checkpoint")
	}
	recoverBytes("history-fork", "file", secondID, second)
	recoverBytes("history-fork", "empty", "latest", []byte{})
	forkLater := page("history-fork", "later")
	if len(forkLater.Versions) < 2 || forkLater.Versions[0].Kind != controlplane.FileVersionKindTombstone {
		t.Fatalf("fork omitted the post-checkpoint file tombstone: %+v", forkLater)
	}

	c.run(nil, "checkpoint", "restore", "history-source", "first", "--yes")
	restored := page("history-source", "file")
	if restored.FileID != sourcePage.FileID || len(restored.Versions) == 0 || restored.Versions[0].Source != "checkpoint_restore" || restored.Versions[0].Op != "put" {
		t.Fatalf("restored history = %+v", restored)
	}
	recoverBytes("history-source", "file", secondID, second)
	recoverBytes("history-source", "later", "latest", []byte("created after checkpoint"))
	if got := c.published("history-source", "file"); !bytes.Equal(got, first) {
		t.Fatal("checkpoint restore changed selected bytes")
	}

	c.run(nil, "delete", "history-source", "--yes")
	recoverBytes("history-fork", "file", secondID, second)
	c.put("history-fork", "file", []byte("independent edit"))
	c.closeWriter("history-fork")
	continued := page("history-fork", "file")
	if continued.FileID != forkPage.FileID || len(continued.Versions) <= len(forkPage.Versions) {
		t.Fatalf("fork policy or inode lineage was not retained: %+v", continued)
	}
}
