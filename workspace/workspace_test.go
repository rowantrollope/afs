package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

// Each fixture launches fresh loopback Redis with its own empty data directory.
// No test uses environment Redis URLs or an existing user server.
func testStore(t *testing.T) (*Store, *redis.Client, context.Context) {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	directory := t.TempDir()
	config := fmt.Sprintf("bind 127.0.0.1\nport %d\nprotected-mode yes\nsave \"\"\nappendonly no\ndir %s\n", port, directory)
	configPath := filepath.Join(directory, "redis.conf")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(directory, "redis.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, configPath)
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		_ = log.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	t.Cleanup(cancel)
	store, err := New(fmt.Sprintf("redis://127.0.0.1:%d/0", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rdb := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
	t.Cleanup(func() { _ = rdb.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for rdb.Ping(ctx).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return store, rdb, ctx
}

func mustEnsure(t *testing.T, store *Store, ctx context.Context, name string) *Workspace {
	t.Helper()
	w, err := store.Ensure(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestConcurrentEnsurePreservesExistingRoot(t *testing.T) {
	store, rdb, ctx := testStore(t)
	const workers = 8
	type result struct {
		workspace *Workspace
		err       error
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			w, err := store.Ensure(ctx, "shared")
			results <- result{w, err}
		}()
	}
	group.Wait()
	close(results)
	var first *Workspace
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if first == nil {
			first = result.workspace
		} else if result.workspace.StorageID != first.StorageID || result.workspace.Generation != first.Generation {
			t.Fatal("parallel ensure created multiple identities")
		}
	}
	if first.Name != "shared" || first.FSKey != first.StorageID {
		t.Fatal("identity fields differ")
	}
	if created, err := first.CreateArtifact(ctx, "brief.md", []byte("retained live text"), 0640); err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	// The usual bootstrap helper would rematerialize an old checkpoint when
	// this marker is missing. Request-scoped opens must retain the live tree.
	if err := rdb.Del(ctx, "afs:{"+first.StorageID+"}:root_head_savepoint").Err(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		current := mustEnsure(t, store, ctx, "shared")
		body, err := current.Client().Cat(ctx, "/brief.md")
		if err != nil || string(body) != "retained live text" || current.Generation != first.Generation {
			t.Fatalf("existing root changed: %q %v", body, err)
		}
	}
	metas, err := store.service.ListWorkspaces(ctx)
	if err != nil || len(metas) != 1 {
		t.Fatalf("catalog changed: %d %v", len(metas), err)
	}
}

func TestOpenDoesNotCreateOrRepairRoot(t *testing.T) {
	store, rdb, ctx := testStore(t)
	if _, err := store.Open(ctx, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open missing: %v", err)
	}
	if size, err := rdb.DBSize(ctx).Result(); err != nil || size != 0 {
		t.Fatalf("Open created state: %d %v", size, err)
	}
	w := mustEnsure(t, store, ctx, "damaged")
	if err := rdb.Del(ctx, "afs:{"+w.FSKey+"}:inode:1").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ensure(ctx, w.Name); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("root must fail closed: %v", err)
	}
	if rdb.Exists(ctx, "afs:{"+w.FSKey+"}:inode:1").Val() != 0 {
		t.Fatal("existing root was reset")
	}
}

func TestConcurrentArtifactsRetryAndConflict(t *testing.T) {
	store, _, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "artifacts")
	data := bytes.Repeat([]byte("full text\n"), 12000)
	const workers = 6
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			created, err := w.CreateArtifact(ctx, "handoffs/brief.md", data, 0644)
			results <- result{created, err}
		}()
	}
	group.Wait()
	close(results)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created %d copies", createdCount)
	}
	if body, err := w.Client().Cat(ctx, "/handoffs/brief.md"); err != nil || !bytes.Equal(body, data) {
		t.Fatalf("read-after-write: %v", err)
	}
	if created, err := w.CreateArtifact(ctx, "handoffs/brief.md", []byte("conflict"), 0644); created || !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting write: %v %v", created, err)
	}
	if entries, err := w.Client().LsLong(ctx, "/"+StagingDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("staging leaked: %d %v", len(entries), err)
	}
	if stat, err := w.Client().Stat(ctx, "/handoffs/brief.md"); err != nil || stat.Mode != 0644 {
		t.Fatalf("mode: %+v %v", stat, err)
	}

	barrier := make(chan struct{})
	conflicts := make(chan result, 2)
	for _, content := range []string{"candidate one", "candidate two"} {
		group.Add(1)
		go func(data string) {
			defer group.Done()
			<-barrier
			created, err := w.CreateArtifact(ctx, "winner.md", []byte(data), 0600)
			conflicts <- result{created, err}
		}(content)
	}
	close(barrier)
	group.Wait()
	close(conflicts)
	winners, losers := 0, 0
	for result := range conflicts {
		if result.created && result.err == nil {
			winners++
		} else if !result.created && errors.Is(result.err, ErrAlreadyExists) {
			losers++
		} else {
			t.Fatalf("create conflict: %+v", result)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("winners=%d losers=%d", winners, losers)
	}
}

type renameHook struct {
	client.Client
	before func(context.Context, string, string)
}

func (h *renameHook) Rename(ctx context.Context, src, dst string, flags uint32) error {
	h.before(ctx, src, dst)
	return h.Client.Rename(ctx, src, dst, flags)
}

func TestArtifactPublishesOnlyCompleteTarget(t *testing.T) {
	store, _, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "atomic")
	base := w.Client()
	data := bytes.Repeat([]byte("complete UTF-8 text\n"), 20000)
	w.fs = &renameHook{Client: base, before: func(ctx context.Context, src, dst string) {
		if stat, err := base.Stat(ctx, dst); err != nil || stat != nil {
			t.Fatalf("partial target: %+v %v", stat, err)
		}
		if body, err := base.Cat(ctx, src); err != nil || !bytes.Equal(body, data) {
			t.Fatalf("incomplete staging: %v", err)
		}
	}}
	if created, err := w.CreateArtifact(ctx, "atomic.md", data, 0644); err != nil || !created {
		t.Fatalf("publish: %v %v", created, err)
	}
	if body, err := base.Cat(ctx, "/atomic.md"); err != nil || !bytes.Equal(body, data) {
		t.Fatalf("published bytes: %v", err)
	}
}

func TestArtifactParentAndGenerationFences(t *testing.T) {
	for _, scenario := range []string{"parent", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			store, rdb, ctx := testStore(t)
			w := mustEnsure(t, store, ctx, "fenced")
			base := w.Client()
			w.fs = &renameHook{Client: base, before: func(_ context.Context, src, dst string) {
				peer := context.Background()
				if scenario == "parent" {
					if err := base.Rename(peer, "/handoffs", "/retired", client.RenameNoreplace); err != nil {
						t.Fatal(err)
					}
					if err := base.Mkdir(peer, "/handoffs"); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := rdb.Set(peer, controlplane.WorkspaceGenerationKey(w.StorageID), "g_replacement", 0).Err(); err != nil {
						t.Fatal(err)
					}
				}
			}}
			created, err := w.CreateArtifact(ctx, "handoffs/brief.md", []byte("guarded"), 0644)
			if created || err == nil {
				t.Fatalf("stale publication: %v %v", created, err)
			}
			if scenario == "parent" {
				if !errors.Is(err, client.ErrWriteConflict) {
					t.Fatalf("parent error: %v", err)
				}
				if stat, err := base.Stat(ctx, "/handoffs/brief.md"); err != nil || stat != nil {
					t.Fatalf("new parent changed: %+v %v", stat, err)
				}
			} else {
				if !errors.Is(err, ErrWorkspaceChanged) {
					t.Fatalf("generation error: %v", err)
				}
				if err := base.Echo(ctx, "/stale.md", []byte("stale")); !errors.Is(err, ErrWorkspaceChanged) {
					t.Fatalf("Client lost fencing: %v", err)
				}
				if _, err := base.Info(ctx); !errors.Is(err, ErrWorkspaceChanged) {
					t.Fatalf("Info lost fencing: %v", err)
				}
			}
		})
	}
}

func TestCleanupRetainsForeignStaging(t *testing.T) {
	store, _, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "cleanup")
	base := w.Client()
	var foreignPath string
	w.fs = &renameHook{Client: base, before: func(_ context.Context, src, dst string) {
		foreignPath = src
		if err := base.Rm(context.Background(), src); err != nil {
			t.Fatal(err)
		}
		if err := base.Echo(context.Background(), src, []byte("peer allocated inode")); err != nil {
			t.Fatal(err)
		}
	}}
	if created, err := w.CreateArtifact(ctx, "result.md", []byte("our artifact"), 0644); created || err == nil {
		t.Fatalf("replaced source: %v %v", created, err)
	}
	if body, err := base.Cat(ctx, foreignPath); err != nil || string(body) != "peer allocated inode" {
		t.Fatalf("foreign staging removed: %q %v", body, err)
	}
	if stat, err := base.Stat(ctx, "/result.md"); err != nil || stat != nil {
		t.Fatalf("foreign bytes published: %+v %v", stat, err)
	}
}

func TestArtifactRejectsSymlinksPathsAndInvalidText(t *testing.T) {
	store, _, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "validation")
	if err := w.Client().Mkdir(ctx, "/actual"); err != nil {
		t.Fatal(err)
	}
	if err := w.Client().Ln(ctx, "/actual", "/link"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", ".", "../outside", "/absolute", "nested/../file", "nested//file", "bad\\path", "bad\npath", StagingDirectory + "/own", "nested/" + StagingDirectory + "/own", "link/file"} {
		if created, err := w.CreateArtifact(ctx, p, []byte("content"), 0644); created || !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("path %q: %v %v", p, created, err)
		}
	}
	for _, data := range [][]byte{{0xff, 0xfe}, bytes.Repeat([]byte("x"), MaxArtifactBytes+1)} {
		if created, err := w.CreateArtifact(ctx, "invalid.md", data, 0644); created || !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("body: %v %v", created, err)
		}
	}
	if created, err := w.CreateArtifact(ctx, "mode.md", []byte("text"), 04644); created || !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("mode: %v %v", created, err)
	}
	if entries, err := w.Client().Ls(ctx, "/actual"); err != nil || len(entries) != 0 {
		t.Fatalf("symlink write: %v %v", entries, err)
	}
}

func TestCanceledPublicationCleansOwnStagingAndRetries(t *testing.T) {
	store, _, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "cancel")
	base := w.Client()
	canceled, cancel := context.WithCancel(ctx)
	w.fs = &renameHook{Client: base, before: func(context.Context, string, string) { cancel() }}
	if created, err := w.CreateArtifact(canceled, "handoff.md", []byte("retry text"), 0644); created || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %v", created, err)
	}
	if entries, err := base.Ls(ctx, "/"+StagingDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("staging retained: %v %v", entries, err)
	}
	w.fs = base
	if created, err := w.CreateArtifact(ctx, "handoff.md", []byte("retry text"), 0644); err != nil || !created {
		t.Fatalf("retry: %v %v", created, err)
	}
}

func TestInvalidRedisURLDoesNotExposeCredentials(t *testing.T) {
	_, err := New("invalid://owner:secret@host")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

// This optional process check uses a freshly built CLI and a private read-only
// folder mount to prove compatibility with independently running AFS clients.
func TestArtifactIndependentFolderObserver(t *testing.T) {
	binary := os.Getenv("AFS_REQUEST_TEST_BINARY")
	if binary == "" {
		t.Skip("set AFS_REQUEST_TEST_BINARY to a test-built AFS CLI")
	}
	store, rdb, ctx := testStore(t)
	w := mustEnsure(t, store, ctx, "observer")
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(directory, "files")
	state := filepath.Join(directory, "state")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "AFS_") {
			env = append(env, entry)
		}
	}
	env = append(env, "AFS_STATE_DIR="+state, "AFS_REDIS_URL=redis://"+rdb.Options().Addr+"/0")
	run := func(commandContext context.Context, args ...string) ([]byte, error) {
		command := exec.CommandContext(commandContext, binary, append([]string{"--config", configPath}, args...)...)
		command.Env = env
		return command.CombinedOutput()
	}
	if output, err := run(ctx, "mount", w.Name, mount, "--readonly"); err != nil {
		t.Fatalf("private observer mount failed: %v %s", err, output)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if output, err := run(cleanup, "unmount", mount); err != nil {
			t.Errorf("private observer unmount failed: %v %s", err, output)
		}
	})
	data := []byte("# Real AFS observer\n\nWritten through the request API.\n")
	if created, err := w.CreateArtifact(ctx, "handoffs/observer.md", data, 0644); err != nil || !created {
		t.Fatalf("request API artifact: %v %v", created, err)
	}
	target := filepath.Join(mount, "handoffs", "observer.md")
	deadline := time.Now().Add(12 * time.Second)
	for {
		body, err := os.ReadFile(target)
		if err == nil && bytes.Equal(body, data) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("independent AFS observer did not receive complete artifact")
		}
		time.Sleep(40 * time.Millisecond)
	}
	reopened, err := store.Open(ctx, w.Name)
	if err != nil || reopened.StorageID != w.StorageID || reopened.Generation != w.Generation {
		t.Fatalf("observer changed workspace identity: %v", err)
	}
}
