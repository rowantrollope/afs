//go:build integration

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFileHistoryIndependentWritersAndRedisRestart(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	a.run(nil, "create", "shared-history")
	a.run(nil, "history", "policy", "shared-history", "--mode", "all")
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	a.mount("shared-history", left)
	b.mount("shared-history", right)
	first, second := []byte{0, 255, 'A', 10}, []byte{128, 'B', 0, 1}
	write(t, filepath.Join(left, "shared"), first)
	a.run(nil, "sync", "--wait", left)
	awaitFile(t, filepath.Join(right, "shared"), first)
	firstPage := historyLineage(t, readHistoryCLI(t, a, "shared-history", "shared"), "")
	if len(firstPage.Versions) == 0 {
		t.Fatal("first process did not capture history")
	}
	write(t, filepath.Join(right, "shared"), second)
	b.run(nil, "sync", "--wait", right)
	awaitFile(t, filepath.Join(left, "shared"), second)
	// Two independent daemons publish disjoint paths at the same time.
	write(t, filepath.Join(left, "from-a"), []byte("left"))
	write(t, filepath.Join(right, "from-b"), []byte("right"))
	awaitFile(t, filepath.Join(right, "from-a"), []byte("left"))
	awaitFile(t, filepath.Join(left, "from-b"), []byte("right"))
	a.unmount(left)
	b.unmount(right)
	page := historyLineage(t, readHistoryCLI(t, b, "shared-history", "shared"), "")
	if page.FileID != firstPage.FileID || len(page.Versions) <= len(firstPage.Versions) {
		t.Fatalf("multiwriter lineage %+v", page)
	}
	// The owned server uses AOF always. History and its exact bodies must survive
	// restarting Redis, without a daemon or post-publication observer to repair it.
	r.stop()
	r.start()
	for i, v := range []struct {
		id   string
		want []byte
	}{{firstPage.Versions[0].VersionID, first}, {page.Versions[0].VersionID, second}} {
		out := filepath.Join(t.TempDir(), "recovered")
		a.run(nil, "history", "export", "shared-history", "shared", "--version", v.id, "--to", out)
		if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, v.want) {
			t.Fatalf("version %d after restart: %q %v", i, got, err)
		}
	}
}
