//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func jsonValue(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON response: %v\n%s", err, raw)
	}
	return value
}

func jsonEqual(t *testing.T, raw []byte, want any) {
	t.Helper()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonValue(t, raw); !reflect.DeepEqual(got, jsonValue(t, encoded)) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", raw, encoded)
	}
}

// This command table follows the reduced public CLI and README examples. It
// exercises the compiled program without importing its command implementations.
func TestReducedCLIContract(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	binaryBytes := []byte{0, 255, 128, '\n', '\r', 0, 'x'}
	inputFile := filepath.Join(t.TempDir(), "binary.dat")
	write(t, inputFile, binaryBytes)
	importRoot := t.TempDir()
	write(t, filepath.Join(importRoot, "nested", "imported"), []byte("imported\n"))
	write(t, filepath.Join(importRoot, "empty"), nil)
	var created map[string]any
	var firstID, secondID string
	steps := []struct {
		name  string
		args  []string
		input []byte
		check func(*testing.T, []byte)
	}{
		{"ws create empty", []string{"ws", "create", "contract"}, nil, func(t *testing.T, raw []byte) {
			created = jsonValue(t, raw).(map[string]any)
			id, _ := created["id"].(string)
			head, _ := created["head_savepoint"].(string)
			if created["name"] != "contract" || id == "" || head == "" {
				t.Fatalf("workspace identity/details missing: %s", raw)
			}
			for key := range created {
				if strings.Contains(strings.ToLower(key), "volume") {
					t.Fatalf("workspace exposes removed volume model: %s", raw)
				}
			}
		}},
		{"ws info exact", []string{"ws", "info", "contract"}, nil, func(t *testing.T, raw []byte) { jsonEqual(t, raw, created) }},
		{"ws list exact", []string{"ws", "list"}, nil, func(t *testing.T, raw []byte) { jsonEqual(t, raw, []any{created}) }},
		{"empty workspace tree", []string{"fs", "ls", "contract"}, nil, func(t *testing.T, raw []byte) { jsonEqual(t, raw, []any{}) }},
		{"ws create from directory", []string{"ws", "create", "imported", "--from", importRoot}, nil, nil},
		{"import nested bytes", []string{"fs", "cat", "imported", "nested/imported"}, nil, func(t *testing.T, raw []byte) {
			if string(raw) != "imported\n" {
				t.Fatalf("import changed bytes: %q", raw)
			}
		}},
		{"import empty bytes", []string{"fs", "cat", "imported", "empty"}, nil, func(t *testing.T, raw []byte) {
			if len(raw) != 0 {
				t.Fatalf("empty import changed bytes: %q", raw)
			}
		}},
		{"fs mkdir", []string{"fs", "mkdir", "contract", "documents"}, nil, func(t *testing.T, raw []byte) {
			jsonEqual(t, raw, map[string]any{"operation": "mkdir", "path": "documents"})
		}},
		{"fs put stdin", []string{"fs", "put", "contract", "notes/today.txt"}, []byte("exact bytes\n"), func(t *testing.T, raw []byte) {
			jsonEqual(t, raw, map[string]any{"path": "notes/today.txt", "bytes": 12})
		}},
		{"fs put from file", []string{"fs", "put", "contract", "image.bin", "--from", inputFile}, []byte("stdin must be ignored"), func(t *testing.T, raw []byte) {
			jsonEqual(t, raw, map[string]any{"path": "image.bin", "bytes": len(binaryBytes)})
		}},
		{"fs cat binary exact stdout", []string{"fs", "cat", "contract", "image.bin"}, nil, func(t *testing.T, raw []byte) {
			if !bytes.Equal(raw, binaryBytes) {
				t.Fatalf("binary stdout changed: %x", raw)
			}
		}},
		{"fs put empty stdin", []string{"fs", "put", "contract", "empty"}, nil, nil},
		{"fs cat empty exact stdout", []string{"fs", "cat", "contract", "empty"}, nil, func(t *testing.T, raw []byte) {
			if len(raw) != 0 {
				t.Fatalf("empty stdout contains bytes: %q", raw)
			}
		}},
		{"fs ls exact entries and metadata", []string{"fs", "ls", "contract", "notes"}, nil, func(t *testing.T, raw []byte) {
			entries := jsonValue(t, raw).([]any)
			if len(entries) != 1 {
				t.Fatalf("unexpected listing: %s", raw)
			}
			entry := entries[0].(map[string]any)
			for _, key := range []string{"Inode", "Mtime"} {
				if value, ok := entry[key].(float64); !ok || value <= 0 {
					t.Fatalf("invalid %s: %s", key, raw)
				}
				delete(entry, key)
			}
			want := map[string]any{"Name": "today.txt", "Type": "file", "Mode": float64(0o644), "UID": float64(0), "GID": float64(0), "Size": float64(12)}
			if !reflect.DeepEqual(entry, want) {
				t.Fatalf("listing metadata mismatch: got=%v want=%v", entry, want)
			}
		}},
		{"fs mv", []string{"fs", "mv", "contract", "notes/today.txt", "documents/today.txt"}, nil, nil},
		{"fs cat moved bytes", []string{"fs", "cat", "contract", "documents/today.txt"}, nil, func(t *testing.T, raw []byte) {
			if string(raw) != "exact bytes\n" {
				t.Fatalf("move changed bytes: %q", raw)
			}
		}},
		{"cp create named first", []string{"cp", "create", "contract", "--name", "first"}, nil, func(t *testing.T, raw []byte) {
			meta := jsonValue(t, raw).(map[string]any)
			firstID, _ = meta["id"].(string)
			if firstID == "" || meta["name"] != "first" {
				t.Fatalf("checkpoint identity missing: %s", raw)
			}
		}},
		{"update before second checkpoint", []string{"fs", "put", "contract", "image.bin"}, []byte("second checkpoint"), nil},
		{"cp create named second", []string{"cp", "create", "contract", "--name", "second"}, nil, func(t *testing.T, raw []byte) {
			meta := jsonValue(t, raw).(map[string]any)
			secondID, _ = meta["id"].(string)
			if secondID == "" || secondID == firstID {
				t.Fatalf("checkpoint identities not distinct: %s", raw)
			}
		}},
		{"cp list includes both", []string{"cp", "list", "contract"}, nil, func(t *testing.T, raw []byte) {
			found := map[string]bool{}
			for _, item := range jsonValue(t, raw).([]any) {
				found[item.(map[string]any)["id"].(string)] = true
			}
			if !found[firstID] || !found[secondID] {
				t.Fatalf("checkpoint list incomplete: %s", raw)
			}
		}},
		{"cp show by name", []string{"cp", "show", "contract", "first"}, nil, func(t *testing.T, raw []byte) {
			show := jsonValue(t, raw).(map[string]any)
			if show["checkpoint"].(map[string]any)["id"] != firstID {
				t.Fatalf("wrong checkpoint shown: %s", raw)
			}
			entries := show["manifest"].(map[string]any)["entries"].(map[string]any)
			entry, exists := entries["/image.bin"].(map[string]any)
			if !exists || entry["size"] != float64(len(binaryBytes)) {
				t.Fatalf("checkpoint manifest lost binary size: %s", raw)
			}
			byID, diagnostic, err := c.runTimeout(10*time.Second, nil, "--json", "cp", "show", "contract", firstID)
			if err != nil || len(diagnostic) != 0 {
				t.Fatalf("checkpoint lookup by ID: %v %s", err, diagnostic)
			}
			jsonEqual(t, byID, show)
		}},
		{"uncheckpointed live update", []string{"fs", "put", "contract", "image.bin"}, []byte("uncheckpointed"), nil},
		{"ws fork default latest checkpoint", []string{"ws", "fork", "contract", "latest-fork"}, nil, nil},
		{"default fork contains latest checkpoint bytes", []string{"fs", "cat", "latest-fork", "image.bin"}, nil, func(t *testing.T, raw []byte) {
			if string(raw) != "second checkpoint" {
				t.Fatalf("default fork must use latest checkpoint: %q", raw)
			}
		}},
		{"ws fork explicit checkpoint", []string{"ws", "fork", "contract", "first-fork", "--checkpoint", "first"}, nil, nil},
		{"explicit fork immutable bytes", []string{"fs", "cat", "first-fork", "image.bin"}, nil, func(t *testing.T, raw []byte) {
			if !bytes.Equal(raw, binaryBytes) {
				t.Fatalf("explicit fork changed bytes: %x", raw)
			}
		}},
		{"fs rm file", []string{"fs", "rm", "contract", "empty"}, nil, nil},
		{"fs rm recursive", []string{"fs", "rm", "contract", "documents", "--recursive"}, nil, nil},
		{"cp restore confirmed", []string{"cp", "restore", "contract", "first", "--yes"}, nil, func(t *testing.T, raw []byte) {
			result := jsonValue(t, raw).(map[string]any)
			if result["restored"] != true || result["checkpoint_id"] != firstID || result["safety_checkpoint_created"] != true {
				t.Fatalf("restore missing safety checkpoint: %s", raw)
			}
		}},
		{"restore exact binary", []string{"fs", "cat", "contract", "image.bin"}, nil, func(t *testing.T, raw []byte) {
			if !bytes.Equal(raw, binaryBytes) {
				t.Fatalf("restore changed bytes: %x", raw)
			}
		}},
		{"restore deleted nested file", []string{"fs", "cat", "contract", "documents/today.txt"}, nil, func(t *testing.T, raw []byte) {
			if string(raw) != "exact bytes\n" {
				t.Fatalf("restore lost nested file: %q", raw)
			}
		}},
		{"cp create generated name", []string{"cp", "create", "contract"}, nil, func(t *testing.T, raw []byte) {
			meta := jsonValue(t, raw).(map[string]any)
			id, _ := meta["id"].(string)
			name, _ := meta["name"].(string)
			if id == "" || name == "" {
				t.Fatalf("unnamed checkpoint must receive usable identity: %s", raw)
			}
		}},
		{"cp delete confirmed", []string{"cp", "delete", "contract", "second", "--yes"}, nil, nil},
		{"ws delete confirmed", []string{"ws", "delete", "contract", "--yes"}, nil, nil},
		{"fork survives source workspace deletion", []string{"fs", "cat", "latest-fork", "image.bin"}, nil, func(t *testing.T, raw []byte) {
			if string(raw) != "second checkpoint" {
				t.Fatalf("fork lost independent data: %q", raw)
			}
		}},
	}
	for _, step := range steps {
		if !t.Run(step.name, func(t *testing.T) {
			args := step.args
			if !(len(args) > 1 && args[0] == "fs" && args[1] == "cat") {
				args = append([]string{"--json"}, args...)
			}
			out, diagnostic, err := c.runTimeout(45*time.Second, step.input, args...)
			if err != nil {
				t.Fatalf("afs %v: %v\nstdout=%s\nstderr=%s", args, err, out, diagnostic)
			}
			if len(diagnostic) != 0 {
				t.Fatalf("successful command emitted diagnostics: %s", diagnostic)
			}
			if !(len(step.args) > 1 && step.args[0] == "fs" && step.args[1] == "cat") {
				jsonValue(t, out)
			}
			if step.check != nil {
				step.check(t, out)
			}
		}) {
			return
		}
	}
	// IDs as well as names are valid references; source removal cannot invalidate forks.
	if got := c.run(nil, "cp", "show", "first-fork", "latest"); !bytes.Contains(got, []byte("manifest")) {
		t.Fatalf("fork checkpoint missing: %s", got)
	}
	c.mustFail("ws", "info", "contract")
	c.mustFail("cp", "show", "contract", secondID)
}

func TestReducedCLIHelpAndFailures(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	commands := map[string][]string{
		"ws":    {"create", "list", "info", "fork", "delete"},
		"fs":    {"ls", "cat", "put", "mkdir", "mv", "rm"},
		"cp":    {"create", "list", "show", "restore", "delete"},
		"mount": {}, "unmount": {}, "status": {},
	}
	var groups []string
	for group := range commands {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		for _, operation := range append([]string{""}, commands[group]...) {
			args := []string{"--config", filepath.Join(t.TempDir(), "missing-config"), "--redis", "redis://127.0.0.1:1/0", group}
			if operation != "" {
				args = append(args, operation)
			}
			args = append(args, "--help")
			t.Run(strings.Join([]string{group, operation, "help"}, "-"), func(t *testing.T) {
				out, diag, err := c.runTimeout(3*time.Second, nil, args...)
				if err != nil || len(diag) != 0 || !bytes.Contains(out, []byte("afs "+group)) {
					t.Fatalf("offline subcommand help: %v %s %s", err, out, diag)
				}
			})
		}
	}
	for _, argument := range []string{"--help", "-h", "--version"} {
		t.Run(argument, func(t *testing.T) {
			out, diag, err := c.runTimeout(3*time.Second, nil, "--config", "/does-not-exist", "--redis", "redis://127.0.0.1:1/0", argument)
			if err != nil || len(diag) != 0 || !bytes.HasPrefix(out, []byte("afs ")) {
				t.Fatalf("offline help/version: %v %s %s", err, out, diag)
			}
			if argument != "--version" {
				for _, excluded := range []string{"vol ", "nfs ", "fuse ", "search ", "query ", "cloud "} {
					if bytes.Contains(out, []byte(excluded)) {
						t.Fatalf("removed command in help: %q", excluded)
					}
				}
			}
		})
	}
	c.run(nil, "ws", "create", "validation")
	c.run([]byte("retained"), "fs", "put", "validation", "file")
	c.run(nil, "cp", "create", "validation", "--name", "saved")
	c.run([]byte("live"), "fs", "put", "validation", "file")
	invalid := [][]string{
		{"unknown"}, {"vol", "list"}, {"--json=true", "ws", "list"}, {"--config"}, {"--redis"},
		{"ws", "create"}, {"ws", "create", "a", "b"}, {"ws", "create", "validation"}, {"ws", "create", "bad/name"},
		{"ws", "list", "extra"}, {"ws", "info"}, {"ws", "fork", "validation"}, {"ws", "delete"},
		{"ws", "list", "--from", "."}, {"ws", "info", "validation", "--yes"}, {"ws", "create", "nope", "--unknown"},
		{"fs", "ls"}, {"fs", "cat", "validation"}, {"fs", "put", "validation"}, {"fs", "mkdir", "validation"},
		{"fs", "mv", "validation", "file"}, {"fs", "rm", "validation"}, {"fs", "cat", "validation", "missing"},
		{"--json", "fs", "cat", "validation", "file"}, {"fs", "put", "validation", "../outside"},
		{"fs", "put", "validation", "/absolute"}, {"fs", "put", "validation", "back\\slash"},
		{"fs", "ls", "validation", "--recursive"}, {"fs", "cat", "validation", "file", "--from", "ignored"},
		{"fs", "put", "validation", "file", "--from"}, {"fs", "rm", "validation", ".", "--recursive"},
		{"cp", "create"}, {"cp", "list", "validation", "extra"}, {"cp", "show", "validation"},
		{"cp", "restore", "validation"}, {"cp", "delete", "validation"}, {"cp", "show", "validation", "missing"},
		{"cp", "list", "validation", "--yes"}, {"cp", "show", "validation", "saved", "--name", "ignored"},
		{"mount"}, {"mount", "validation"}, {"mount", "validation", "directory", "extra"},
		{"mount", "validation", "directory", "--fuse"}, {"unmount"}, {"unmount", "one", "two"},
		{"status", "one", "two"}, {"status", "--unknown"},
		{"ws", "delete", "validation"}, {"cp", "restore", "validation", "saved"}, {"cp", "delete", "validation", "saved"},
	}
	for _, args := range invalid {
		t.Run("reject-"+strings.Join(args, "-"), func(t *testing.T) {
			out, diagnostic, err := c.runTimeout(5*time.Second, nil, args...)
			if err == nil || len(out) != 0 || len(diagnostic) == 0 {
				t.Fatalf("invalid command must fail on stderr only: %v\nstdout=%q\nstderr=%q", err, out, diagnostic)
			}
		})
	}
	if got := c.run(nil, "fs", "cat", "validation", "file"); string(got) != "live" {
		t.Fatalf("rejected destructive commands changed workspace: %q", got)
	}
	c.run(nil, "cp", "show", "validation", "saved")
}

func TestConfigurationFileSelection(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	// Omit the harness's --redis argument to verify the real file-only path.
	for _, args := range [][]string{{"ws", "create", "configured"}, {"--json", "ws", "info", "configured"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, binary, append([]string{"--config", c.config}, args...)...)
		cmd.Env = append(os.Environ(), "AFS_STATE_DIR="+c.state)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("file-only configuration: %v %s", err, out)
		}
		if jsonValue(t, out).(map[string]any)["name"] != "configured" {
			t.Fatalf("wrong configured workspace: %s", out)
		}
	}
	if err := os.WriteFile(c.config, []byte(`{"redis":"redis://127.0.0.1:1/0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c.run(nil, "ws", "info", "configured") // explicit --redis overrides the file.
	for name, contents := range map[string]string{"invalid-json": `{`, "invalid-url": `{"redis":"not-a-redis-url"}`} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--config", file, "ws", "list")
			cmd.Env = append(os.Environ(), "AFS_STATE_DIR="+c.state)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			if err := cmd.Run(); err == nil || out.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("invalid config must fail on stderr: %v %s %s", err, out.Bytes(), diagnostic.Bytes())
			}
		})
	}
}
