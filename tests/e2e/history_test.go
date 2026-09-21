//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func readHistoryCLI(t *testing.T, c *cli, workspace, path string, flags ...string) controlplane.FileHistoryResponse {
	t.Helper()
	var page controlplane.FileHistoryResponse
	raw := c.run(nil, append([]string{"--json", "history", "list", workspace, path}, flags...)...)
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func historyLineage(t *testing.T, page controlplane.FileHistoryResponse, fileID string) controlplane.FileHistoryLineage {
	t.Helper()
	for _, lineage := range page.Lineages {
		if fileID == "" || lineage.FileID == fileID {
			return lineage
		}
	}
	t.Fatalf("missing lineage %q in %+v", fileID, page)
	return controlplane.FileHistoryLineage{}
}

func TestFileHistoryCLIRecoveryAndLineages(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "versions")
	root := filepath.Join(t.TempDir(), "mounted")
	c.mount("versions", root)
	flush := func() { t.Helper(); c.run(nil, "sync", "--wait", root, "--timeout", "20s") }
	history := func(path string, flags ...string) controlplane.FileHistoryResponse {
		t.Helper()
		return readHistoryCLI(t, c, "versions", path, flags...)
	}
	file := filepath.Join(root, "file")
	original := []byte{0, 255, '\n', 128, 'a'}
	write(t, file, original)
	if err := os.Chmod(file, 0o640); err != nil {
		t.Fatal(err)
	}
	flush()
	if page := history("file"); len(page.Lineages) != 0 {
		t.Fatalf("history should default off: %+v", page)
	}
	// An already-running upgraded writer must observe the shared policy.
	c.run(nil, "history", "policy", "versions", "--mode", "all")
	checkpoints := c.run(nil, "--json", "checkpoint", "list", "versions")
	write(t, file, []byte("updated"))
	flush()
	page := historyLineage(t, history("file"), "")
	if len(page.Versions) < 2 {
		t.Fatalf("missing first-overwrite baseline: %+v", page)
	}
	first := page.Versions[len(page.Versions)-1]
	if first.SizeBytes != int64(len(original)) {
		t.Fatalf("baseline size: %+v", first)
	}
	out := filepath.Join(t.TempDir(), "binary")
	c.run(nil, "history", "export", "versions", "file", "--version", first.VersionID, "--to", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("historical binary %v %v", got, err)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("historical mode %v %v", info, err)
	}
	if failure := c.mustFail("history", "export", "versions", "file", "--version", first.VersionID, "--to", out); !strings.Contains(failure, "already exists") {
		t.Fatalf("existing destination error: %s", failure)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, original) {
		t.Fatal("existing recovered bytes replaced")
	}
	// Verify actual CLI pagination and ordinal selectors.
	one := history("file", "--limit", "1")
	if len(one.Lineages) != 1 || len(one.Lineages[0].Versions) != 1 || one.NextCursor == "" {
		t.Fatalf("first page: %+v", one)
	}
	two := history("file", "--limit", "1", "--cursor", one.NextCursor)
	if len(two.Lineages) != 1 || len(two.Lineages[0].Versions) != 1 || two.Lineages[0].Versions[0].Ordinal >= one.Lineages[0].Versions[0].Ordinal {
		t.Fatalf("next page: %+v", two)
	}
	ordinalOut := filepath.Join(t.TempDir(), "ordinal")
	c.run(nil, "history", "export", "versions", "file", "--version", strconv.FormatInt(first.Ordinal, 10), "--to", ordinalOut)
	if got, _ := os.ReadFile(ordinalOut); !bytes.Equal(got, original) {
		t.Fatalf("ordinal bytes %v", got)
	}

	write(t, filepath.Join(root, "empty"), nil)
	if err := os.Symlink("../unfollowed-target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	flush()
	emptyOut, linkOut := filepath.Join(t.TempDir(), "empty"), filepath.Join(t.TempDir(), "link")
	c.run(nil, "history", "export", "versions", "empty", "--to", emptyOut)
	c.run(nil, "history", "export", "versions", "link", "--to", linkOut)
	if got, err := os.ReadFile(emptyOut); err != nil || len(got) != 0 {
		t.Fatalf("empty recovery %v %v", got, err)
	}
	if target, err := os.Readlink(linkOut); err != nil || target != "../unfollowed-target" {
		t.Fatalf("symlink recovery %q %v", target, err)
	}

	oldID := page.FileID
	renamed := filepath.Join(root, "renamed")
	if err := os.Rename(file, renamed); err != nil {
		t.Fatal(err)
	}
	flush()
	moved := historyLineage(t, history("renamed"), "")
	if len(moved.Versions) == 0 {
		t.Fatal("renamed file has no published history")
	}
	for _, version := range moved.Versions {
		if version.Op == "rename" && moved.FileID != oldID {
			t.Fatalf("published rename lost lineage: %+v", moved)
		}
	}
	// Sync may discover a rapid local rename as deletion plus creation. The
	// accepted publication determines lineage; the old bytes remain recoverable.
	priorOut := filepath.Join(t.TempDir(), "before-local-rename")
	c.run(nil, "history", "export", "versions", "file", "--file-id", oldID, "--to", priorOut)
	if got, err := os.ReadFile(priorOut); err != nil || string(got) != "updated" {
		t.Fatalf("pre-rename history %q %v", got, err)
	}
	oldID = moved.FileID
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	flush()
	deleted := historyLineage(t, history("renamed"), "")
	if len(deleted.Versions) == 0 || deleted.Versions[0].Kind != controlplane.FileVersionKindTombstone || deleted.State != controlplane.FileLineageStateDeleted {
		t.Fatalf("missing tombstone: %+v", deleted)
	}
	deletedOut := filepath.Join(t.TempDir(), "undeleted")
	c.run(nil, "history", "export", "versions", "renamed", "--to", deletedOut)
	if got, err := os.ReadFile(deletedOut); err != nil || string(got) != "updated" {
		t.Fatalf("deleted recovery %q %v", got, err)
	}
	write(t, renamed, []byte("different file"))
	flush()
	if recreated := historyLineage(t, history("renamed"), ""); recreated.FileID == oldID {
		t.Fatalf("recreated file reused lineage: %+v", recreated)
	}
	lineages := history("renamed")
	if len(lineages.Lineages) != 2 {
		t.Fatalf("incarnations: %+v", lineages)
	}
	if prior := historyLineage(t, lineages, oldID); prior.State != controlplane.FileLineageStateDeleted {
		t.Fatalf("older incarnation not marked deleted: %+v", prior)
	}
	oldOut := filepath.Join(t.TempDir(), "older-incarnation")
	c.run(nil, "history", "export", "versions", "renamed", "--file-id", oldID, "--to", oldOut)
	if got, _ := os.ReadFile(oldOut); string(got) != "updated" {
		t.Fatalf("old lineage bytes %q", got)
	}
	if after := c.run(nil, "--json", "checkpoint", "list", "versions"); !bytes.Equal(checkpoints, after) {
		t.Fatal("file history modified checkpoints")
	}

	c.run(nil, "history", "policy", "versions", "--max-versions", "1", "--prune")
	if retained := historyLineage(t, history("renamed"), oldID); len(retained.Versions) != 2 || retained.Versions[0].Kind != controlplane.FileVersionKindTombstone || retained.Versions[1].Kind == controlplane.FileVersionKindTombstone {
		t.Fatalf("count retention: %+v", retained)
	}
	c.run(nil, "history", "policy", "versions", "--mode", "off")
	beforeOff := historyLineage(t, history("renamed"), "")
	write(t, renamed, []byte("capture off"))
	flush()
	afterOff := historyLineage(t, history("renamed"), beforeOff.FileID)
	if len(afterOff.Versions) != len(beforeOff.Versions) || afterOff.Versions[0].VersionID != beforeOff.Versions[0].VersionID {
		t.Fatalf("off still captured: %+v", afterOff)
	}
}

func TestFileHistoryCLIPolicyFiltersAndSizeExclusions(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "filtered")
	update := func(flags ...string) filehistory.Policy {
		t.Helper()
		var result struct {
			Policy filehistory.Policy `json:"policy"`
		}
		if err := json.Unmarshal(c.run(nil, append([]string{"--json", "history", "policy", "filtered"}, flags...)...), &result); err != nil {
			t.Fatal(err)
		}
		return result.Policy
	}
	if p := update(); p.Mode != "off" {
		t.Fatalf("default policy %+v", p)
	}
	p := update("--mode", "paths", "--include", "notes/**", "--include", "docs/**", "--exclude", "notes/private/**", "--max-file-bytes", "4")
	if len(p.Include) != 2 || len(p.Exclude) != 1 || p.MaxFileBytes != 4 {
		t.Fatalf("saved policy %+v", p)
	}
	if p := update("--max-versions", "10"); p.Mode != "paths" || len(p.Include) != 2 || p.MaxFileBytes != 4 {
		t.Fatalf("partial update reset fields %+v", p)
	}
	root := filepath.Join(t.TempDir(), "mounted")
	c.mount("filtered", root)
	write(t, filepath.Join(root, "notes", "small"), []byte("one"))
	write(t, filepath.Join(root, "notes", "private", "secret"), []byte("hide"))
	write(t, filepath.Join(root, "outside"), []byte("skip"))
	write(t, filepath.Join(root, "docs", "large"), []byte("oversized"))
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	page := func(path string) controlplane.FileHistoryResponse {
		t.Helper()
		return readHistoryCLI(t, c, "filtered", path)
	}
	for _, path := range []string{"notes/private/secret", "outside"} {
		if got := page(path); len(got.Lineages) != 0 {
			t.Fatalf("excluded %s captured: %+v", path, got)
		}
	}
	if got := historyLineage(t, page("docs/large"), ""); len(got.Versions) != 1 || !got.Versions[0].MetadataOnly || got.Versions[0].SizeBytes != 9 || got.Versions[0].ContentHash == "" {
		t.Fatalf("oversized file metadata missing: %+v", got)
	}
	if got := page("notes/small"); len(got.Lineages) == 0 {
		t.Fatal("included file missing")
	}
	write(t, filepath.Join(root, "notes", "small"), []byte("now oversized"))
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	if got, err := c.remote("filtered", "notes/small"); err != nil || string(got) != "now oversized" {
		t.Fatalf("size exclusion blocked live publication %q %v", got, err)
	}
	destination := filepath.Join(t.TempDir(), "last-captured")
	c.run(nil, "history", "export", "filtered", "notes/small", "--to", destination)
	if got, err := os.ReadFile(destination); err != nil || string(got) != "one" {
		t.Fatalf("last captured version %q %v", got, err)
	}
	if p := update("--mode", "all", "--include=", "--exclude=", "--max-file-bytes", "0"); len(p.Include) != 0 || len(p.Exclude) != 0 || p.MaxFileBytes != 0 {
		t.Fatalf("cleared policy %+v", p)
	}
	write(t, filepath.Join(root, "outside"), []byte("tracked after policy update"))
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	if got := historyLineage(t, page("outside"), ""); len(got.Versions) < 2 {
		t.Fatalf("updated shared policy not honored: %+v", got)
	}
}
