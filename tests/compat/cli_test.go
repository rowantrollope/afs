//go:build compatibility

// Package compat compares the extracted CLI with a separately built original CLI.
// It intentionally uses only subprocesses and ordinary filesystem operations.
package compat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

var baselineBinary, currentBinary string

func TestMain(m *testing.M) {
	baselineBinary = os.Getenv("AFS_BASELINE_BINARY")
	if baselineBinary == "" {
		fmt.Fprintln(os.Stderr, "compatibility tests require AFS_BASELINE_BINARY pointing to the original afs CLI; no comparisons ran")
		os.Exit(1)
	}
	var err error
	baselineBinary, err = filepath.Abs(baselineBinary)
	if err != nil {
		panic(err)
	}
	if info, err := os.Stat(baselineBinary); err != nil || info.IsDir() {
		fmt.Fprintf(os.Stderr, "invalid AFS_BASELINE_BINARY %q: %v\n", baselineBinary, err)
		os.Exit(1)
	}
	currentBinary = os.Getenv("AFS_E2E_BINARY")
	var buildDir string
	if currentBinary == "" {
		buildDir, err = os.MkdirTemp("", "afs-compat-build-")
		if err != nil {
			panic(err)
		}
		currentBinary = filepath.Join(buildDir, "afs")
		_, source, _, _ := runtime.Caller(0)
		build := exec.Command("go", "build", "-o", currentBinary, "./cmd/afs")
		build.Dir = filepath.Dir(filepath.Dir(filepath.Dir(source)))
		if output, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build derivative CLI: %v\n%s", err, output)
			_ = os.RemoveAll(buildDir)
			os.Exit(1)
		}
	} else {
		currentBinary, err = filepath.Abs(currentBinary)
		if err != nil {
			panic(err)
		}
	}
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

type cli struct {
	t                          *testing.T
	prior                      bool
	binary, root, config, addr string
	env                        []string
	mounts                     map[string]bool
}

func newCLI(t *testing.T, prior bool) *cli {
	t.Helper()
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Fatal("compatibility tests require redis-server; comparison was not run")
	}
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	_, port, _ := net.SplitHostPort(addr)
	log, err := os.Create(filepath.Join(root, "redis.log"))
	if err != nil {
		t.Fatal(err)
	}
	server := exec.Command("redis-server", "--bind", "127.0.0.1", "--port", port, "--save", "", "--appendonly", "no", "--dir", root, "--loglevel", "warning")
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	_ = log.Close()
	rdb := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: 0, DialTimeout: time.Second, ReadTimeout: time.Second})
	t.Cleanup(func() { _ = server.Process.Signal(syscall.SIGTERM); _ = server.Wait(); _ = rdb.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for rdb.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("disposable Redis failed to start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := &cli{t: t, prior: prior, binary: currentBinary, root: root, addr: addr, config: filepath.Join(root, "config.json"), mounts: map[string]bool{}}
	if prior {
		c.binary = baselineBinary
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	// The original only supports HOME/.afs; the derivative supports AFS_STATE_DIR.
	// Remove inherited endpoint/state overrides before giving each child its private home.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "REDIS_URL" && !strings.HasPrefix(key, "AFS_") {
			c.env = append(c.env, entry)
		}
	}
	c.env = append(c.env, "HOME="+home, "AFS_STATE_DIR="+filepath.Join(root, "state"))
	var config any = map[string]any{"redis": "redis://" + addr + "/0"}
	if prior {
		// An omitted runtime block resets the prior backend to the platform default.
		// Explicit none avoids loading an unrelated NFS/FUSE helper in sync mode.
		config = map[string]any{"redis": map[string]any{"addr": addr}, "productMode": "local", "mode": "sync", "runtime": map[string]any{"mount": map[string]any{"backend": "none"}, "logs": map[string]string{"sync": filepath.Join(root, "sync.log"), "mount": filepath.Join(root, "mount.log")}}}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for path := range c.mounts {
			args := []string{"unmount", path, "--force"}
			if prior {
				args = []string{"vol", "unmount", path}
			}
			_, _, err := c.execute(15*time.Second, args...)
			if err != nil {
				t.Errorf("cleanup unmount %s: %v", path, err)
			}
		}
		if t.Failed() {
			_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() == "sync.log" {
					raw, _ := os.ReadFile(path)
					if len(raw) > 5000 {
						raw = raw[len(raw)-5000:]
					}
					t.Logf("sync log %s:\n%s", path, raw)
				}
				return nil
			})
		}
	})
	return c
}

func (c *cli) execute(timeout time.Duration, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.binary, append([]string{"--config", c.config}, args...)...)
	command.Dir = c.root
	command.Env = c.env
	command.Stdin = bytes.NewReader(nil)
	var out, diagnostic bytes.Buffer
	command.Stdout = &out
	command.Stderr = &diagnostic
	err := command.Run()
	return out.Bytes(), diagnostic.Bytes(), err
}
func (c *cli) run(args ...string) []byte {
	c.t.Helper()
	out, diagnostic, err := c.execute(45*time.Second, args...)
	c.t.Logf("%s %s: %v", filepath.Base(c.binary), strings.Join(args, " "), err)
	if err != nil {
		c.t.Fatalf("CLI %v: %v\nstdout=%s\nstderr=%s", args, err, out, diagnostic)
	}
	return out
}
func (c *cli) mapped(prior, current []string) []byte {
	c.t.Helper()
	if c.prior {
		return c.run(prior...)
	}
	return c.run(current...)
}
func (c *cli) mount(workspace, path string) {
	c.t.Helper()
	c.mapped([]string{"vol", "mount", workspace, path}, []string{"mount", workspace, path})
	c.mounts[path] = true
}
func (c *cli) unmount(path string) {
	c.t.Helper()
	c.mapped([]string{"vol", "unmount", path}, []string{"unmount", path})
	delete(c.mounts, path)
}
func (c *cli) checkpoint(name, mount string) {
	c.t.Helper()
	if c.prior && mount != "" {
		c.run("vol", "save", "--json", mount)
	}
	c.mapped([]string{"cp", "create", "--volume", "corpus", name}, []string{"cp", "create", "corpus", "--name", name})
}
func decode(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, raw)
	}
}
func (c *cli) names() []string {
	var rows []struct {
		Name string `json:"name"`
	}
	if c.prior {
		var v struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		decode(c.t, c.run("vol", "list", "--json"), &v)
		rows = v.Items
	} else {
		decode(c.t, c.run("--json", "list"), &rows)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}

type checkpointStats struct {
	Name        string
	Files, Dirs int
	Bytes       int64
}

func (c *cli) checkpointInfo(name string) checkpointStats {
	type summary struct {
		Name    string `json:"name"`
		Files   int    `json:"file_count"`
		Dirs    int    `json:"dir_count"`
		Folders int    `json:"folder_count"`
		Bytes   int64  `json:"total_bytes"`
	}
	var s summary
	if c.prior {
		decode(c.t, c.run("cp", "show", "corpus", name, "--json"), &s)
		s.Dirs = s.Folders
	} else {
		var v struct {
			Checkpoint summary `json:"checkpoint"`
		}
		decode(c.t, c.run("--json", "cp", "show", "corpus", name), &v)
		s = v.Checkpoint
	}
	return checkpointStats{s.Name, s.Files, s.Dirs, s.Bytes}
}

type fileState struct {
	Kind   string
	Mode   uint32
	Size   int64
	SHA256 string
	Target string
}

func snapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".afs-sync" || rel == ".afs-lite-sync" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		e := fileState{Mode: uint32(info.Mode().Perm())}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			e.Kind = "symlink"
			e.Target, err = os.Readlink(path)
			e.Mode = 0
		case info.IsDir():
			e.Kind = "dir"
		case info.Mode().IsRegular():
			e.Kind = "file"
			e.Size = info.Size()
			var raw []byte
			raw, err = os.ReadFile(path)
			if err == nil {
				e.SHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
		default:
			return fmt.Errorf("unexpected file kind: %s", path)
		}
		if err != nil {
			return err
		}
		out[rel] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func equal(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("%s mismatch\ngot=%s\nwant=%s", label, g, w)
	}
}
func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The fixture covers regular and empty files, binary bytes, executable modes,
// empty directories, hidden paths, and a relative symbolic link.
func corpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "text.txt"), []byte("first line\nsecond line\n"), 0644)
	write(t, filepath.Join(root, "empty"), nil, 0644)
	write(t, filepath.Join(root, "blob.bin"), []byte{0, 255, 129, 0, 10, 13}, 0644)
	write(t, filepath.Join(root, "nested", "run.sh"), []byte("#!/bin/sh\nprintf hello\n"), 0755)
	write(t, filepath.Join(root, ".hidden"), []byte("hidden content\n"), 0640)
	if err := os.Mkdir(filepath.Join(root, "empty-dir"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("text.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOfflineHelpAndVersion(t *testing.T) {
	for _, prior := range []bool{true, false} {
		name, binary := "current", currentBinary
		if prior {
			name, binary = "prior", baselineBinary
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) ([]byte, error) {
				command := exec.Command(binary, append([]string{"--config", filepath.Join(dir, "missing.json")}, args...)...)
				command.Env = []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH")}
				return command.CombinedOutput()
			}
			help, err := run("--help")
			if err != nil || !bytes.Contains(help, []byte("Usage:")) {
				t.Fatalf("offline help: %v\n%s", err, help)
			}
			version, err := run("--version")
			if err != nil || !strings.HasPrefix(string(version), "afs ") {
				t.Fatalf("offline version: %v\n%s", err, version)
			}
			if !prior {
				for _, action := range []string{"create", "list", "info", "fork", "delete", "mount", "unmount", "status", "cp"} {
					if !regexp.MustCompile(`(?m)^\s+` + action + `(?:\s|$)`).Match(help) {
						t.Errorf("root help missing %s:\n%s", action, help)
					}
					out, err := run(action, "--help")
					if err != nil || !bytes.Contains(out, []byte("Usage:")) {
						t.Errorf("offline %s help: %v\n%s", action, err, out)
					}
				}
			}
			for _, removed := range []string{"ws", "fs"} {
				out, err := run(removed, "--help")
				if prior {
					if err != nil {
						t.Errorf("original %s help: %v\n%s", removed, err, out)
					}
				} else {
					if err == nil || !bytes.Contains(bytes.ToLower(out), []byte("unknown command")) {
						t.Errorf("removed %s must be rejected before config access: %v\n%s", removed, err, out)
					}
					if regexp.MustCompile(`(?m)^\s+` + removed + `(?:\s|$)`).Match(help) {
						t.Errorf("root help still advertises removed %s", removed)
					}
				}
			}
		})
	}
}

func TestCLIBehaviorMatchesPrior(t *testing.T) {
	source := corpus(t)
	imported := snapshot(t, source)
	type observation struct {
		Names                              []string
		Before, After                      checkpointStats
		Imported, Edited, Forked, Restored map[string]fileState
	}
	var priorResult observation
	for _, prior := range []bool{true, false} {
		name := "current"
		if prior {
			name = "prior"
		}
		ok := t.Run(name, func(t *testing.T) {
			c := newCLI(t, prior)
			var result observation
			c.mapped([]string{"vol", "create", "blank"}, []string{"create", "blank"})
			c.mapped([]string{"vol", "import", "corpus", source}, []string{"create", "corpus", "--from", source})
			result.Names = c.names()
			equal(t, "created workspace names", result.Names, []string{"blank", "corpus"})
			info := c.mapped([]string{"vol", "info", "corpus"}, []string{"--json", "info", "corpus"})
			if !bytes.Contains(info, []byte("corpus")) || !bytes.Contains(info, []byte("initial")) {
				t.Fatalf("workspace info missing name/head: %s", info)
			}
			c.checkpoint("before", "")
			result.Before = c.checkpointInfo("before")
			cps := c.mapped([]string{"cp", "list", "corpus"}, []string{"--json", "cp", "list", "corpus"})
			if !bytes.Contains(cps, []byte("before")) || !bytes.Contains(cps, []byte("initial")) {
				t.Fatalf("checkpoint list missing names: %s", cps)
			}
			c.mapped([]string{"vol", "fork", "corpus", "forked"}, []string{"fork", "corpus", "forked"})
			mount := filepath.Join(c.root, "mount")
			c.mount("corpus", mount)
			result.Imported = snapshot(t, mount)
			equal(t, "imported filesystem", result.Imported, imported)
			equal(t, "mounted text bytes", readFile(t, filepath.Join(mount, "text.txt")), []byte("first line\nsecond line\n"))
			equal(t, "mounted empty file", len(readFile(t, filepath.Join(mount, "empty"))), 0)
			equal(t, "mounted binary bytes", readFile(t, filepath.Join(mount, "blob.bin")), []byte{0, 255, 129, 0, 10, 13})
			write(t, filepath.Join(mount, "text.txt"), []byte("edited through the real folder mount\n"), 0644)
			if err := os.Rename(filepath.Join(mount, "nested", "run.sh"), filepath.Join(mount, "nested", "renamed.sh")); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(mount, "nested", "renamed.sh"), 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(mount, "empty")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(mount, "newdir", "large.bin"), bytes.Repeat([]byte{0, 255, 128, 65, 10}, 430001), 0644)
			c.checkpoint("after", mount)
			result.After = c.checkpointInfo("after")
			result.Edited = snapshot(t, mount)
			c.unmount(mount)
			equal(t, "unmount preserves local bytes and modes", snapshot(t, mount), result.Edited)
			// An independent materialization proves that saved binary bytes and metadata
			// reached Redis, rather than merely remaining in the writer's local folder.
			verify := filepath.Join(c.root, "verify")
			c.mount("corpus", verify)
			equal(t, "published tree after remount", snapshot(t, verify), result.Edited)
			equal(t, "published edit", readFile(t, filepath.Join(verify, "text.txt")), []byte("edited through the real folder mount\n"))
			c.unmount(verify)
			fork := filepath.Join(c.root, "fork")
			c.mount("forked", fork)
			result.Forked = snapshot(t, fork)
			equal(t, "fork unaffected by source edits", result.Forked, imported)
			c.unmount(fork)
			c.mapped([]string{"cp", "restore", "corpus", "before"}, []string{"cp", "restore", "corpus", "before", "--yes"})
			restored := filepath.Join(c.root, "restored")
			c.mount("corpus", restored)
			result.Restored = snapshot(t, restored)
			equal(t, "restored checkpoint", result.Restored, imported)
			c.unmount(restored)
			c.mapped([]string{"vol", "delete", "--no-confirmation", "forked"}, []string{"delete", "forked", "--yes"})
			equal(t, "workspace deletion", c.names(), []string{"blank", "corpus"})
			if prior {
				priorResult = result
			} else {
				equal(t, "normalized prior/current CLI behavior", result, priorResult)
			}
		})
		if !ok && prior {
			t.Fatal("prior CLI contract failed; derivative comparison cannot use an unverified baseline")
		}
	}
}

func TestMountPreservesPreexistingIgnoredFiles(t *testing.T) {
	source := t.TempDir()
	public := []byte("already published remote content\n")
	write(t, filepath.Join(source, "public.txt"), public, 0o644)
	remoteTree := snapshot(t, source)
	var priorTree map[string]fileState
	for _, prior := range []bool{true, false} {
		name := "current"
		if prior {
			name = "prior"
		}
		ok := t.Run(name, func(t *testing.T) {
			c := newCLI(t, prior)
			c.mapped([]string{"vol", "import", "corpus", source}, []string{"create", "corpus", "--from", source})
			mount := filepath.Join(c.root, "mount")
			write(t, filepath.Join(mount, ".afsignore"), []byte("private/\n"), 0o644)
			write(t, filepath.Join(mount, "private", "local.txt"), []byte("private local bytes must survive\n"), 0o600)
			want := snapshot(t, mount)
			want["public.txt"] = remoteTree["public.txt"]

			c.mount("corpus", mount)
			equal(t, "mount preserves ignore file and ignored local bytes", snapshot(t, mount), want)
			equal(t, "published public file", readFile(t, filepath.Join(mount, "public.txt")), public)
			// A completed save/checkpoint proves the exclusion survives a full
			// scan, rather than merely checking before the daemon can upload.
			c.checkpoint("ignored-preserved", mount)
			equal(t, "ignored paths stay out of checkpoint", c.checkpointInfo("ignored-preserved"), checkpointStats{Name: "ignored-preserved", Files: 1, Dirs: 0, Bytes: int64(len(public))})
			c.unmount(mount)
			got := snapshot(t, mount)
			equal(t, "unmount preserves ignored local bytes", got, want)
			if prior {
				priorTree = got
			} else {
				equal(t, "prior/current ignored-file preservation", got, priorTree)
			}

			verify := filepath.Join(c.root, "verify")
			c.mount("corpus", verify)
			equal(t, "independent mount contains only published files", snapshot(t, verify), remoteTree)
			c.unmount(verify)
		})
		if !ok && prior {
			t.Fatal("original CLI ignored-file behavior failed; comparison needs a verified baseline")
		}
	}
}

func readableDefault(t *testing.T, label string, raw []byte, required ...[]string) {
	t.Helper()
	text := strings.ToLower(strings.TrimSpace(string(raw)))
	if text == "" || json.Valid(raw) || strings.Contains(text, "map[") {
		t.Errorf("%s must default to readable text, got %q", label, raw)
	}
	for _, alternatives := range required {
		found := false
		for _, token := range alternatives {
			found = found || strings.Contains(text, strings.ToLower(token))
		}
		if !found {
			t.Errorf("%s missing readable context %v:\n%s", label, alternatives, raw)
		}
	}
}

// Default terminal output is part of the retained CLI behavior. Machine
// comparisons above deliberately request --json; file reads use real mounts.
func TestDefaultPresentationMatchesPrior(t *testing.T) {
	source := t.TempDir()
	content := []byte("{\"still\":\"exact file bytes\"}\n")
	write(t, filepath.Join(source, "readme.txt"), content, 0o644)
	for _, prior := range []bool{true, false} {
		name := "current"
		if prior {
			name = "prior"
		}
		t.Run(name, func(t *testing.T) {
			c := newCLI(t, prior)
			empty := c.mapped([]string{"vol", "list"}, []string{"list"})
			readableDefault(t, "empty workspace list", empty, []string{"no volumes", "no workspaces"})
			created := c.mapped([]string{"vol", "create", "blank"}, []string{"create", "blank"})
			readableDefault(t, "create confirmation", created, []string{"blank"}, []string{"created"})
			imported := c.mapped([]string{"vol", "import", "corpus", source}, []string{"create", "corpus", "--from", source})
			readableDefault(t, "import confirmation", imported, []string{"corpus"}, []string{"imported", "created"})
			list := c.mapped([]string{"vol", "list"}, []string{"list"})
			readableDefault(t, "workspace table", list, []string{"volume", "workspace", "name"}, []string{"blank"}, []string{"corpus"})
			info := c.mapped([]string{"vol", "info", "corpus"}, []string{"info", "corpus"})
			readableDefault(t, "workspace details", info, []string{"corpus"}, []string{"head"}, []string{"initial"})
			checkpoint := c.mapped([]string{"cp", "create", "--volume", "corpus", "before"}, []string{"cp", "create", "corpus", "--name", "before"})
			readableDefault(t, "checkpoint confirmation", checkpoint, []string{"before"}, []string{"created"})
			checkpoints := c.mapped([]string{"cp", "list", "corpus"}, []string{"cp", "list", "corpus"})
			readableDefault(t, "checkpoint table", checkpoints, []string{"checkpoint", "name"}, []string{"created"}, []string{"size", "bytes"}, []string{"before"}, []string{"initial"})
			detail := c.mapped([]string{"cp", "show", "corpus", "before"}, []string{"cp", "show", "corpus", "before"})
			readableDefault(t, "checkpoint details", detail, []string{"before"}, []string{"files"}, []string{"folders", "directories", "dirs"}, []string{"size", "bytes"})
			if !regexp.MustCompile(`(?im)^\s*files\s*:?\s+1\s*$`).Match(detail) {
				t.Errorf("checkpoint detail must label its one-file count:\n%s", detail)
			}
			// Explicit JSON stays available alongside the human defaults.
			equal(t, "explicit JSON workspace list", c.names(), []string{"blank", "corpus"})
			equal(t, "explicit JSON checkpoint counts", c.checkpointInfo("before"), checkpointStats{Name: "before", Files: 1, Dirs: 0, Bytes: int64(len(content))})
			mount := filepath.Join(c.root, "mount")
			mounted := c.mapped([]string{"vol", "mount", "corpus", mount}, []string{"mount", "corpus", mount})
			c.mounts[mount] = true
			equal(t, "JSON-looking mounted file keeps exact bytes", readFile(t, filepath.Join(mount, "readme.txt")), content)
			readableDefault(t, "mount confirmation", mounted, []string{"corpus"}, []string{mount}, []string{"mounted", "syncing"})
			readableDefault(t, "status table", c.run("status"), []string{"corpus"}, []string{"mount", "path", "directory"})
			unmounted := c.mapped([]string{"vol", "unmount", mount}, []string{"unmount", mount})
			delete(c.mounts, mount)
			readableDefault(t, "unmount confirmation", unmounted, []string{mount}, []string{"unmounted", "stopped"})
			deleted := c.mapped([]string{"vol", "delete", "--no-confirmation", "blank"}, []string{"delete", "blank", "--yes"})
			readableDefault(t, "delete confirmation", deleted, []string{"blank"}, []string{"deleted"})
		})
	}
}
