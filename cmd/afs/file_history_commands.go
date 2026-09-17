package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func (a *app) historyFileCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(historyCommandUsage)
	}
	action := args[0]
	if action != "list" && action != "show" && action != "diff" && action != "restore" && action != "undelete" {
		return fmt.Errorf("unknown history command %q", action)
	}
	f := flag.NewFlagSet("history "+action, flag.ContinueOnError)
	var selector controlplane.FileVersionSelector
	var from, to controlplane.FileVersionDiffOperand
	var order, cursor string
	var limit int
	if action == "list" {
		f.StringVar(&order, "order", "desc", "history order")
		f.StringVar(&cursor, "cursor", "", "page cursor")
		f.IntVar(&limit, "limit", 50, "page limit")
	} else if action == "diff" {
		f.StringVar(&from.VersionID, "from-version", "", "source version")
		f.StringVar(&from.FileID, "from-file-id", "", "source lineage")
		f.Int64Var(&from.Ordinal, "from-ordinal", 0, "source ordinal")
		f.StringVar(&from.Ref, "from-ref", "", "source live/checkpoint ref")
		f.StringVar(&to.VersionID, "to-version", "", "destination version")
		f.StringVar(&to.FileID, "to-file-id", "", "destination lineage")
		f.Int64Var(&to.Ordinal, "to-ordinal", 0, "destination ordinal")
		f.StringVar(&to.Ref, "to-ref", "", "destination live/checkpoint ref")
	} else {
		f.StringVar(&selector.VersionID, "version", "", "version ID")
		f.StringVar(&selector.FileID, "file-id", "", "file lineage")
		f.Int64Var(&selector.Ordinal, "ordinal", 0, "ordinal")
	}
	pos, err := parseCommandFlags(f, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New(historySubcommandUsage[action])
	}
	name, err := filehistory.NormalizePath(pos[1])
	if err != nil {
		return err
	}
	if action == "list" && (limit < 1 || limit > 1000 || (order != "asc" && order != "desc")) {
		return fmt.Errorf("--limit must be 1..1000 and --order asc or desc")
	}
	if action == "show" || action == "restore" || action == "undelete" {
		if err := validateFileSelector(selector, action == "undelete"); err != nil {
			return err
		}
	}
	if action == "diff" {
		if err := validateDiffSelector(from, false); err != nil {
			return err
		}
		if err := validateDiffSelector(to, true); err != nil {
			return err
		}
	}
	ctx := context.Background()
	if _, err := a.historyWorkspace(ctx, pos[0]); err != nil {
		return err
	}
	switch action {
	case "list":
		history, err := a.service.GetFileHistoryPage(ctx, pos[0], controlplane.FileHistoryRequest{Path: name, NewestFirst: order == "desc", Limit: limit, Cursor: cursor})
		if errors.Is(err, os.ErrNotExist) && cursor == "" {
			if _, liveErr := a.service.GetFileContent(ctx, pos[0], "working-copy", name); liveErr == nil {
				return a.output(controlplane.FileHistoryResponse{WorkspaceID: pos[0], Path: name, Order: order, Lineages: []controlplane.FileHistoryLineage{}}, "No file history recorded yet.\n")
			}
		}
		if err != nil {
			return err
		}
		return a.output(history, formatFileHistory(history))
	case "show":
		var result controlplane.FileVersionContentResponse
		if selector.VersionID != "" {
			result, err = a.service.GetFileVersionContent(ctx, pos[0], selector.VersionID)
		} else {
			result, err = a.service.GetFileVersionContentAtOrdinal(ctx, pos[0], selector.FileID, selector.Ordinal)
		}
		if err != nil {
			return err
		}
		if result.Path != name {
			return fmt.Errorf("version belongs to %q", result.Path)
		}
		output := result.Content
		if result.Binary {
			output = fmt.Sprintf("Binary version %s (%d bytes). Use history export --to for exact bytes.\n", result.VersionID, result.Size)
		}
		if result.MetadataOnly {
			output = fmt.Sprintf("Version %s has metadata only; content exceeded the configured cutoff.\n", result.VersionID)
		}
		return a.output(result, output)
	case "diff":
		result, err := a.service.DiffFileVersions(ctx, pos[0], name, from, to)
		if err != nil {
			return err
		}
		output := result.Diff
		if result.Binary {
			output = "Binary versions; a text diff is unavailable.\n"
		}
		return a.output(result, output)
	case "restore":
		result, err := a.service.RestoreFileVersion(ctx, pos[0], name, selector)
		if err != nil {
			return err
		}
		return a.output(result, fmt.Sprintf("Restored version %s to %s.\n", result.RestoredFromVersionID, name))
	case "undelete":
		result, err := a.service.UndeleteFileVersion(ctx, pos[0], name, selector)
		if err != nil {
			return err
		}
		return a.output(result, fmt.Sprintf("Undeleted %s from version %s.\n", name, result.UndeletedFromVersionID))
	}
	return nil
}

func formatFileHistory(history controlplane.FileHistoryResponse) string {
	rows := [][]string{}
	for _, lineage := range history.Lineages {
		for _, version := range lineage.Versions {
			content := "available"
			if version.Kind == controlplane.FileVersionKindTombstone {
				content = "deleted"
			} else if version.MetadataOnly {
				content = "metadata only"
			}
			rows = append(rows, []string{lineage.FileID, strconv.FormatInt(version.Ordinal, 10), version.VersionID, version.Op, version.Path, textTime(version.CreatedAt), content})
		}
	}
	out := "No file history recorded yet.\n"
	if len(rows) > 0 {
		out = textTable([]string{"FILE ID", "ORDINAL", "VERSION", "OPERATION", "PATH", "CREATED", "CONTENT"}, rows)
	}
	if history.NextCursor != "" {
		out += "\nNext page: --cursor " + history.NextCursor + "\n"
	}
	return out
}

func validateFileSelector(selector controlplane.FileVersionSelector, optional bool) error {
	if selector == (controlplane.FileVersionSelector{}) && optional {
		return nil
	}
	if strings.TrimSpace(selector.VersionID) != "" && selector.FileID == "" && selector.Ordinal == 0 {
		return nil
	}
	if selector.VersionID == "" && strings.TrimSpace(selector.FileID) != "" && selector.Ordinal > 0 {
		return nil
	}
	return fmt.Errorf("choose --version or --file-id with a positive --ordinal")
}
func validateDiffSelector(operand controlplane.FileVersionDiffOperand, optional bool) error {
	if operand.Ref != "" {
		if operand.VersionID != "" || operand.FileID != "" || operand.Ordinal != 0 {
			return fmt.Errorf("diff ref cannot be combined with a version selector")
		}
		return nil
	}
	return validateFileSelector(controlplane.FileVersionSelector{VersionID: operand.VersionID, FileID: operand.FileID, Ordinal: operand.Ordinal}, optional)
}
