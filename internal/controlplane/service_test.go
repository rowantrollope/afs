package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func serviceFixture(t *testing.T) (*Service, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewService(NewStore(rdb)), rdb
}

func TestCheckpointForkRestoreAndReferenceDeletion(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	client := afsclient.New(rdb, meta.ID)
	large := bytes.Repeat([]byte{0, 1, 255, 2}, 2048)
	for name, data := range map[string][]byte{"/text": []byte("hello\n"), "/binary": large, "/empty": {}, "/nested/file": []byte("nested")} {
		if err = client.Echo(ctx, name, data); err != nil {
			t.Fatal(err)
		}
	}
	if err = client.Chmod(ctx, "/text", 0o750); err != nil {
		t.Fatal(err)
	}
	if err = client.Ln(ctx, "text", "/link"); err != nil {
		t.Fatal(err)
	}
	first, err := s.SaveCheckpointFromLive(ctx, "demo", "first")
	if err != nil {
		t.Fatal(err)
	}
	if first.FileCount != 4 {
		t.Fatalf("file count %d", first.FileCount)
	}
	if err = s.DeleteCheckpoint(ctx, "demo", "initial"); err == nil {
		t.Fatal("deleted the default checkpoint reference")
	}

	if err = s.ForkWorkspace(ctx, "demo", "fork", "first"); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetWorkspace(ctx, "fork")
	if err != nil {
		t.Fatal(err)
	}
	forkClient := afsclient.New(rdb, fork.ID)
	if err = client.Echo(ctx, "/binary", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCheckpointFromLive(ctx, "demo", "second"); err != nil {
		t.Fatal(err)
	}
	_, m, err := s.GetCheckpoint(ctx, "demo", "first")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ManifestEntryData(m.Entries["/binary"], func(id string) ([]byte, error) { return s.store.GetBlob(ctx, meta.ID, id) })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, large) {
		t.Fatal("immutable checkpoint changed")
	}
	generation, err := s.WorkspaceGeneration(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreCheckpoint(ctx, "demo", "first"); err != nil {
		t.Fatal(err)
	}
	nextGeneration, err := s.WorkspaceGeneration(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if generation == nextGeneration {
		t.Fatal("restore did not fence prior generation")
	}
	got, err = client.Cat(ctx, "/binary")
	if err != nil || !bytes.Equal(got, large) {
		t.Fatalf("restored bytes %d, %v", len(got), err)
	}
	stat, err := client.Stat(ctx, "/text")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode&0o777 != 0o750 {
		t.Fatalf("mode %o", stat.Mode)
	}
	if target, err := client.Readlink(ctx, "/link"); err != nil || target != "text" {
		t.Fatalf("symlink %q %v", target, err)
	}
	if err = s.DeleteCheckpoint(ctx, "demo", "first"); err == nil {
		t.Fatal("deleted current checkpoint")
	}
	if _, err = s.SaveCheckpointFromLive(ctx, "demo", "third"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCheckpoint(ctx, "demo", "first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.GetCheckpoint(ctx, "demo", "first"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted checkpoint: %v", err)
	}
	got, err = client.Cat(ctx, "/binary")
	if err != nil || !bytes.Equal(got, large) {
		t.Fatal("checkpoint deletion changed live content")
	}
	if err = s.DeleteWorkspace(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	got, err = forkClient.Cat(ctx, "/binary")
	if err != nil || !bytes.Equal(got, large) {
		t.Fatal("source deletion changed independent fork")
	}
	tombstone, err := rdb.Get(ctx, WorkspaceGenerationKey(meta.ID)).Result()
	if err != nil || tombstone != "deleted" {
		t.Fatalf("tombstone %q %v", tombstone, err)
	}
	replacement, err := s.CreateWorkspace(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == meta.ID {
		t.Fatal("deleted workspace ID reused")
	}
}

func TestImportManifestPreservesSupportedEntries(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	binary := bytes.Repeat([]byte{0, 255}, 4096)
	m := Manifest{Entries: map[string]ManifestEntry{
		"/": {Type: "dir", Mode: 0o750}, "/deep/empty": {Type: "file", Mode: 0o640},
		"/binary": {Type: "file", Mode: 0o600, Size: int64(len(binary)), BlobID: "binary"},
		"/text":   {Type: "file", Mode: 0o755, Size: 4, Inline: base64.StdEncoding.EncodeToString([]byte("text"))},
		"/link":   {Type: "symlink", Mode: 0o777, Target: "text"},
	}}
	meta, err := s.CreateWorkspaceFromManifest(ctx, "import", m, map[string][]byte{"binary": binary})
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	got, err := c.Cat(ctx, "/binary")
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("binary mismatch %v", err)
	}
	empty, err := c.Cat(ctx, "/deep/empty")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty file %v", err)
	}
	if _, err = s.CreateWorkspace(ctx, "import"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	// Original AFS names and data are not consulted.
	if err = rdb.Set(ctx, "afs:workspace:index:names", "unrelated", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListWorkspaces(ctx); err != nil || len(list) != 1 {
		t.Fatalf("namespace leak: %v %v", list, err)
	}
}

func TestRestoreSafetyCheckpointDoesNotTrustDirtyHint(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	if err = c.Echo(ctx, "/file", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCheckpointFromLive(ctx, "demo", "saved"); err != nil {
		t.Fatal(err)
	}
	if err = c.Echo(ctx, "/file", []byte("uncheckpointed")); err != nil {
		t.Fatal(err)
	}
	// Dirty is a hint. Exercise restore when it lags the committed file bytes.
	if err = rdb.Set(ctx, workspaceRootDirtyKey(meta.ID), "0", 0).Err(); err != nil {
		t.Fatal(err)
	}
	result, err := s.RestoreCheckpoint(ctx, "demo", "saved")
	if err != nil {
		t.Fatal(err)
	}
	if !result.SafetyCheckpointCreated {
		t.Fatal("live change not captured before replacement")
	}
	_, m, err := s.GetCheckpoint(ctx, "demo", result.SafetyCheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ManifestEntryData(m.Entries["/file"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "uncheckpointed" {
		t.Fatalf("safety capture %q", data)
	}
}

func TestCheckpointCommitRejectsReplacedGeneration(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.WorkspaceGeneration(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := s.GetCheckpoint(ctx, "demo", "initial")
	if err != nil {
		t.Fatal(err)
	}
	if err = rdb.Set(ctx, WorkspaceGenerationKey(meta.ID), "g_replacement", 0).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = s.saveCheckpoint(ctx, SaveCheckpointRequest{Workspace: meta.ID, ExpectedHead: "initial", ExpectedGeneration: old, CheckpointID: "stale", Manifest: m, AllowUnchanged: true, SkipWorkspaceRootSync: true})
	if !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("stale checkpoint commit: %v", err)
	}
	if _, _, err = s.GetCheckpoint(ctx, "demo", "stale"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale checkpoint persisted: %v", err)
	}
}

type faultHook struct {
	process  func(context.Context, redis.Cmder, redis.ProcessHook) error
	pipeline func(context.Context, []redis.Cmder, redis.ProcessPipelineHook) error
}

func (h faultHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h faultHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.process != nil {
			return h.process(ctx, cmd, next)
		}
		return next(ctx, cmd)
	}
}
func (h faultHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if h.pipeline != nil {
			return h.pipeline(ctx, cmds, next)
		}
		return next(ctx, cmds)
	}
}

func TestInterruptedRestoreStaysFencedAndCanBeRetried(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "restore")
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	if err = c.Echo(ctx, "/file", []byte("before")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCheckpointFromLive(ctx, "restore", "before"); err != nil {
		t.Fatal(err)
	}
	if err = c.Echo(ctx, "/file", []byte("uncheckpointed")); err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "hset" && len(cmd.Args()) > 1 && strings.Contains(cmd.Args()[1].(string), ":inode:") && failed.CompareAndSwap(false, true) {
				return errors.New("injected interruption during root publication")
			}
		}
		return next(ctx, cmds)
	}})
	if _, err = s.RestoreCheckpoint(ctx, "restore", "before"); err == nil {
		t.Fatal("injected failure was reported as success")
	}
	if _, err = s.WorkspaceGeneration(ctx, "restore"); err == nil {
		t.Fatal("incomplete restored tree accepted writers")
	}
	if _, err = s.RestoreCheckpoint(ctx, "restore", "before"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceGeneration(ctx, "restore"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Cat(ctx, "/file")
	if err != nil || string(got) != "before" {
		t.Fatalf("retry result %q %v", got, err)
	}
	found := false
	all, err := s.ListCheckpoints(ctx, "restore")
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range all {
		if cp.Kind == CheckpointKindSafety {
			_, m, e := s.GetCheckpoint(ctx, "restore", cp.ID)
			if e != nil {
				t.Fatal(e)
			}
			b, e := ManifestEntryData(m.Entries["/file"], nil)
			if e == nil && string(b) == "uncheckpointed" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("pre-restore content missing after interrupted publication")
	}
}

func TestWorkspaceDeletionDoesNotRemoveReusedNameMapping(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	old, err := s.CreateWorkspace(ctx, "reused")
	if err != nil {
		t.Fatal(err)
	}
	peer := redis.NewClient(rdb.Options())
	defer peer.Close()
	replacementService := NewService(NewStore(peer))
	var injected atomic.Bool
	var replacement WorkspaceMeta
	rdb.AddHook(faultHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
		err := next(ctx, cmd)
		if cmd.Name() == "scan" && strings.Contains(cmd.String(), workspacePattern(old.ID)) && injected.CompareAndSwap(false, true) {
			// Model deletion after its first metadata page but before old-name cleanup.
			if e := peer.Del(ctx, workspaceMetaKey(old.ID)).Err(); e != nil {
				return e
			}
			var e error
			replacement, e = replacementService.CreateWorkspace(ctx, "reused")
			if e != nil {
				return e
			}
		}
		return err
	}})
	if err = s.DeleteWorkspace(ctx, "reused"); err != nil {
		t.Fatal(err)
	}
	if !injected.Load() {
		t.Fatal("deletion race barrier did not execute")
	}
	current, err := s.GetWorkspace(ctx, "reused")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != replacement.ID {
		t.Fatalf("name maps to %q, want new ID %q", current.ID, replacement.ID)
	}
}
