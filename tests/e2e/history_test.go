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

	"github.com/rowantrollope/afs/internal/filehistory"
)

func TestFileHistoryCLIRecoveryAndLineages(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "versions")
	root := filepath.Join(t.TempDir(), "mounted")
	c.mount("versions", root)
	flush := func() { t.Helper(); c.run(nil, "sync", "--wait", root, "--timeout", "20s") }
	history := func(path string, flags ...string) filehistory.Page {
		t.Helper()
		var page filehistory.Page
		raw := c.run(nil, append([]string{"--json", "history", "versions", path}, flags...)...)
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	file := filepath.Join(root, "file")
	original := []byte{0, 255, '\n', 128, 'a'}
	write(t, file, original)
	if err := os.Chmod(file, 0o640); err != nil {
		t.Fatal(err)
	}
	flush()
	if page := history("file"); len(page.Versions) != 0 {
		t.Fatalf("history should default off: %+v", page)
	}
	// An already-running upgraded writer must observe the shared policy.
	c.run(nil, "versioning", "versions", "--mode", "all")
	checkpoints := c.run(nil, "--json", "cp", "list", "versions")
	write(t, file, []byte("updated"))
	flush()
	page := history("file")
	if len(page.Versions) < 2 {
		t.Fatalf("missing first-overwrite baseline: %+v", page)
	}
	first := page.Versions[len(page.Versions)-1]
	if first.Size != int64(len(original)) {
		t.Fatalf("baseline size: %+v", first)
	}
	out := filepath.Join(t.TempDir(), "binary")
	c.run(nil, "recover", "versions", "file", "--version", first.ID, "--to", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("historical binary %v %v", got, err)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("historical mode %v %v", info, err)
	}
	if failure := c.mustFail("recover", "versions", "file", "--version", first.ID, "--to", out); !strings.Contains(failure, "already exists") {
		t.Fatalf("existing destination error: %s", failure)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, original) {
		t.Fatal("existing recovered bytes replaced")
	}
	// Verify actual CLI pagination and ordinal selectors.
	one := history("file", "--limit", "1")
	if len(one.Versions) != 1 || one.NextBefore == 0 {
		t.Fatalf("first page: %+v", one)
	}
	two := history("file", "--limit", "1", "--before", strconv.FormatInt(one.NextBefore, 10))
	if len(two.Versions) != 1 || two.Versions[0].Version >= one.Versions[0].Version {
		t.Fatalf("next page: %+v", two)
	}
	ordinalOut := filepath.Join(t.TempDir(), "ordinal")
	c.run(nil, "recover", "versions", "file", "--version", strconv.FormatInt(first.Version, 10), "--to", ordinalOut)
	if got, _ := os.ReadFile(ordinalOut); !bytes.Equal(got, original) {
		t.Fatalf("ordinal bytes %v", got)
	}

	write(t, filepath.Join(root, "empty"), nil)
	if err := os.Symlink("../unfollowed-target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	flush()
	emptyOut, linkOut := filepath.Join(t.TempDir(), "empty"), filepath.Join(t.TempDir(), "link")
	c.run(nil, "recover", "versions", "empty", "--to", emptyOut)
	c.run(nil, "recover", "versions", "link", "--to", linkOut)
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
	moved := history("renamed")
	if len(moved.Versions) == 0 {
		t.Fatal("renamed file has no published history")
	}
	for _, version := range moved.Versions {
		if version.Operation == "rename" && moved.FileID != oldID {
			t.Fatalf("published rename lost lineage: %+v", moved)
		}
	}
	// Sync may discover a rapid local rename as deletion plus creation. The
	// accepted publication determines lineage; the old bytes remain recoverable.
	priorOut := filepath.Join(t.TempDir(), "before-local-rename")
	c.run(nil, "recover", "versions", "file", "--file-id", oldID, "--to", priorOut)
	if got, err := os.ReadFile(priorOut); err != nil || string(got) != "updated" {
		t.Fatalf("pre-rename history %q %v", got, err)
	}
	oldID = moved.FileID
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	flush()
	deleted := history("renamed")
	if len(deleted.Versions) == 0 || !deleted.Versions[0].Deleted {
		t.Fatalf("missing tombstone: %+v", deleted)
	}
	deletedOut := filepath.Join(t.TempDir(), "undeleted")
	c.run(nil, "recover", "versions", "renamed", "--to", deletedOut)
	if got, err := os.ReadFile(deletedOut); err != nil || string(got) != "updated" {
		t.Fatalf("deleted recovery %q %v", got, err)
	}
	write(t, renamed, []byte("different file"))
	flush()
	if recreated := history("renamed"); recreated.FileID == oldID {
		t.Fatalf("recreated file reused lineage: %+v", recreated)
	}
	var lineages filehistory.LineagePage
	if err := json.Unmarshal(c.run(nil, "--json", "history", "versions", "renamed", "--lineages"), &lineages); err != nil {
		t.Fatal(err)
	}
	if len(lineages.Files) != 2 {
		t.Fatalf("incarnations: %+v", lineages)
	}
	oldOut := filepath.Join(t.TempDir(), "older-incarnation")
	c.run(nil, "recover", "versions", "renamed", "--file-id", oldID, "--to", oldOut)
	if got, _ := os.ReadFile(oldOut); string(got) != "updated" {
		t.Fatalf("old lineage bytes %q", got)
	}
	if after := c.run(nil, "--json", "cp", "list", "versions"); !bytes.Equal(checkpoints, after) {
		t.Fatal("file history modified checkpoints")
	}

	c.run(nil, "versioning", "versions", "--max-versions", "1", "--prune")
	if retained := history("renamed", "--file-id", oldID); len(retained.Versions) != 2 || !retained.Versions[0].Deleted || retained.Versions[1].Deleted {
		t.Fatalf("count retention: %+v", retained)
	}
	c.run(nil, "versioning", "versions", "--mode", "off")
	beforeOff := history("renamed")
	write(t, renamed, []byte("capture off"))
	flush()
	afterOff := history("renamed")
	if len(afterOff.Versions) != len(beforeOff.Versions) || afterOff.Versions[0].ID != beforeOff.Versions[0].ID {
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
		if err := json.Unmarshal(c.run(nil, append([]string{"--json", "versioning", "filtered"}, flags...)...), &result); err != nil {
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
	page := func(path string) filehistory.Page {
		t.Helper()
		var page filehistory.Page
		if err := json.Unmarshal(c.run(nil, "--json", "history", "filtered", path), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	for _, path := range []string{"notes/private/secret", "outside"} {
		if got := page(path); len(got.Versions) != 0 {
			t.Fatalf("excluded %s captured: %+v", path, got)
		}
	}
	if got := page("docs/large"); len(got.Versions) != 1 || !got.Versions[0].MetadataOnly || got.Versions[0].Size != 9 || got.Versions[0].ContentHash == "" {
		t.Fatalf("oversized file metadata missing: %+v", got)
	}
	if got := page("notes/small"); len(got.Versions) == 0 {
		t.Fatal("included file missing")
	}
	write(t, filepath.Join(root, "notes", "small"), []byte("now oversized"))
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	if got, err := c.remote("filtered", "notes/small"); err != nil || string(got) != "now oversized" {
		t.Fatalf("size exclusion blocked live publication %q %v", got, err)
	}
	destination := filepath.Join(t.TempDir(), "last-captured")
	c.run(nil, "recover", "filtered", "notes/small", "--to", destination)
	if got, err := os.ReadFile(destination); err != nil || string(got) != "one" {
		t.Fatalf("last captured version %q %v", got, err)
	}
	if p := update("--mode", "all", "--include=", "--exclude=", "--max-file-bytes", "0"); len(p.Include) != 0 || len(p.Exclude) != 0 || p.MaxFileBytes != 0 {
		t.Fatalf("cleared policy %+v", p)
	}
	write(t, filepath.Join(root, "outside"), []byte("tracked after policy update"))
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	if got := page("outside"); len(got.Versions) < 2 {
		t.Fatalf("updated shared policy not honored: %+v", got)
	}
}
