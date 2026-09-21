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
	wantBinary := []byte{0, 255, '\n', 128, 'x'}
	importRoot := t.TempDir()
	write(t, filepath.Join(importRoot, "notes", "original"), []byte("one\n"))
	write(t, filepath.Join(importRoot, "binary"), wantBinary)
	write(t, filepath.Join(importRoot, "empty"), nil)
	writer := filepath.Join(t.TempDir(), "writer")
	type step struct {
		name   string
		args   []string
		before func()
		facts  [][]string
	}
	steps := []step{
		{"empty workspace list", []string{"list"}, nil, [][]string{{"no workspaces", "empty"}}},
		{"empty mount status", []string{"status"}, nil, [][]string{{"no mounts", "no mounted", "no active", "empty"}}},
		{"create", []string{"create", "blank"}, nil, [][]string{{"created"}, {"blank"}}},
		{"create from directory", []string{"create", "friendly", "--from", importRoot}, nil, [][]string{{"created", "imported"}, {"friendly"}}},
		{"list", []string{"list"}, nil, [][]string{{"friendly"}, {"blank"}}},
		{"info", []string{"info", "friendly"}, nil, [][]string{{"friendly"}, {"id"}, {"head", "checkpoint"}}},
		{"checkpoint create", []string{"checkpoint", "create", "friendly", "--name", "before-edit"}, nil, [][]string{{"checkpoint"}, {"created"}, {"before-edit"}}},
		{"checkpoint list", []string{"checkpoint", "list", "friendly"}, nil, [][]string{{"before-edit"}}},
		{"checkpoint show", []string{"checkpoint", "show", "friendly", "before-edit"}, nil, [][]string{{"before-edit"}, {"files"}, {"bytes"}, {"notes/original"}}},
		{"fork", []string{"fork", "friendly", "friendly-copy", "--checkpoint", "before-edit"}, nil, [][]string{{"forked"}, {"friendly"}, {"friendly-copy"}}},
		{"checkpoint create next", []string{"checkpoint", "create", "friendly", "--name", "after-edit"}, func() {
			c.mount("friendly", writer)
			if err := os.Remove(filepath.Join(writer, "notes", "original")); err != nil {
				t.Fatal(err)
			}
			c.unmount(writer)
		}, [][]string{{"checkpoint"}, {"after-edit"}}},
		{"checkpoint restore", []string{"checkpoint", "restore", "friendly", "before-edit", "--yes"}, func() {
			c.mount("friendly", writer)
			write(t, filepath.Join(writer, "pending"), []byte("preserve in safety checkpoint"))
			c.unmount(writer)
		}, [][]string{{"restored"}, {"friendly"}, {"safety"}}},
		{"checkpoint delete", []string{"checkpoint", "delete", "friendly", "after-edit", "--yes"}, nil, [][]string{{"deleted"}, {"checkpoint"}, {"after-edit"}}},
		{"delete", []string{"delete", "friendly-copy", "--yes"}, nil, [][]string{{"deleted"}, {"workspace"}, {"friendly-copy"}}},
	}
	for _, s := range steps {
		if s.before != nil {
			s.before()
		}
		commandSucceeded := false
		t.Run(s.name, func(t *testing.T) {
			out, diag, err := c.runTimeout(30*time.Second, nil, s.args...)
			if err != nil {
				t.Fatalf("afs %v: %v\nstdout=%s\nstderr=%s", s.args, err, out, diag)
			}
			commandSucceeded = true
			label := "REDIS"
			if s.args[0] == "status" {
				label = "Configured REDIS"
			}
			if !strings.HasPrefix(string(out), label+": "+r.url()+"\n\n") {
				t.Errorf("missing database header: %s", out)
			}
			if len(diag) != 0 {
				t.Errorf("successful command wrote diagnostics: %s", diag)
			}
			readableOutput(t, out, s.facts...)
		})
		if !commandSucceeded {
			return
		}
	}
	for rel, want := range map[string][]byte{"binary": wantBinary, "empty": nil, "notes/original": []byte("one\n")} {
		got, err := c.remote("friendly", rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("checkpoint command changed file %s: %x %v", rel, got, err)
		}
	}
}

func TestDefaultMountOutputAndJSONStatus(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "--json", "create", "visible-mount")
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

func TestDatabaseHeaderFollowsEffectiveOverride(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	override := strings.TrimSuffix(r.url(), "/0") + "/1?db=3"
	endpoint := strings.TrimSuffix(r.url(), "/0") + "/3"
	c.run(nil, "--redis", override, "create", "only-in-three")
	out := c.run(nil, "--redis", override, "list")
	if !strings.HasPrefix(string(out), "REDIS: "+endpoint+"\n\n") || !strings.Contains(string(out), "only-in-three") {
		t.Fatalf("override result/header disagree: %s", out)
	}
	out = c.run(nil, "list")
	if !strings.Contains(string(out), "No workspaces.") || !strings.HasPrefix(string(out), "REDIS: "+r.url()+"\n\n") {
		t.Fatalf("default database context: %s", out)
	}
	out = c.run(nil, "--redis", override, "--json", "list")
	if !json.Valid(out) || !strings.Contains(string(out), "only-in-three") {
		t.Fatalf("JSON output: %s", out)
	}
}
