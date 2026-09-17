package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

const historyCommandUsage = `Usage: afs history <workspace> <path> [--limit N] [--before N] [--file-id ID] [--lineages]

List versions newest first, including renames and deletion records. Paths are
relative to the workspace root. The default selects the latest file lineage;
--lineages lists IDs for earlier files deleted and recreated at the same path.
--file-id selects one of those lineages. --limit defaults to 50 (maximum 1000).
Use the returned next_before as --before to fetch the next page.
History records published mutations, not every transient local edit.
`

const recoverCommandUsage = `Usage: afs recover <workspace> <path> [--version ID-or-ordinal] [--file-id ID] --to <local-path>

Recover historical bytes or a symlink into a new local path. Without --version,
select the latest recoverable version in the selected lineage, even if deleted.
Use history --lineages and --file-id for an earlier incarnation of the path.
The destination must not exist; its parent directory must already exist.
File permissions are preserved. Symlinks are recreated without following them.
Inspect the recovered file, then copy it into a mounted directory to publish it.
`

const versioningCommandUsage = `Usage: afs versioning <workspace> [options]

With no options, show the shared workspace file history policy (default: off).
Options update only the supplied fields and apply to all upgraded writers:
  --mode off|all|paths     Enable all files or matching paths; off stops capture
  --include <glob>        Include pattern for paths mode; repeat for more patterns
  --exclude <glob>        Exclude pattern; repeat for more patterns
  --max-versions N        Maximum retained versions per file (0: unlimited)
  --max-age-days N        Maximum age of non-head versions (0: unlimited)
  --max-bytes N           Workspace logical history byte budget (0: unlimited)
  --max-file-bytes N      Exclude snapshots above this file size (0: unlimited)
  --prune                Apply retention to existing versions in bounded batches

An include/exclude option replaces that entire list; use --include= or --exclude=
to clear it. Globs are workspace-relative; ** matches across directories.
Upgrade every writer before enabling history. Retention always preserves heads.
`

func (a *app) historyWorkspace(ctx context.Context, workspace string) (string, error) {
	if err := a.connect(ctx); err != nil {
		return "", err
	}
	meta, err := a.service.GetWorkspace(ctx, workspace)
	if err != nil {
		return "", err
	}
	return controlplane.WorkspaceStorageID(meta), nil
}

func (a *app) historyCommand(args []string) error {
	f := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := f.Int("limit", 50, "page size")
	before := f.Int64("before", 0, "exclusive cursor")
	fileID := f.String("file-id", "", "file lineage")
	lineages := f.Bool("lineages", false, "list file lineages")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New(historyCommandUsage)
	}
	if *limit < 1 || *limit > 1000 || *before < 0 {
		return errors.New("--limit must be between 1 and 1000; --before must be non-negative")
	}
	if *lineages && *fileID != "" {
		return errors.New("--lineages cannot be combined with --file-id")
	}
	path, err := filehistory.NormalizePath(pos[1])
	if err != nil {
		return err
	}
	ctx := context.Background()
	id, err := a.historyWorkspace(ctx, pos[0])
	if err != nil {
		return err
	}
	if *lineages {
		page, err := filehistory.Lineages(ctx, a.rdb, id, path, *limit, *before)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(page.Files))
		for _, lineage := range page.Files {
			rows = append(rows, []string{lineage.FileID, strconv.FormatInt(lineage.Sequence, 10)})
		}
		output := "No file history.\n"
		if len(rows) > 0 {
			output = textTable([]string{"FILE ID", "SEQUENCE"}, rows)
		}
		return a.output(page, output+historyNextPage(page.NextBefore))
	}
	page, err := filehistory.List(ctx, a.rdb, id, path, *limit, *before, *fileID)
	if err != nil {
		return err
	}
	return a.output(page, formatFileHistory(page))
}

func historyNextPage(before int64) string {
	if before == 0 {
		return ""
	}
	return fmt.Sprintf("\nNext page: --before %d\n", before)
}

func formatFileHistory(page filehistory.Page) string {
	if len(page.Versions) == 0 {
		return "No file history.\n"
	}
	rows := make([][]string, 0, len(page.Versions))
	for _, record := range page.Versions {
		content := "available"
		if record.Deleted {
			content = "deleted"
		} else if record.Type == "file" && record.ContentRef == "" {
			content = "excluded"
		}
		rows = append(rows, []string{strconv.FormatInt(record.Version, 10), record.ID,
			textTime(time.UnixMilli(record.CreatedAt)), record.Operation, record.Path,
			strconv.FormatInt(record.Size, 10), content})
	}
	return fmt.Sprintf("File ID: %s\n\n", textCell(page.FileID)) +
		textTable([]string{"VERSION", "ID", "CREATED", "OPERATION", "PATH", "BYTES", "CONTENT"}, rows) +
		historyNextPage(page.NextBefore)
}

type historyGlobs []string

func (g *historyGlobs) String() string { return strings.Join(*g, ", ") }
func (g *historyGlobs) Set(value string) error {
	if value != "" {
		*g = append(*g, value)
	}
	return nil
}

func (a *app) versioningCommand(args []string) error {
	f := flag.NewFlagSet("versioning", flag.ContinueOnError)
	mode := f.String("mode", "", "capture mode")
	var include, exclude historyGlobs
	f.Var(&include, "include", "include pattern")
	f.Var(&exclude, "exclude", "exclude pattern")
	maxVersions := f.Int("max-versions", 0, "maximum versions per file")
	maxAge := f.Int("max-age-days", 0, "maximum version age")
	maxBytes := f.Int64("max-bytes", 0, "logical byte budget")
	maxFile := f.Int64("max-file-bytes", 0, "maximum captured file bytes")
	prune := f.Bool("prune", false, "apply retention")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(versioningCommandUsage)
	}
	changed := make(map[string]bool)
	f.Visit(func(option *flag.Flag) { changed[option.Name] = true })
	if changed["mode"] && *mode != "off" && *mode != "all" && *mode != "paths" {
		return errors.New("--mode must be off, all, or paths")
	}
	if *maxVersions < 0 || *maxAge < 0 || *maxBytes < 0 || *maxFile < 0 {
		return errors.New("history retention limits must be non-negative")
	}
	ctx := context.Background()
	id, err := a.historyWorkspace(ctx, pos[0])
	if err != nil {
		return err
	}
	delete(changed, "prune")
	var policy filehistory.Policy
	if len(changed) > 0 {
		policy, err = filehistory.UpdatePolicy(ctx, a.rdb, id, func(policy filehistory.Policy) (filehistory.Policy, error) {
			if changed["mode"] {
				policy.Mode = *mode
			}
			if changed["include"] {
				policy.Include = include
			}
			if changed["exclude"] {
				policy.Exclude = exclude
			}
			if changed["max-versions"] {
				policy.MaxVersions = *maxVersions
			}
			if changed["max-age-days"] {
				policy.MaxAgeDays = *maxAge
			}
			if changed["max-bytes"] {
				policy.MaxBytes = *maxBytes
			}
			if changed["max-file-bytes"] {
				policy.MaxFileBytes = *maxFile
			}
			return policy, nil
		})
	} else {
		policy, err = filehistory.GetPolicy(ctx, a.rdb, id)
	}
	if err != nil {
		return err
	}
	trimmed := 0
	pruneMore := false
	if *prune {
		// Each call holds Redis only for a bounded cleanup batch. Keep the CLI
		// bounded as well when other writers are continually adding versions.
		for batch := 0; batch < 100; batch++ {
			result, err := filehistory.PrunePage(ctx, a.rdb, id, 100)
			if err != nil {
				return err
			}
			trimmed += result.Removed
			pruneMore = result.More
			if !result.More {
				break
			}
		}
	}
	output := textTable(nil, [][]string{
		{"Workspace:", pos[0]}, {"Mode:", policy.Mode},
		{"Include:", strings.Join(policy.Include, ", ")}, {"Exclude:", strings.Join(policy.Exclude, ", ")},
		{"Maximum versions:", strconv.Itoa(policy.MaxVersions)}, {"Maximum age (days):", strconv.Itoa(policy.MaxAgeDays)},
		{"Maximum logical bytes:", strconv.FormatInt(policy.MaxBytes, 10)}, {"Maximum file bytes:", strconv.FormatInt(policy.MaxFileBytes, 10)},
	})
	if *prune {
		output += fmt.Sprintf("Pruned %d versions.\n", trimmed)
		if pruneMore {
			output += "More history remains to inspect; run --prune again to continue.\n"
		}
	}
	return a.output(struct {
		Workspace string             `json:"workspace"`
		Policy    filehistory.Policy `json:"policy"`
		Pruned    int                `json:"pruned,omitempty"`
		PruneMore bool               `json:"prune_more,omitempty"`
	}{pos[0], policy, trimmed, pruneMore}, output)
}

func (a *app) recoverCommand(args []string) error {
	f := flag.NewFlagSet("recover", flag.ContinueOnError)
	selector := f.String("version", "", "version ID or ordinal")
	fileID := f.String("file-id", "", "file lineage")
	destination := f.String("to", "", "new local destination")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *destination == "" {
		return errors.New(recoverCommandUsage)
	}
	path, err := filehistory.NormalizePath(pos[1])
	if err != nil {
		return err
	}
	local, err := expandPath(*destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(local); err == nil {
		return fmt.Errorf("recovery destination %q already exists", local)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx := context.Background()
	id, err := a.historyWorkspace(ctx, pos[0])
	if err != nil {
		return err
	}
	record, content, err := filehistory.Get(ctx, a.rdb, id, path, *selector, *fileID)
	if err != nil {
		return err
	}
	if err := writeRecoveredVersion(local, record, content); err != nil {
		return err
	}
	return a.output(map[string]any{"workspace": pos[0], "path": path, "version": record, "destination": local},
		fmt.Sprintf("Recovered version %q to %q.\n", record.ID, local))
}

func writeRecoveredVersion(destination string, record filehistory.Record, content []byte) error {
	if record.Deleted {
		return errors.New("a deletion record has no content; select an earlier version")
	}
	switch record.Type {
	case "symlink":
		return os.Symlink(record.Target, destination)
	case "file":
		if record.ContentRef == "" {
			return errors.New("content was excluded by the file history policy")
		}
	default:
		return fmt.Errorf("cannot recover file type %q", record.Type)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Never remove or reopen this pathname on failure: another local process
	// could have replaced it. All writes and permission changes use our handle.
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Chmod(os.FileMode(record.Mode)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
