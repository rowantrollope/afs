package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/rowantrollope/afs/internal/controlplane"
)

// Retain the original CLI's plain tables and labeled sections, using the
// standard library in place of its styled UI dependencies.
func textTable(headers []string, rows [][]string) string {
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	writeRow := func(row []string) {
		for i, value := range row {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, textCell(value))
		}
		fmt.Fprintln(tw)
	}
	if len(headers) > 0 {
		writeRow(headers)
	}
	for _, row := range rows {
		writeRow(row)
	}
	_ = tw.Flush() // The underlying strings.Builder cannot fail.
	return out.String()
}

func textCell(value string) string {
	if value == "" {
		return "-"
	}
	// Paths and errors can contain tabs, newlines or terminal control bytes.
	// Quote those values so one value cannot create extra rows or hide output.
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func textTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatWorkspaces(workspaces []controlplane.WorkspaceMeta) string {
	if len(workspaces) == 0 {
		return "No workspaces.\n"
	}
	rows := make([][]string, 0, len(workspaces))
	for _, ws := range workspaces {
		rows = append(rows, []string{ws.Name, textTime(ws.UpdatedAt), ws.HeadSavepoint})
	}
	return textTable([]string{"NAME", "UPDATED", "CHECKPOINT"}, rows)
}

func formatWorkspace(ws controlplane.WorkspaceMeta) string {
	return textTable(nil, [][]string{
		{"Workspace:", ws.Name}, {"ID:", ws.ID},
		{"Created:", textTime(ws.CreatedAt)}, {"Updated:", textTime(ws.UpdatedAt)},
		{"Checkpoint head:", ws.HeadSavepoint}, {"Default checkpoint:", ws.DefaultSavepoint},
	})
}

func formatCheckpoints(checkpoints []controlplane.SavepointMeta) string {
	if len(checkpoints) == 0 {
		return "No checkpoints.\n"
	}
	rows := make([][]string, 0, len(checkpoints))
	for _, cp := range checkpoints {
		rows = append(rows, []string{cp.Name, cp.ID, textTime(cp.CreatedAt), strconv.Itoa(cp.FileCount),
			strconv.Itoa(cp.DirCount), strconv.FormatInt(cp.TotalBytes, 10)})
	}
	return textTable([]string{"NAME", "ID", "CREATED", "FILES", "DIRECTORIES", "BYTES"}, rows)
}

func formatCheckpoint(workspace string, cp controlplane.SavepointMeta, manifest controlplane.Manifest) string {
	summary := textTable(nil, [][]string{
		{"Checkpoint:", cp.Name}, {"ID:", cp.ID}, {"Workspace:", workspace},
		{"Created:", textTime(cp.CreatedAt)}, {"Parent:", cp.ParentSavepoint},
		{"Files:", strconv.Itoa(cp.FileCount)}, {"Directories:", strconv.Itoa(cp.DirCount)},
		{"Bytes:", strconv.FormatInt(cp.TotalBytes, 10)},
	})
	if len(manifest.Entries) == 0 {
		return summary + "\n(empty)\n"
	}
	paths := make([]string, 0, len(manifest.Entries))
	for path := range manifest.Entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	rows := make([][]string, 0, len(paths))
	for _, path := range paths {
		entry := manifest.Entries[path]
		rows = append(rows, []string{path, entry.Type, fmt.Sprintf("%04o", entry.Mode&0o7777),
			strconv.FormatInt(entry.Size, 10), entry.Target})
	}
	return summary + "\n" + textTable([]string{"PATH", "TYPE", "MODE", "BYTES", "TARGET"}, rows)
}

func formatRestore(result controlplane.RestoreCheckpointResult) string {
	text := fmt.Sprintf("Restored checkpoint %q in workspace %q.\n", result.CheckpointID, result.WorkspaceName)
	if result.SafetyCheckpointCreated {
		text += fmt.Sprintf("Safety checkpoint: %s\n", result.SafetyCheckpointID)
	}
	return text
}

func formatUnmount(root string, force bool) string {
	if force {
		return fmt.Sprintf("Detached %q without flushing; pending changes may exist only locally.\n", root)
	}
	return fmt.Sprintf("Unmounted %q; pending changes flushed. Local files preserved.\n", root)
}

func formatMountStatus(mounts []map[string]any, detailed bool) string {
	if len(mounts) == 0 {
		return "No mounts.\n"
	}
	rows := make([][]string, 0, len(mounts))
	for _, mount := range mounts {
		backend, _ := mount["backend"].(string)
		if backend == "" {
			backend = "sync"
		}
		connection, queued, uploads, entries, conflicts := "unknown", "-", "-", "-", "-"
		lastError, _ := mount["error"].(string)
		if status, ok := mount["sync"].(*syncStatus); ok && status != nil {
			connection = "disconnected"
			if status.Connected {
				connection = "connected"
			}
			queued, uploads, entries = strconv.Itoa(status.Queued), strconv.Itoa(status.TrackedUploads), strconv.Itoa(status.Entries)
			conflicts = strconv.FormatUint(status.Conflicts, 10)
			lastError = status.LastError
		}
		if status, ok := mount["native"].(map[string]any); ok {
			connection = "disconnected"
			if status["connected"] == true {
				connection = "connected"
			}
		}
		if detailed {
			if backend, ok := mount["backend"].(string); ok && (backend == "fuse" || backend == "nfs") {
				return textTable(nil, [][]string{
					{"Workspace:", fmt.Sprint(mount["workspace"])}, {"Directory:", fmt.Sprint(mount["directory"])},
					{"Backend:", backend}, {"State:", fmt.Sprint(mount["state"])}, {"Connection:", connection},
					{"Error:", lastError}, {"PID:", fmt.Sprint(mount["pid"])}, {"Redis:", fmt.Sprint(mount["redis"])},
				})
			}
			return textTable(nil, [][]string{
				{"Workspace:", fmt.Sprint(mount["workspace"])}, {"Directory:", fmt.Sprint(mount["directory"])},
				{"State:", fmt.Sprint(mount["state"])}, {"Connection:", connection},
				{"Queued:", queued}, {"Uploads:", uploads}, {"Entries:", entries}, {"Conflicts:", conflicts},
				{"Error:", lastError}, {"PID:", fmt.Sprint(mount["pid"])}, {"Redis:", fmt.Sprint(mount["redis"])},
			})
		}
		rows = append(rows, []string{fmt.Sprint(mount["workspace"]), fmt.Sprint(mount["directory"]),
			backend, fmt.Sprint(mount["state"]), connection, queued, uploads, conflicts, lastError})
	}
	return textTable([]string{"WORKSPACE", "DIRECTORY", "BACKEND", "STATE", "CONNECTION", "QUEUED", "UPLOADS", "CONFLICTS", "ERROR"}, rows)
}
