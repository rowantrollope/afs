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

// The command contract uses only the public workspace/checkpoint commands.
// File contents are written through ordinary mounted directories and observed
// through the retained read-only client test helper.
func TestReducedCLIContract(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	runJSON := func(args ...string) []byte {
		out, diagnostic, err := c.runTimeout(45*time.Second, nil, append([]string{"--json"}, args...)...)
		if err != nil || len(diagnostic) != 0 {
			t.Fatalf("afs %v: %v\nstdout=%s\nstderr=%s", args, err, out, diagnostic)
		}
		jsonValue(t, out)
		return out
	}
	created := jsonValue(t, runJSON("create", "contract")).(map[string]any)
	workspaceID, _ := created["id"].(string)
	head, _ := created["head_savepoint"].(string)
	if created["name"] != "contract" || workspaceID == "" || head == "" {
		t.Fatalf("workspace identity/details missing: %v", created)
	}
	for key := range created {
		if strings.Contains(strings.ToLower(key), "volume") {
			t.Fatalf("workspace exposes removed volume model: %v", created)
		}
	}
	jsonEqual(t, runJSON("info", "contract"), created)
	jsonEqual(t, runJSON("list"), []any{created})

	binaryBytes := []byte{0, 255, 128, '\n', '\r', 0, 'x'}
	importRoot := t.TempDir()
	write(t, filepath.Join(importRoot, "nested", "imported"), []byte("imported\n"))
	write(t, filepath.Join(importRoot, "empty"), nil)
	runJSON("create", "imported", "--from", importRoot)
	for rel, want := range map[string][]byte{"nested/imported": []byte("imported\n"), "empty": nil} {
		got, err := c.remote("imported", rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("import changed %s: %q %v", rel, got, err)
		}
	}

	root := filepath.Join(t.TempDir(), "writer")
	c.mount("contract", root)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".afs-lite-sync" {
			t.Fatalf("new workspace was not empty: %s", entry.Name())
		}
	}
	write(t, filepath.Join(root, "notes", "today.txt"), []byte("exact bytes\n"))
	write(t, filepath.Join(root, "image.bin"), binaryBytes)
	write(t, filepath.Join(root, "empty"), nil)
	if err := os.Mkdir(filepath.Join(root, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "notes", "today.txt"), filepath.Join(root, "documents", "today.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "documents", "today.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("documents/today.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	first := jsonValue(t, runJSON("cp", "create", "contract", "--name", "first")).(map[string]any)
	firstID, _ := first["id"].(string)
	if firstID == "" || first["name"] != "first" {
		t.Fatalf("checkpoint identity missing: %v", first)
	}
	for rel, want := range map[string][]byte{"image.bin": binaryBytes, "empty": nil, "documents/today.txt": []byte("exact bytes\n")} {
		got, err := c.remote("contract", rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("mounted write changed %s: %q %v", rel, got, err)
		}
	}
	show := jsonValue(t, runJSON("cp", "show", "contract", "first")).(map[string]any)
	if show["checkpoint"].(map[string]any)["id"] != firstID {
		t.Fatalf("wrong checkpoint shown: %v", show)
	}
	manifest := show["manifest"].(map[string]any)["entries"].(map[string]any)
	if manifest["/image.bin"].(map[string]any)["size"] != float64(len(binaryBytes)) || manifest["/documents/today.txt"].(map[string]any)["mode"] != float64(0o640) {
		t.Fatalf("checkpoint lost bytes or permissions: %v", manifest)
	}
	if link := manifest["/link"].(map[string]any); link["type"] != "symlink" || link["target"] != "documents/today.txt" {
		t.Fatalf("checkpoint lost symlink: %v", link)
	}
	jsonEqual(t, runJSON("cp", "show", "contract", firstID), show)

	write(t, filepath.Join(root, "image.bin"), []byte("second checkpoint"))
	second := jsonValue(t, runJSON("cp", "create", "contract", "--name", "second")).(map[string]any)
	secondID, _ := second["id"].(string)
	if secondID == "" || secondID == firstID {
		t.Fatalf("checkpoint identities not distinct: %v", second)
	}
	found := map[string]bool{}
	for _, item := range jsonValue(t, runJSON("cp", "list", "contract")).([]any) {
		found[item.(map[string]any)["id"].(string)] = true
	}
	if !found[firstID] || !found[secondID] {
		t.Fatalf("checkpoint list incomplete: %v", found)
	}
	write(t, filepath.Join(root, "image.bin"), []byte("uncheckpointed"))
	if err := os.Remove(filepath.Join(root, "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "documents")); err != nil {
		t.Fatal(err)
	}
	c.unmount(root)
	jsonEqual(t, runJSON("fork", "contract", "latest-fork"), map[string]any{"workspace": "latest-fork", "forked_from": "contract"})
	jsonEqual(t, runJSON("fork", "contract", "first-fork", "--checkpoint", "first"), map[string]any{"workspace": "first-fork", "forked_from": "contract"})
	for workspace, want := range map[string][]byte{"latest-fork": []byte("second checkpoint"), "first-fork": binaryBytes} {
		got, err := c.remote(workspace, "image.bin")
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("fork %s changed checkpoint bytes: %q %v", workspace, got, err)
		}
	}
	restored := jsonValue(t, runJSON("cp", "restore", "contract", "first", "--yes")).(map[string]any)
	if restored["restored"] != true || restored["checkpoint_id"] != firstID || restored["safety_checkpoint_created"] != true {
		t.Fatalf("restore missing safety checkpoint: %v", restored)
	}
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	c.mount("contract", restoredRoot)
	awaitFile(t, filepath.Join(restoredRoot, "image.bin"), binaryBytes)
	awaitFile(t, filepath.Join(restoredRoot, "documents", "today.txt"), []byte("exact bytes\n"))
	awaitFile(t, filepath.Join(restoredRoot, "empty"), nil)
	info, err := os.Stat(filepath.Join(restoredRoot, "documents", "today.txt"))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("restored permissions differ: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(restoredRoot, "link")); err != nil || target != "documents/today.txt" {
		t.Fatalf("restored symlink differs: %q %v", target, err)
	}
	c.unmount(restoredRoot)
	generated := jsonValue(t, runJSON("cp", "create", "contract")).(map[string]any)
	generatedID, _ := generated["id"].(string)
	generatedName, _ := generated["name"].(string)
	if generatedID == "" || generatedName == "" {
		t.Fatalf("unnamed checkpoint must receive usable identity: %v", generated)
	}
	jsonEqual(t, runJSON("cp", "delete", "contract", "second", "--yes"), map[string]any{"deleted": "second", "workspace": "contract"})
	jsonEqual(t, runJSON("delete", "contract", "--yes"), map[string]any{"deleted": "contract"})
	got, err := c.remote("latest-fork", "image.bin")
	if err != nil || string(got) != "second checkpoint" {
		t.Fatalf("fork lost independent data: %q %v", got, err)
	}
	if got := runJSON("cp", "show", "first-fork", "latest"); !bytes.Contains(got, []byte("manifest")) {
		t.Fatalf("fork checkpoint missing: %s", got)
	}
	c.mustFail("info", "contract")
	c.mustFail("cp", "show", "contract", secondID)
}

func TestReducedCLIHelpAndFailures(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	commands := map[string][]string{
		"create": {}, "list": {}, "info": {}, "fork": {}, "delete": {},
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
	for _, removed := range [][]string{{"fs"}, {"fs", "--help"}, {"fs", "cat", "validation", "file"}, {"fs", "put", "validation", "file", "--help"}, {"ws"}, {"ws", "--help"}, {"ws", "create", "validation"}, {"ws", "list", "--help"}} {
		t.Run("removed-"+strings.Join(removed, "-"), func(t *testing.T) {
			args := append([]string{"--config", "/does-not-exist", "--redis", "redis://127.0.0.1:1/0"}, removed...)
			out, diagnostic, err := c.runTimeout(3*time.Second, nil, args...)
			if err == nil || len(out) != 0 || !bytes.Contains(diagnostic, []byte("unknown command")) {
				t.Fatalf("removed family must fail before configuration/Redis: %v stdout=%q stderr=%q", err, out, diagnostic)
			}
		})
	}
	for _, argument := range []string{"--help", "-h", "--version"} {
		t.Run(argument, func(t *testing.T) {
			out, diag, err := c.runTimeout(3*time.Second, nil, "--config", "/does-not-exist", "--redis", "redis://127.0.0.1:1/0", argument)
			if err != nil || len(diag) != 0 || !bytes.HasPrefix(out, []byte("afs ")) {
				t.Fatalf("offline help/version: %v %s %s", err, out, diag)
			}
			if argument != "--version" {
				for _, line := range strings.Split(string(out), "\n") {
					fields := strings.Fields(line)
					if len(fields) == 0 {
						continue
					}
					for _, excluded := range []string{"ws", "fs", "vol", "nfs", "fuse", "search", "query", "cloud"} {
						if fields[0] == excluded {
							t.Fatalf("removed command in help: %q", excluded)
						}
					}
				}
			}
		})
	}
	c.run(nil, "create", "validation")
	root := filepath.Join(t.TempDir(), "validation")
	c.mount("validation", root)
	write(t, filepath.Join(root, "file"), []byte("retained"))
	c.run(nil, "cp", "create", "validation", "--name", "saved")
	write(t, filepath.Join(root, "file"), []byte("live"))
	c.unmount(root)
	invalid := [][]string{
		{"unknown"}, {"vol", "list"}, {"--json=true", "list"}, {"--config"}, {"--redis"},
		{"create"}, {"create", "a", "b"}, {"create", "validation"}, {"create", "bad/name"},
		{"list", "extra"}, {"info"}, {"fork", "validation"}, {"delete"},
		{"list", "--from", "."}, {"info", "validation", "--yes"}, {"create", "nope", "--unknown"},
		{"cp", "create"}, {"cp", "list", "validation", "extra"}, {"cp", "show", "validation"},
		{"cp", "restore", "validation"}, {"cp", "delete", "validation"}, {"cp", "show", "validation", "missing"},
		{"cp", "list", "validation", "--yes"}, {"cp", "show", "validation", "saved", "--name", "ignored"},
		{"mount"}, {"mount", "validation"}, {"mount", "validation", "directory", "extra"},
		{"mount", "validation", "directory", "--fuse"}, {"unmount"}, {"unmount", "one", "two"},
		{"status", "one", "two"}, {"status", "--unknown"},
		{"delete", "validation"}, {"cp", "restore", "validation", "saved"}, {"cp", "delete", "validation", "saved"},
	}
	for _, args := range invalid {
		t.Run("reject-"+strings.Join(args, "-"), func(t *testing.T) {
			out, diagnostic, err := c.runTimeout(5*time.Second, nil, args...)
			// Commands that reach Redis identify it before reporting operation errors.
			body := strings.TrimPrefix(string(out), "REDIS: "+r.url()+"\n\n")
			if err == nil || len(body) != 0 || len(diagnostic) == 0 {
				t.Fatalf("invalid command must fail on stderr only: %v\nstdout=%q\nstderr=%q", err, out, diagnostic)
			}
		})
	}
	if got, err := c.remote("validation", "file"); err != nil || string(got) != "live" {
		t.Fatalf("rejected destructive commands changed workspace: %q %v", got, err)
	}
	c.run(nil, "cp", "show", "validation", "saved")
}

func TestConfigurationFileSelection(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	// Omit the harness's --redis argument to verify the real file-only path.
	for _, args := range [][]string{{"--json", "create", "configured"}, {"--json", "info", "configured"}} {
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
	c.run(nil, "info", "configured") // explicit --redis overrides the file.
	for name, contents := range map[string]string{"invalid-json": `{`, "invalid-url": `{"redis":"not-a-redis-url"}`} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--config", file, "list")
			cmd.Env = append(os.Environ(), "AFS_STATE_DIR="+c.state)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			if err := cmd.Run(); err == nil || out.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("invalid config must fail on stderr: %v %s %s", err, out.Bytes(), diagnostic.Bytes())
			}
		})
	}
}

func TestMountKeepsPreexistingIgnoredFiles(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	source := t.TempDir()
	write(t, filepath.Join(source, "included"), []byte("published"))
	c.run(nil, "create", "ignored-local", "--from", source)
	root := filepath.Join(t.TempDir(), "root")
	write(t, filepath.Join(root, ".afsignore"), []byte("private/\n"))
	write(t, filepath.Join(root, "private", "local.txt"), []byte("keep local only"))
	c.mount("ignored-local", root)
	awaitFile(t, filepath.Join(root, "included"), []byte("published"))
	for relative, want := range map[string]string{".afsignore": "private/\n", "private/local.txt": "keep local only"} {
		got, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil || string(got) != want {
			t.Fatalf("mount destroyed existing ignored file %s: %q %v", relative, got, err)
		}
	}
	c.awaitMissingPublished("ignored-local", "private/local.txt")
	c.unmount(root)
}
