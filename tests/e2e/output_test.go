//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Each term group expresses one useful fact; alternatives permit normal prose,
// tables, or labels without encoding a particular decorative layout in tests.
func readableOutput(t *testing.T, raw []byte, facts ...[]string) {
	t.Helper()
	text := strings.TrimSpace(string(raw))
	if text == "" || !utf8.Valid(raw) {
		t.Errorf("default output must contain readable text: %q", raw)
		return
	}
	if json.Valid(raw) {
		t.Errorf("default output is JSON; readable text is required without --json:\n%s", raw)
	}
	lower := strings.ToLower(text)
	for _, alternatives := range facts {
		found := false
		for _, term := range alternatives {
			if strings.Contains(lower, strings.ToLower(term)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default output is missing useful detail %q:\n%s", alternatives, raw)
		}
	}
}

func TestDefaultCommandOutput(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	file := filepath.Join(t.TempDir(), "source.bin")
	wantBinary := []byte{0, 255, '\n', 128, 'x'}
	write(t, file, wantBinary)
	importRoot := t.TempDir()
	write(t, filepath.Join(importRoot, "imported"), []byte("from directory"))
	type step struct {
		name  string
		args  []string
		input []byte
		facts [][]string
	}
	steps := []step{
		{"empty workspace list", []string{"ws", "list"}, nil, [][]string{{"no workspaces", "empty"}}},
		{"empty mount status", []string{"status"}, nil, [][]string{{"no mounts", "no mounted", "no active", "empty"}}},
		{"ws create", []string{"ws", "create", "friendly"}, nil, [][]string{{"created"}, {"friendly"}}},
		{"ws create from directory", []string{"ws", "create", "import-friendly", "--from", importRoot}, nil, [][]string{{"created", "imported"}, {"import-friendly"}}},
		{"ws list", []string{"ws", "list"}, nil, [][]string{{"friendly"}, {"import-friendly"}}},
		{"ws info", []string{"ws", "info", "friendly"}, nil, [][]string{{"friendly"}, {"id"}, {"head", "checkpoint"}}},
		{"empty file list", []string{"fs", "ls", "friendly"}, nil, [][]string{{"no files", "no entries", "empty"}}},
		{"fs mkdir", []string{"fs", "mkdir", "friendly", "notes"}, nil, [][]string{{"created", "mkdir"}, {"notes"}}},
		{"fs put stdin", []string{"fs", "put", "friendly", "notes/original"}, []byte("one\n"), [][]string{{"wrote", "written", "put"}, {"notes/original"}, {"4"}, {"bytes"}}},
		{"fs put from file", []string{"fs", "put", "friendly", "binary", "--from", file}, nil, [][]string{{"wrote", "written", "put"}, {"binary"}, {"5"}, {"bytes"}}},
		{"fs ls", []string{"fs", "ls", "friendly", "notes"}, nil, [][]string{{"original"}, {"4"}}},
		{"fs mv", []string{"fs", "mv", "friendly", "notes/original", "notes/renamed"}, nil, [][]string{{"moved", "renamed"}, {"notes/original"}, {"notes/renamed"}}},
		{"cp create", []string{"cp", "create", "friendly", "--name", "before-edit"}, nil, [][]string{{"checkpoint"}, {"created"}, {"before-edit"}}},
		{"cp list", []string{"cp", "list", "friendly"}, nil, [][]string{{"before-edit"}}},
		{"cp show", []string{"cp", "show", "friendly", "before-edit"}, nil, [][]string{{"before-edit"}, {"files"}, {"bytes"}, {"notes/renamed"}}},
		{"ws fork", []string{"ws", "fork", "friendly", "friendly-copy", "--checkpoint", "before-edit"}, nil, [][]string{{"forked"}, {"friendly"}, {"friendly-copy"}}},
		{"fs rm", []string{"fs", "rm", "friendly", "notes/renamed"}, nil, [][]string{{"removed", "deleted"}, {"notes/renamed"}}},
		{"cp create next", []string{"cp", "create", "friendly", "--name", "after-edit"}, nil, [][]string{{"checkpoint"}, {"after-edit"}}},
		{"uncheckpointed edit before restore", []string{"fs", "put", "friendly", "pending"}, []byte("preserve in safety checkpoint"), [][]string{{"wrote", "written", "put"}, {"pending"}}},
		{"cp restore", []string{"cp", "restore", "friendly", "before-edit", "--yes"}, nil, [][]string{{"restored"}, {"friendly"}, {"safety"}}},
		{"cp delete", []string{"cp", "delete", "friendly", "after-edit", "--yes"}, nil, [][]string{{"deleted"}, {"checkpoint"}, {"after-edit"}}},
		{"fs rm recursive", []string{"fs", "rm", "friendly", "notes", "--recursive"}, nil, [][]string{{"removed", "deleted"}, {"notes"}}},
		{"ws delete", []string{"ws", "delete", "friendly-copy", "--yes"}, nil, [][]string{{"deleted"}, {"workspace"}, {"friendly-copy"}}},
	}
	for _, s := range steps {
		commandSucceeded := false
		t.Run(s.name, func(t *testing.T) {
			out, diag, err := c.runTimeout(30*time.Second, s.input, s.args...)
			if err != nil {
				t.Fatalf("afs %v: %v\nstdout=%s\nstderr=%s", s.args, err, out, diag)
			}
			commandSucceeded = true
			if len(diag) != 0 {
				t.Errorf("successful command wrote diagnostics: %s", diag)
			}
			readableOutput(t, out, s.facts...)
		})
		if !commandSucceeded {
			return
		}
	}
	t.Run("cat remains exact binary stdout", func(t *testing.T) {
		out, diag, err := c.runTimeout(10*time.Second, nil, "fs", "cat", "friendly", "binary")
		if err != nil || len(diag) != 0 || !bytes.Equal(out, wantBinary) {
			t.Fatalf("cat changed: %v stdout=%x stderr=%s", err, out, diag)
		}
	})
	t.Run("cat remains exact empty stdout", func(t *testing.T) {
		c.run(nil, "fs", "put", "friendly", "empty")
		out, diag, err := c.runTimeout(10*time.Second, nil, "fs", "cat", "friendly", "empty")
		if err != nil || len(diag) != 0 || len(out) != 0 {
			t.Fatalf("empty cat changed: %v stdout=%q stderr=%s", err, out, diag)
		}
	})
}

func TestDefaultMountOutputAndJSONStatus(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "--json", "ws", "create", "visible-mount")
	root := filepath.Join(t.TempDir(), "root")
	// Mount without --json, then obtain PID through the explicit machine API for
	// test-owned process cleanup. Readable output never doubles as a hidden API.
	out := c.run(nil, "mount", "visible-mount", root)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	var status []struct {
		PID       int            `json:"pid"`
		State     string         `json:"state"`
		Workspace string         `json:"workspace"`
		Directory string         `json:"directory"`
		Sync      map[string]any `json:"sync"`
	}
	statusJSON := c.run(nil, "--json", "status", root)
	if err := json.Unmarshal(statusJSON, &status); err != nil || len(status) != 1 || status[0].PID <= 0 {
		t.Fatalf("invalid JSON status: %v %s", err, statusJSON)
	}
	c.mounts[root] = status[0].PID
	t.Run("mount readable", func(t *testing.T) {
		readableOutput(t, out, []string{"mounted", "syncing", "synchronizing"}, []string{"visible-mount"}, []string{root})
	})
	t.Run("status JSON schema", func(t *testing.T) {
		if status[0].Workspace != "visible-mount" || status[0].Directory != canonicalRoot || status[0].State != "running" || len(status[0].Sync) == 0 {
			t.Fatalf("JSON status schema changed: %s", statusJSON)
		}
	})
	for _, args := range [][]string{{"status"}, {"status", root}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			readableOutput(t, c.run(nil, args...), []string{"visible-mount"}, []string{root}, []string{"running"}, []string{"connected", "connection"}, []string{"pending", "queued", "queue"}, []string{"conflict"}, []string{"error"})
		})
	}
	write(t, filepath.Join(root, "kept"), []byte("local survives unmount"))
	out = c.run(nil, "unmount", root)
	delete(c.mounts, root)
	t.Run("unmount readable", func(t *testing.T) {
		readableOutput(t, out, []string{"unmounted"}, []string{root}, []string{"flushed", "synchronized"})
	})
	if got, err := os.ReadFile(filepath.Join(root, "kept")); err != nil || string(got) != "local survives unmount" {
		t.Fatalf("unmount changed file: %q %v", got, err)
	}
	t.Run("empty status readable", func(t *testing.T) {
		readableOutput(t, c.run(nil, "status"), []string{"no mounts", "no mounted", "no active", "empty"})
	})
	t.Run("empty status JSON preserved", func(t *testing.T) { jsonEqual(t, c.run(nil, "--json", "status"), []any{}) })
	// --force must state its unsynchronized-data consequence in default output.
	c.mount("visible-mount", root)
	out = c.run(nil, "unmount", root, "--force")
	delete(c.mounts, root)
	t.Run("forced detach readable", func(t *testing.T) {
		readableOutput(t, out, []string{"detached", "unmounted"}, []string{root}, []string{"not synchronized", "without flushing", "not flushed", "unsynchronized"})
	})
}
