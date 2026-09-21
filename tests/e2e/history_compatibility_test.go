//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestHistoryCompatibilityCLIProcess(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "compatible")
	c.run(nil, "history", "policy", "compatible", "--mode", "all")
	c.put("compatible", "file.txt", []byte("old\n"))
	c.run(nil, "checkpoint", "create", "compatible", "--name", "old")
	c.put("compatible", "file.txt", []byte("new\n"))
	c.closeWriter("compatible")
	var first controlplane.FileHistoryResponse
	if err := json.Unmarshal(c.run(nil, "--json", "history", "list", "compatible", "file.txt", "--order", "asc", "--limit", "1"), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Lineages) != 1 || first.NextCursor == "" {
		t.Fatalf("history page: %+v", first)
	}
	version := first.Lineages[0].Versions[0]
	var second controlplane.FileHistoryResponse
	if err := json.Unmarshal(c.run(nil, "--json", "history", "list", "compatible", "file.txt", "--order", "asc", "--limit", "1", "--cursor", first.NextCursor), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Lineages) != 1 || second.Lineages[0].Versions[0].Ordinal <= version.Ordinal {
		t.Fatalf("second history page: %+v", second)
	}
	if shown := c.run(nil, "--json", "history", "show", "compatible", "file.txt", "--version", version.VersionID); !bytes.Contains(shown, []byte(`"content":"old\n"`)) {
		t.Fatalf("show: %s", shown)
	}
	diff := c.run(nil, "--json", "history", "diff", "compatible", "file.txt", "--from-version", version.VersionID, "--to-ref", "working-copy")
	if !bytes.Contains(diff, []byte("-old")) || !bytes.Contains(diff, []byte("+new")) {
		t.Fatalf("CLI diff: %s", diff)
	}
	c.run(nil, "history", "restore", "compatible", "file.txt", "--version", version.VersionID)
	if got := c.published("compatible", "file.txt"); string(got) != "old\n" {
		t.Fatalf("CLI restore: %q", got)
	}
	c.removePublished("compatible", "file.txt")
	c.closeWriter("compatible")
	c.run(nil, "history", "undelete", "compatible", "file.txt")
	if got := c.published("compatible", "file.txt"); string(got) != "old\n" {
		t.Fatalf("CLI undelete: %q", got)
	}

	listed := readHistoryCLI(t, c, "compatible", "file.txt")
	if len(listed.Lineages) != 1 || listed.Lineages[0].FileID != version.FileID {
		t.Fatalf("CLI listing lost restored lineage: %+v", listed)
	}
	c.run(nil, "history", "policy", "compatible", "--mode", "off")
	c.put("compatible", "off.txt", []byte("activity without snapshots"))
	c.closeWriter("compatible")
	if disabled := readHistoryCLI(t, c, "compatible", "off.txt"); len(disabled.Lineages) != 0 {
		t.Fatalf("CLI policy did not disable capture: %+v", disabled)
	}
}
