package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestOutputModeIsExplicit(t *testing.T) {
	for _, machine := range []bool{false, true} {
		a := app{options: cliOptions{json: machine}}
		out, err := captureStdout(t, func() error {
			return a.output(map[string]any{"bytes": 3}, "Wrote 3 bytes.\n")
		})
		if err != nil {
			t.Fatal(err)
		}
		if machine {
			if out != "{\"bytes\":3}\n" {
				t.Fatalf("machine response changed: %q", out)
			}
		} else if out != "Wrote 3 bytes.\n" || json.Valid([]byte(out)) {
			t.Fatalf("default output must be readable text: %q", out)
		}
	}
}

func TestTextOutputPreservesRowBoundaries(t *testing.T) {
	name := "one\nsecond\t\x1b[2J.txt"
	out := textTable([]string{"DIRECTORY", "STATE"}, [][]string{{name, "running"}})
	if strings.Contains(out, name) || strings.ContainsRune(out, '\x1b') || strings.Count(out, "\n") != 2 {
		t.Fatalf("filename changed terminal rows or control state: %q", out)
	}
	if !strings.Contains(out, `one\nsecond\t\x1b[2J.txt`) || !strings.Contains(out, "running") {
		t.Fatalf("directory name or state lost: %q", out)
	}
}

func TestStatusTextDistinguishesUnavailableFromIdle(t *testing.T) {
	row := map[string]any{"workspace": "demo", "directory": "/tmp/demo", "state": "stopped", "pid": 10,
		"redis": "redis://localhost:6379/0", "error": "daemon stopped; local changes may be pending"}
	out := formatMountStatus([]map[string]any{row}, true)
	if !strings.Contains(out, "unknown") || !strings.Contains(out, row["error"].(string)) {
		t.Fatalf("unavailable daemon looks synchronized: %s", out)
	}
	row["state"] = "running"
	delete(row, "error")
	row["sync"] = &syncStatus{Connected: false, Queued: 7, TrackedUploads: 2, Entries: 42, Conflicts: 3, LastError: "connection lost"}
	out = formatMountStatus([]map[string]any{row}, true)
	for _, expected := range []string{"disconnected", "Queued:", "7", "Uploads:", "2", "Entries:", "42", "Conflicts:", "3", "connection lost"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("status lost %q: %s", expected, out)
		}
	}
	t.Run("compact overview preserves unhealthy states", func(t *testing.T) {
		for _, state := range []string{"stopped", "unresponsive", "unavailable", "running"} {
			row["state"] = state
			out := formatMountStatus([]map[string]any{row}, false)
			want := state
			if state == "running" {
				want = "disconnected"
			}
			if !strings.Contains(out, want) || !strings.Contains(out, "\nErrors:\n") || !strings.Contains(out, "/tmp/demo:") || !strings.Contains(out, "connection lost") {
				t.Fatalf("overview hides state or error: %s", out)
			}
		}
		delete(row, "sync")
		out := formatMountStatus([]map[string]any{row}, false)
		if !strings.Contains(out, "unknown") || strings.Contains(out, "connected") {
			t.Fatalf("missing live status looks connected: %s", out)
		}
	})
	t.Run("home mount overview fits 80 columns", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		row["directory"] = filepath.Join(home, "demo")
		row["sync"] = &syncStatus{Connected: true}
		out := formatMountStatus([]map[string]any{row}, false)
		if !strings.Contains(out, "~/demo") || !strings.Contains(out, "connected") || strings.Contains(out, "REDIS") || strings.Contains(out, "STATE") || strings.Contains(out, "CONNECTION") || strings.Contains(out, "Errors:") {
			t.Fatalf("overview still includes full diagnostic columns: %s", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if len(line) > 80 {
				t.Fatalf("overview wraps at 80 columns: %q", line)
			}
		}
		out = formatMountStatus([]map[string]any{row}, true)
		for _, want := range []string{row["directory"].(string), row["redis"].(string), "State:", "running", "Connection:", "connected"} {
			if !strings.Contains(out, want) {
				t.Fatalf("detailed status lost %q: %s", want, out)
			}
		}
		row["directory"] = home + "-other/demo"
		out = formatMountStatus([]map[string]any{row}, false)
		if !strings.Contains(out, row["directory"].(string)) || strings.Contains(out, "~/") {
			t.Fatalf("home abbreviation changed a sibling path: %s", out)
		}
	})
}

func TestEmptyCheckpointText(t *testing.T) {
	if out := formatCheckpoints(nil); !strings.Contains(strings.ToLower(out), "no checkpoints") || json.Valid([]byte(out)) {
		t.Fatalf("empty checkpoint list is not readable: %q", out)
	}
	out := formatCheckpoint("demo", controlplane.SavepointMeta{Name: "initial", ID: "cp1"}, controlplane.Manifest{})
	for _, expected := range []string{"demo", "initial", "cp1", "Files:", "Bytes:", "(empty)"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("empty checkpoint lost %q: %s", expected, out)
		}
	}
}

func TestWorkspaceTextDistinguishesHeadFromDefault(t *testing.T) {
	out := formatWorkspace(controlplane.WorkspaceMeta{Name: "demo", HeadSavepoint: "new-head", DefaultSavepoint: "initial"})
	for _, expected := range []string{"Checkpoint head:", "new-head", "Default checkpoint:", "initial"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("workspace checkpoint labels lost %q: %s", expected, out)
		}
	}
	if strings.Contains(out, "Latest checkpoint") {
		t.Fatalf("default checkpoint is not necessarily latest: %s", out)
	}
}
