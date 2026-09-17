//go:build integration

// Package e2e exercises the built CLI and independent folder-sync processes.
// Every test owns a disposable Redis server and separate client state roots.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

var binary string
var binaryDirectory string

func TestMain(m *testing.M) {
	binary = os.Getenv("AFS_E2E_BINARY")
	if binary == "" {
		dir, err := os.MkdirTemp("", "afs-e2e-binary-")
		if err != nil {
			panic(err)
		}
		binaryDirectory = dir
		binary = filepath.Join(dir, "afs")
		_, source, _, _ := runtime.Caller(0)
		root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
		build := exec.Command("go", "build", "-o", binary, "./cmd/afs")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build e2e binary: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	if binaryDirectory != "" {
		_ = os.RemoveAll(binaryDirectory)
	}
	os.Exit(code)
}

type redisServer struct {
	t         *testing.T
	dir, addr string
	command   *exec.Cmd
	client    *redis.Client
}

func newRedis(t *testing.T) *redisServer {
	t.Helper()
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Fatal("integration suite requires redis-server; tests were not run")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	r := &redisServer{t: t, dir: t.TempDir(), addr: addr}
	r.client = redis.NewClient(&redis.Options{Addr: addr, MaxRetries: 0, DialTimeout: time.Second, ReadTimeout: time.Second})
	t.Cleanup(func() { r.stop(); _ = r.client.Close() })
	r.start()
	return r
}
func (r *redisServer) start() {
	r.t.Helper()
	_, port, _ := net.SplitHostPort(r.addr)
	log, err := os.OpenFile(filepath.Join(r.dir, "redis.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		r.t.Fatal(err)
	}
	r.command = exec.Command("redis-server", "--bind", "127.0.0.1", "--port", port, "--save", "", "--appendonly", "yes", "--appendfsync", "always", "--dir", r.dir, "--loglevel", "warning")
	r.command.Stdout = log
	r.command.Stderr = log
	if err = r.command.Start(); err != nil {
		r.t.Fatal(err)
	}
	_ = log.Close()
	eventually(r.t, 10*time.Second, "owned Redis readiness", func() bool {
		info, err := r.client.Info(context.Background(), "server").Result()
		if err != nil {
			return false
		}
		// A released ephemeral port can be claimed by another process before
		// Redis binds. Never run mutations merely because that peer answers.
		for _, line := range strings.Split(info, "\n") {
			if strings.TrimSpace(line) == "process_id:"+strconv.Itoa(r.command.Process.Pid) {
				return true
			}
		}
		return false
	})
}
func (r *redisServer) stop() {
	if r.command != nil {
		_ = r.command.Process.Signal(syscall.SIGCONT)
		_ = r.command.Process.Signal(syscall.SIGTERM)
		_ = r.command.Wait()
		r.command = nil
	}
}
func (r *redisServer) signal(s syscall.Signal) {
	r.t.Helper()
	if err := r.command.Process.Signal(s); err != nil {
		r.t.Fatal(err)
	}
}
func (r *redisServer) url() string { return "redis://" + r.addr + "/0" }

type cli struct {
	t               *testing.T
	redis           *redisServer
	state, config   string
	mounts          map[string]int
	mountWorkspaces map[string]string
	writers         map[string]string
}

func newCLI(t *testing.T, r *redisServer) *cli {
	t.Helper()
	dir := t.TempDir()
	c := &cli{t: t, redis: r, state: filepath.Join(dir, "state"), config: filepath.Join(dir, "config.json"), mounts: map[string]int{}, mountWorkspaces: map[string]string{}, writers: map[string]string{}}
	if err := os.WriteFile(c.config, []byte(`{"redis":"`+r.url()+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			_ = filepath.WalkDir(c.state, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() == "sync.log" {
					raw, e := os.ReadFile(path)
					if e == nil {
						if len(raw) > 6000 {
							raw = raw[len(raw)-6000:]
						}
						t.Logf("daemon log %s:\n%s", path, raw)
					}
				}
				return nil
			})
		}
		for path, pid := range c.mounts {
			_, _, err := c.runTimeout(12*time.Second, nil, "unmount", path, "--force")
			if err != nil && pid > 0 {
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
			}
		}
	})
	return c
}
func (c *cli) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, append([]string{"--config", c.config, "--redis", c.redis.url()}, args...)...)
	cmd.Env = append(os.Environ(), "AFS_STATE_DIR="+c.state)
	return cmd
}
func (c *cli) runTimeout(timeout time.Duration, input []byte, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := c.command(ctx, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out, diagnostic bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	err := cmd.Run()
	return out.Bytes(), diagnostic.Bytes(), err
}
func (c *cli) run(input []byte, args ...string) []byte {
	c.t.Helper()
	out, diagnostic, err := c.runTimeout(45*time.Second, input, args...)
	if err != nil {
		c.t.Fatalf("afs %v: %v\nstdout=%s\nstderr=%s", args, err, out, diagnostic)
	}
	return out
}
func (c *cli) mustFail(args ...string) string {
	c.t.Helper()
	out, diagnostic, err := c.runTimeout(20*time.Second, nil, args...)
	if err == nil {
		c.t.Fatalf("afs %v unexpectedly succeeded: %s", args, out)
	}
	return string(out) + string(diagnostic)
}
func (c *cli) mount(workspace, path string) int {
	c.t.Helper()
	out := c.run(nil, "--json", "mount", workspace, path)
	var v struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		c.t.Fatal(err)
	}
	if v.PID <= 0 {
		c.t.Fatalf("mount PID missing: %s", out)
	}
	c.mounts[path] = v.PID
	c.mountWorkspaces[path] = workspace
	return v.PID
}
func (c *cli) unmount(path string) {
	c.t.Helper()
	c.run(nil, "unmount", path)
	delete(c.mounts, path)
	delete(c.mountWorkspaces, path)
}

// Published observations use only the retained native client's read operations.
// Pinning the live generation also prevents its ensureRoot path from creating
// any Redis state when a workspace has been removed or replaced.
func (c *cli) publishedReader(ctx context.Context, workspace string) (context.Context, client.Client, error) {
	store := controlplane.NewStore(c.redis.client)
	meta, err := store.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return ctx, nil, err
	}
	generation, err := store.WorkspaceGeneration(ctx, meta.ID)
	if err != nil {
		return ctx, nil, err
	}
	return client.WithWorkspaceGeneration(ctx, generation), client.New(c.redis.client, controlplane.WorkspaceFSKey(meta.ID)), nil
}
func (c *cli) remote(workspace, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx, reader, err := c.publishedReader(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return reader.Cat(ctx, "/"+path)
}
func (c *cli) published(workspace, path string) []byte {
	c.t.Helper()
	out, err := c.remote(workspace, path)
	if err != nil {
		c.t.Fatalf("read published %s/%s: %v", workspace, path, err)
	}
	return out
}
func (c *cli) awaitMissingPublished(workspace, path string) {
	c.t.Helper()
	eventually(c.t, 30*time.Second, "published path removed: "+workspace+"/"+path, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx, reader, err := c.publishedReader(ctx, workspace)
		if err != nil {
			return false
		}
		stat, err := reader.Stat(ctx, "/"+path)
		return err == nil && stat == nil
	})
}

// Test mutations are ordinary filesystem operations performed in real mounted
// directories. Reuse an explicit test mount where possible; otherwise create a
// test-owned writer mount which callers close before restore or deletion.
func (c *cli) writerRoot(workspace string) string {
	c.t.Helper()
	for root, mountedWorkspace := range c.mountWorkspaces {
		if mountedWorkspace == workspace {
			if _, active := c.mounts[root]; active {
				return root
			}
		}
	}
	root, err := os.MkdirTemp(filepath.Dir(c.state), "writer-")
	if err != nil {
		c.t.Fatal(err)
	}
	c.mount(workspace, root)
	c.writers[workspace] = root
	return root
}
func (c *cli) closeWriter(workspace string) {
	c.t.Helper()
	if root, ok := c.writers[workspace]; ok {
		c.unmount(root)
		delete(c.writers, workspace)
	}
}
func (c *cli) put(workspace, path string, data []byte) {
	c.t.Helper()
	write(c.t, filepath.Join(c.writerRoot(workspace), path), data)
	awaitRemote(c.t, c, workspace, path, data)
}
func (c *cli) removePublished(workspace, path string) {
	c.t.Helper()
	if err := os.RemoveAll(filepath.Join(c.writerRoot(workspace), path)); err != nil {
		c.t.Fatal(err)
	}
	c.awaitMissingPublished(workspace, path)
}
func (c *cli) movePublished(workspace, source, destination string) {
	c.t.Helper()
	root := c.writerRoot(workspace)
	data, err := os.ReadFile(filepath.Join(root, source))
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, source), filepath.Join(root, destination)); err != nil {
		c.t.Fatal(err)
	}
	awaitRemote(c.t, c, workspace, destination, data)
	c.awaitMissingPublished(workspace, source)
}
func eventually(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
func awaitFile(t *testing.T, path string, want []byte) {
	t.Helper()
	eventually(t, 30*time.Second, "file "+path, func() bool { got, err := os.ReadFile(path); return err == nil && bytes.Equal(got, want) })
}
func awaitRemote(t *testing.T, c *cli, workspace, path string, want []byte) {
	t.Helper()
	eventually(t, 30*time.Second, "remote "+path, func() bool { got, err := c.remote(workspace, path); return err == nil && bytes.Equal(got, want) })
}
func containsBytes(roots []string, want []byte) bool {
	for _, root := range roots {
		found := false
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, e := os.ReadFile(path)
				if e == nil && bytes.Equal(b, want) {
					found = true
				}
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}

func TestTwoIndependentWritableClientsAndCLI(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	a.run(nil, "create", "shared")
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	a.mount("shared", left)
	b.mount("shared", right)
	t.Run("exact mounted file bytes", func(t *testing.T) {
		a.t = t
		b.t = t
		for name, data := range map[string][]byte{"text": []byte("hello\n"), "binary": {0, 255, 1, 0, 128}, "empty": {}, "nested/file": []byte("nested")} {
			a.put("shared", name, data)
			got := a.published("shared", name)
			if !bytes.Equal(got, data) {
				t.Fatalf("round trip %s", name)
			}
			awaitFile(t, filepath.Join(left, name), data)
			awaitFile(t, filepath.Join(right, name), data)
		}
	})
	t.Run("disjoint concurrent writers", func(t *testing.T) {
		a.t = t
		b.t = t
		ready := make(chan struct{})
		var wg sync.WaitGroup
		for _, entry := range []struct{ root, name, value string }{{left, "from-left", "left"}, {right, "from-right", "right"}} {
			wg.Add(1)
			go func(root, name, value string) {
				defer wg.Done()
				<-ready
				_ = os.WriteFile(filepath.Join(root, name), []byte(value), 0o644)
			}(entry.root, entry.name, entry.value)
		}
		close(ready)
		wg.Wait()
		awaitFile(t, filepath.Join(right, "from-left"), []byte("left"))
		awaitFile(t, filepath.Join(left, "from-right"), []byte("right"))
	})
	t.Run("same path preserves competing bytes", func(t *testing.T) {
		a.t = t
		b.t = t
		a.put("shared", "race", []byte("base"))
		awaitFile(t, filepath.Join(left, "race"), []byte("base"))
		awaitFile(t, filepath.Join(right, "race"), []byte("base"))
		r.signal(syscall.SIGSTOP)
		write(t, filepath.Join(left, "race"), []byte("left competing version"))
		write(t, filepath.Join(right, "race"), []byte("right competing version"))
		r.signal(syscall.SIGCONT)
		eventually(t, 40*time.Second, "same-path convergence with preserved conflict", func() bool {
			x, e1 := os.ReadFile(filepath.Join(left, "race"))
			y, e2 := os.ReadFile(filepath.Join(right, "race"))
			remote, e3 := a.remote("shared", "race")
			return e1 == nil && e2 == nil && e3 == nil && bytes.Equal(x, y) && bytes.Equal(x, remote) && containsBytes([]string{left}, []byte("left competing version")) && containsBytes([]string{left}, []byte("right competing version")) && containsBytes([]string{right}, []byte("left competing version")) && containsBytes([]string{right}, []byte("right competing version"))
		})
	})
	t.Run("checkpoint flush and independent fork", func(t *testing.T) {
		a.t = t
		b.t = t
		write(t, filepath.Join(left, "checkpoint-local"), []byte("flushed"))
		a.run(nil, "cp", "create", "shared", "--name", "capture")
		a.run(nil, "fork", "shared", "fork", "--checkpoint", "capture")
		if got := a.published("fork", "checkpoint-local"); string(got) != "flushed" {
			t.Fatalf("local flush missing: %q", got)
		}
		a.put("shared", "checkpoint-local", []byte("changed"))
		if got := a.published("fork", "checkpoint-local"); string(got) != "flushed" {
			t.Fatal("fork changed with source")
		}
		awaitFile(t, filepath.Join(left, "checkpoint-local"), []byte("changed"))
		awaitFile(t, filepath.Join(right, "checkpoint-local"), []byte("changed"))
		a.run(nil, "cp", "create", "shared", "--name", "newer")
		a.run(nil, "cp", "list", "shared")
		a.run(nil, "cp", "show", "shared", "newer")
		a.run(nil, "cp", "delete", "shared", "capture", "--yes")
		a.mustFail("cp", "show", "shared", "capture")
		if got := a.published("fork", "checkpoint-local"); string(got) != "flushed" {
			t.Fatal("checkpoint deletion broke independent fork")
		}

	})
	t.Run("ownership and populated directory guards", func(t *testing.T) {
		a.t = t
		b.t = t
		if msg := b.mustFail("mount", "shared", left); msg == "" {
			t.Fatal("missing ownership error")
		}
		unrelated := filepath.Join(t.TempDir(), "unrelated")
		write(t, filepath.Join(unrelated, "private"), []byte("keep"))
		b.mustFail("mount", "shared", unrelated)
		awaitFile(t, filepath.Join(unrelated, "private"), []byte("keep"))
	})
	if t.Failed() {
		return
	}
	a.t = t
	b.t = t
	a.unmount(left)
	b.unmount(right)
	awaitFile(t, filepath.Join(left, "from-left"), []byte("left"))
	a.run(nil, "delete", "shared", "--yes")
	if got := a.published("fork", "checkpoint-local"); string(got) != "flushed" {
		t.Fatal("source deletion broke fork")
	}
}

func TestDisconnectCrashAndRootLoss(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	a.run(nil, "create", "recovery")
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	a.mount("recovery", left)
	a.put("recovery", "file", []byte("base"))
	pid := b.mount("recovery", right)
	awaitFile(t, filepath.Join(left, "file"), []byte("base"))
	awaitFile(t, filepath.Join(right, "file"), []byte("base"))
	t.Run("disconnect reconnect", func(t *testing.T) {
		a.t = t
		b.t = t
		r.stop()
		write(t, filepath.Join(left, "offline"), []byte("queued during outage"))
		r.start()
		awaitFile(t, filepath.Join(right, "offline"), []byte("queued during outage"))
	})
	t.Run("client crash and restart", func(t *testing.T) {
		a.t = t
		b.t = t
		p, _ := os.FindProcess(pid)
		if err := p.Kill(); err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, "stopped daemon", func() bool { return strings.Contains(string(b.run(nil, "--json", "status", right)), "stopped") })
		a.put("recovery", "after-crash", []byte("remote while stopped"))
		pid = b.mount("recovery", right)
		awaitFile(t, filepath.Join(right, "after-crash"), []byte("remote while stopped"))
	})
	t.Run("missing root never deletes remote", func(t *testing.T) {
		a.t = t
		b.t = t
		backup := left + "-preserved"
		if err := os.Rename(left, backup); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Rename(backup, left) }()
		// Reconnection requests a full scan while the local root is absent.
		r.stop()
		r.start()
		eventually(t, 35*time.Second, "full reconciliation refuses missing root", func() bool {
			return clientLogContains(a, "no such file") || clientLogContains(a, "local root unavailable") || clientLogContains(a, "does not exist")
		})
		got := a.published("recovery", "file")
		if string(got) != "base" {
			t.Fatal("missing root changed published tree")
		}
	})
	if t.Failed() {
		return
	}
	a.t = t
	b.t = t
	a.unmount(left)
	b.unmount(right)
}

func TestRestoreFencesOtherProcessAndPreservesPendingLocalData(t *testing.T) {
	r := newRedis(t)
	owner, peer := newCLI(t, r), newCLI(t, r)
	owner.run(nil, "create", "restore")
	owner.put("restore", "file", []byte("old"))
	owner.run(nil, "cp", "create", "restore", "--name", "old")
	root := filepath.Join(t.TempDir(), "peer")
	pid := peer.mount("restore", root)
	awaitFile(t, filepath.Join(root, "file"), []byte("old"))
	p, _ := os.FindProcess(pid)
	if err := p.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "file"), []byte("pending local bytes"))
	owner.put("restore", "file", []byte("published newer"))
	owner.closeWriter("restore")
	owner.run(nil, "cp", "restore", "restore", "old", "--yes")
	if err := p.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "stale peer reports a generation error", func() bool {
		out := strings.ToLower(string(peer.run(nil, "--json", "status", root)))
		return strings.Contains(out, "generation") || strings.Contains(out, "replaced") || strings.Contains(out, "restored") || strings.Contains(out, "deleted") || strings.Contains(out, "stale")
	})
	if got := owner.published("restore", "file"); string(got) != "old" {
		t.Fatalf("stale client republished: %q", got)
	}
	if !containsBytes([]string{root}, []byte("pending local bytes")) {
		t.Fatal("pending local bytes lost")
	}
	peer.run(nil, "unmount", root, "--force")
	delete(peer.mounts, root)
	msg := peer.mustFail("mount", "restore", root)
	if !strings.Contains(msg, "restor") {
		t.Fatalf("unexpected remount rejection: %s", msg)
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	peer.mount("restore", fresh)
	awaitFile(t, filepath.Join(fresh, "file"), []byte("old"))
	peer.unmount(fresh)
}

func TestForegroundAndOutputConfiguration(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "foreground")
	root := filepath.Join(t.TempDir(), "foreground")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := c.command(ctx, "mount", "foreground", root, "--foreground")
	log, err := os.Create(filepath.Join(t.TempDir(), "foreground.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	eventually(t, 20*time.Second, "foreground status running", func() bool {
		out, _, err := c.runTimeout(5*time.Second, nil, "--json", "status", root)
		return err == nil && strings.Contains(string(out), "running")
	})
	write(t, filepath.Join(root, "file"), []byte("foreground"))
	if err = cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("foreground shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("foreground shutdown did not finish")
	}
	if got := c.published("foreground", "file"); string(got) != "foreground" {
		t.Fatal("foreground shutdown did not flush")
	}
	// Help/version do not contact Redis, even with an unreachable explicit URL.
	for _, args := range [][]string{{"--redis", "redis://127.0.0.1:1/0", "--help"}, {"--redis", "redis://127.0.0.1:1/0", "--version"}} {
		c.run(nil, args...)
	}
	badConfig := []byte(`{"redis":"redis://127.0.0.1:1/0"}`)
	if err = os.WriteFile(c.config, badConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	c.run(nil, "list") // explicit --redis from harness overrides config.
	secret := "secret-e2e-" + strconv.Itoa(os.Getpid())
	out := c.mustFail("--redis", "redis://user:"+secret+"@127.0.0.1:1/0", "list")
	if strings.Contains(out, secret) {
		t.Fatal("connection error leaked password")
	}
}

func TestDeleteAndRenameVersusPausedWriter(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	a.run(nil, "create", "mutations")
	root := filepath.Join(t.TempDir(), "peer")
	pid := b.mount("mutations", root)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		out, diagnostic, err := b.runTimeout(5*time.Second, nil, "--json", "status", root)
		t.Logf("paused-writer status: %s; diagnostic=%s; error=%v", out, diagnostic, err)
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() {
				data, readErr := os.ReadFile(path)
				t.Logf("paused-writer local %s: %q; error=%v", path, data, readErr)
			}
			return nil
		})
		_ = filepath.WalkDir(b.state, func(path string, entry os.DirEntry, err error) error {
			if err == nil && filepath.Base(filepath.Dir(path)) == "sync" && filepath.Ext(path) == ".json" {
				data, readErr := os.ReadFile(path)
				t.Logf("paused-writer baseline %s: %s; error=%v", path, data, readErr)
			}
			return nil
		})
	})
	p, _ := os.FindProcess(pid)
	for _, op := range []string{"delete", "rename"} {
		a.put("mutations", op, []byte("base"))
		awaitFile(t, filepath.Join(root, op), []byte("base"))
		if err := p.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		changed := []byte(op + " local competing edit")
		write(t, filepath.Join(root, op), changed)
		if op == "delete" {
			a.removePublished("mutations", op)
		} else {
			a.movePublished("mutations", op, op+"-moved")
		}
		if err := p.Signal(syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		eventually(t, 30*time.Second, op+" reconciles with protected local edits", func() bool {
			out, _, err := b.runTimeout(5*time.Second, nil, "--json", "status", root)
			if err != nil || !strings.Contains(string(out), "running") {
				return false
			}
			if op == "rename" {
				data, e := os.ReadFile(filepath.Join(root, op+"-moved"))
				if e != nil || string(data) != "base" {
					return false
				}
			}
			return containsBytes([]string{root}, changed)
		})
		b.run(nil, "cp", "create", "mutations", "--name", op+"-reconciled")
		if !containsBytes([]string{root}, changed) {
			t.Fatalf("%s discarded modified content", op)
		}
	}
	b.unmount(root)
	a.closeWriter("mutations")
}

func TestUnmountOutageReportsFailureAndKeepsLocalFiles(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "outage")
	root := filepath.Join(t.TempDir(), "root")
	c.mount("outage", root)
	r.stop()
	write(t, filepath.Join(root, "pending"), []byte("local during outage"))
	_, diagnostic, err := c.runTimeout(3*time.Minute, nil, "unmount", root)
	r.start()
	if err == nil {
		t.Fatal("unmount falsely reported successful synchronization during outage")
	}
	if len(diagnostic) == 0 {
		t.Fatalf("unmount did not report its failed flush: %v", err)
	}
	awaitFile(t, filepath.Join(root, "pending"), []byte("local during outage"))
	awaitRemote(t, c, "outage", "pending", []byte("local during outage"))
	c.unmount(root)
}

func TestWorkspaceDeletionFencesPausedClient(t *testing.T) {
	r := newRedis(t)
	owner, peer := newCLI(t, r), newCLI(t, r)
	owner.run(nil, "create", "deletion-case")
	root := filepath.Join(t.TempDir(), "peer")
	pid := peer.mount("deletion-case", root)
	p, _ := os.FindProcess(pid)
	if err := p.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "pending"), []byte("preserve after workspace deletion"))
	owner.run(nil, "delete", "deletion-case", "--yes")
	if err := p.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "deleted workspace client reports fencing", func() bool {
		out := strings.ToLower(string(peer.run(nil, "--json", "status", root)))
		return strings.Contains(out, "workspace was restored or deleted") || strings.Contains(out, "generation") || strings.Contains(out, "stale")
	})
	if !containsBytes([]string{root}, []byte("preserve after workspace deletion")) {
		t.Fatal("workspace deletion lost local unpublished bytes")
	}
	owner.mustFail("info", "deletion-case")
	peer.run(nil, "unmount", root, "--force")
	delete(peer.mounts, root)
}

func TestPerformanceSmoke(t *testing.T) {
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	source := t.TempDir()
	for i := 0; i < 1000; i++ {
		write(t, filepath.Join(source, fmt.Sprintf("dir-%02d/file-%04d", i%20, i)), bytes.Repeat([]byte("workspace bytes\n"), 32))
	}
	start := time.Now()
	a.run(nil, "create", "performance", "--from", source)
	t.Logf("import 1000 files: %s", time.Since(start))
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	start = time.Now()
	a.mount("performance", left)
	b.mount("performance", right)
	awaitFile(t, filepath.Join(right, "dir-19/file-0999"), bytes.Repeat([]byte("workspace bytes\n"), 32))
	t.Logf("initial sync, two independent 1000-file mounts: %s", time.Since(start))
	start = time.Now()
	write(t, filepath.Join(left, "dir-19/file-0999"), []byte("small edit"))
	awaitFile(t, filepath.Join(right, "dir-19/file-0999"), []byte("small edit"))
	t.Logf("small edit in 1000-file tree: %s", time.Since(start))
	start = time.Now()
	a.run(nil, "cp", "create", "performance", "--name", "synchronized")
	t.Logf("checkpoint after synchronization: %s", time.Since(start))
	info, _ := r.client.Info(context.Background(), "stats").Result()
	before := statNumber(info, "total_commands_processed")
	start = time.Now()
	timer := time.NewTimer(2 * time.Second)
	<-timer.C
	info, _ = r.client.Info(context.Background(), "stats").Result()
	after := statNumber(info, "total_commands_processed")
	t.Logf("two idle mounts: %.1f Redis commands/sec over %s", float64(after-before)/time.Since(start).Seconds(), time.Since(start))
	a.unmount(left)
	b.unmount(right)
}
func statNumber(info, key string) int64 {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, key+":") {
			n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, key+":")), 10, 64)
			return n
		}
	}
	return 0
}

func clientLogContains(c *cli, text string) bool {
	found := false
	_ = filepath.WalkDir(c.state, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "sync.log" {
			b, e := os.ReadFile(path)
			if e == nil && strings.Contains(strings.ToLower(string(b)), strings.ToLower(text)) {
				found = true
			}
		}
		return nil
	})
	return found
}

func TestImportedMetadataAndUnreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unreadable-directory check requires an unprivileged process")
	}
	r := newRedis(t)
	a, b := newCLI(t, r), newCLI(t, r)
	source := t.TempDir()
	write(t, filepath.Join(source, "sub", "executable"), []byte("#!/bin/sh\n"))
	write(t, filepath.Join(source, "outside"), []byte("keep symlink target"))
	if err := os.Symlink("../outside", filepath.Join(source, "sub", "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "sub", "executable"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/executable", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	a.run(nil, "create", "metadata", "--from", source)
	left, right := filepath.Join(t.TempDir(), "left"), filepath.Join(t.TempDir(), "right")
	a.mount("metadata", left)
	b.mount("metadata", right)
	for _, root := range []string{left, right} {
		awaitFile(t, filepath.Join(root, "sub", "executable"), []byte("#!/bin/sh\n"))
		info, err := os.Stat(filepath.Join(root, "sub", "executable"))
		if err != nil || info.Mode().Perm() != 0o751 {
			t.Fatalf("file mode after import/mount: %v %v", info, err)
		}
		info, err = os.Stat(filepath.Join(root, "sub"))
		if err != nil || info.Mode().Perm() != 0o750 {
			t.Fatalf("directory mode after import/mount: %v %v", info, err)
		}
		target, err := os.Readlink(filepath.Join(root, "link"))
		if err != nil || target != "sub/executable" {
			t.Fatalf("symlink after import/mount: %q %v", target, err)
		}
	}
	if err := os.Chmod(filepath.Join(left, "sub", "executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "permission change reaches peer", func() bool {
		info, err := os.Stat(filepath.Join(right, "sub", "executable"))
		return err == nil && info.Mode().Perm() == 0o600
	})
	// Execute-only permits the control request through its known private path,
	// while directory enumeration for the flush is denied by the kernel.
	if err := os.Chmod(left, 0o111); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(left, 0o755)
	message := a.mustFail("cp", "create", "metadata", "--name", "unreadable")
	if !strings.Contains(strings.ToLower(message), "permission denied") {
		t.Fatalf("unreadable root did not report its scan failure: %s", message)
	}
	a.mustFail("cp", "show", "metadata", "unreadable")
	if got := b.published("metadata", "sub/executable"); string(got) != "#!/bin/sh\n" {
		t.Fatal("unreadable root changed remote content")
	}
	if err := os.Chmod(left, 0o755); err != nil {
		t.Fatal(err)
	}
	a.removePublished("metadata", "sub")
	for _, root := range []string{left, right} {
		eventually(t, 30*time.Second, "recursive delete reaches "+root, func() bool { _, err := os.Lstat(filepath.Join(root, "sub")); return os.IsNotExist(err) })
		awaitFile(t, filepath.Join(root, "outside"), []byte("keep symlink target"))
	}
	a.unmount(left)
	b.unmount(right)
}

func TestReplacedRootRejectedLiveAndOnRestart(t *testing.T) {
	r := newRedis(t)
	owner, peer := newCLI(t, r), newCLI(t, r)
	owner.run(nil, "create", "identity")
	owner.put("identity", "kept", []byte("published stays"))
	root := filepath.Join(t.TempDir(), "root")
	pid := peer.mount("identity", root)
	awaitFile(t, filepath.Join(root, "kept"), []byte("published stays"))
	preserved := root + "-original"
	if err := os.Rename(root, preserved); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "unrelated"), []byte("replacement directory"))
	owner.put("identity", "new-remote", []byte("remote event"))
	eventually(t, 30*time.Second, "running daemon rejects replacement local root", func() bool {
		return clientLogContains(peer, "root changed") || clientLogContains(peer, "replaced") || clientLogContains(peer, "root identity")
	})
	owner.awaitMissingPublished("identity", "unrelated")
	if got := owner.published("identity", "kept"); string(got) != "published stays" {
		t.Fatal("replacement root deleted remote files")
	}
	awaitFile(t, filepath.Join(root, "unrelated"), []byte("replacement directory"))
	p, _ := os.FindProcess(pid)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	delete(peer.mounts, root)
	message := peer.mustFail("mount", "identity", root)
	if !strings.Contains(strings.ToLower(message), "replaced") && !strings.Contains(strings.ToLower(message), "identity") {
		t.Fatalf("unexpected replaced-root rejection: %s", message)
	}
	awaitFile(t, filepath.Join(root, "unrelated"), []byte("replacement directory"))
	owner.closeWriter("identity")
}

func TestStaleRegistryDoesNotSignalUnrelatedProcess(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "stale")
	root := filepath.Join(t.TempDir(), "root")
	c.mount("stale", root)
	registryPath := filepath.Join(c.state, "mounts.json")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err = json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	c.unmount(root)
	dummy := exec.Command("sleep", "60")
	if err = dummy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dummy.Process.Kill(); _ = dummy.Wait() })
	mounts := registry["mounts"].([]any)
	mounts[0].(map[string]any)["pid"] = dummy.Process.Pid
	raw, err = json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c.mustFail("unmount", root)
	c.run(nil, "unmount", root, "--force")
	if err = dummy.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("stale PID metadata terminated unrelated test process: %v", err)
	}
}

func TestRedisConnectionFailureFriendlyError(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	r.stop()
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"list"}
			if mode == "json" {
				args = append([]string{"--json"}, args...)
			}
			out, diagnostic, err := c.runTimeout(20*time.Second, nil, args...)
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("expected exit 1, got %v", err)
			}
			if len(out) != 0 {
				t.Fatalf("unexpected stdout: %s", out)
			}
			want := "afs: Cannot connect to Redis on " + r.url() + "\n\nPoint to your Redis server using \"afs --redis <url> list\".\n"
			if string(diagnostic) != want {
				t.Fatalf("connection error = %q, want %q", diagnostic, want)
			}
		})
	}
}
