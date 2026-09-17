package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func enableWorkspaceHistory(t *testing.T, rdb *redis.Client, id string) {
	t.Helper()
	if err := filehistory.SetPolicy(context.Background(), rdb, id, filehistory.Policy{Mode: filehistory.ModeAll}); err != nil {
		t.Fatal(err)
	}
}

func historyPage(t *testing.T, rdb *redis.Client, id, path string) filehistory.Page {
	t.Helper()
	page, err := filehistory.List(context.Background(), rdb, id, path, 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestCheckpointForkCopiesIndependentCompleteHistory(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "source-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/later", []byte("recover me")); err != nil {
		t.Fatal(err)
	}
	sourcePage := historyPage(t, rdb, meta.ID, "/file")
	if len(sourcePage.Versions) != 2 {
		t.Fatalf("source versions = %+v", sourcePage)
	}
	if err := s.ForkWorkspace(ctx, meta.ID, "fork-history", "first"); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetWorkspace(ctx, "fork-history")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := filehistory.GetPolicy(ctx, rdb, fork.ID)
	if err != nil || policy.Mode != filehistory.ModeAll {
		t.Fatalf("fork policy = %+v, %v", policy, err)
	}
	forkPage := historyPage(t, rdb, fork.ID, "/file")
	if len(forkPage.Versions) != 3 || forkPage.FileID != sourcePage.FileID || forkPage.Versions[0].Operation != "checkpoint_fork" {
		t.Fatalf("fork history = %+v", forkPage)
	}
	later := historyPage(t, rdb, fork.ID, "/later")
	if len(later.Versions) != 2 || !later.Versions[0].Deleted {
		t.Fatalf("later history = %+v", later)
	}
	if err := s.DeleteWorkspace(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	for selector, want := range map[string]string{"1": "first", "2": "second", "3": "first"} {
		_, body, err := filehistory.Get(ctx, rdb, fork.ID, "/file", selector, "")
		if err != nil || string(body) != want {
			t.Fatalf("fork version %s = %q, %v", selector, body, err)
		}
	}
	if err := afsclient.New(rdb, fork.ID).Echo(ctx, "/file", []byte("fork edit")); err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, fork.ID, "/file")
	if page.FileID != sourcePage.FileID || len(page.Versions) != 4 {
		t.Fatalf("continued fork history = %+v", page)
	}
}

func TestCheckpointRestorePreservesHistoryAcrossRenumberingAndDeletion(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "restore-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/z-file", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	// Creation order differs from manifest order, forcing different inode IDs
	// when the checkpoint tree is materialized.
	if err := c.Echo(ctx, "/a-base", []byte("base")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	first := historyPage(t, rdb, meta.ID, "/z-file")
	if err := c.Echo(ctx, "/z-file", []byte("uncheckpointed")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/a-later", []byte("still recoverable")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, meta.ID, "/z-file")
	if page.FileID != first.FileID || len(page.Versions) != 3 || page.Versions[0].Operation != "checkpoint_restore" {
		t.Fatalf("restored history = %+v", page)
	}
	_, body, err := filehistory.Get(ctx, rdb, meta.ID, "/z-file", "2", "")
	if err != nil || string(body) != "uncheckpointed" {
		t.Fatalf("pre-restore content = %q, %v", body, err)
	}
	later := historyPage(t, rdb, meta.ID, "/a-later")
	if !later.Versions[0].Deleted {
		t.Fatal("restoration omitted tombstone for removed file")
	}
	_, body, err = filehistory.Get(ctx, rdb, meta.ID, "/a-later", "latest", "")
	if err != nil || string(body) != "still recoverable" {
		t.Fatalf("removed file = %q, %v", body, err)
	}
	if err := c.Echo(ctx, "/z-file", []byte("after restore")); err != nil {
		t.Fatal(err)
	}
	page = historyPage(t, rdb, meta.ID, "/z-file")
	if page.FileID != first.FileID || len(page.Versions) != 4 {
		t.Fatalf("continued history = %+v", page)
	}
}

func TestCheckpointRestoreReusesRenamedLineage(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "renamed-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/before", []byte("same bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "before"); err != nil {
		t.Fatal(err)
	}
	initial := historyPage(t, rdb, meta.ID, "/before")
	if err := c.Rename(ctx, "/before", "/after", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "before"); err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, meta.ID, "/before")
	if page.FileID != initial.FileID || len(page.Versions) != 3 || page.Versions[0].PreviousPath != "/after" {
		t.Fatalf("restored rename lineage = %+v", page)
	}
}

func TestHistoryForkFailureDoesNotPublishDestination(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "source-failure")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "copy" && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":history:") {
				return errors.New("injected history copy failure")
			}
		}
		return next(ctx, cmds)
	}})
	if err := s.ForkWorkspace(ctx, meta.ID, "unpublished", "initial"); err == nil {
		t.Fatal("history failure accepted")
	}
	if exists, err := s.store.WorkspaceExists(ctx, "unpublished"); err != nil || exists {
		t.Fatalf("failed fork was exposed: %v, %v", exists, err)
	}
}

func TestInterruptedRestoreRetainsOriginalHistory(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "interrupted-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("uncheckpointed")); err != nil {
		t.Fatal(err)
	}
	initial := historyPage(t, rdb, meta.ID, "/file")
	var failed atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "hset" && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":inode:") && failed.CompareAndSwap(false, true) {
				return errors.New("injected publication failure")
			}
		}
		return next(ctx, cmds)
	}})
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err == nil {
		t.Fatal("interruption accepted")
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, meta.ID, "/file")
	if page.FileID != initial.FileID {
		t.Fatalf("retry changed lineage: %+v", page)
	}
	_, body, err := filehistory.Get(ctx, rdb, meta.ID, "/file", "2", "")
	if err != nil || string(body) != "uncheckpointed" {
		t.Fatalf("original history lost: %q, %v", body, err)
	}
}

func TestHistoryForkRetriesConcurrentSourcePublication(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "concurrent-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	var raced atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "copy" && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":history:") && raced.CompareAndSwap(false, true) {
				if err := c.Echo(ctx, "/file", []byte("concurrent")); err != nil {
					return err
				}
			}
		}
		return next(ctx, cmds)
	}})
	if err := s.ForkWorkspace(ctx, meta.ID, "concurrent-fork", "saved"); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetWorkspace(ctx, "concurrent-fork")
	if err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, fork.ID, "/file")
	if !raced.Load() || len(page.Versions) != 3 {
		t.Fatalf("concurrent snapshot history = %+v, race %v", page, raced.Load())
	}
	_, body, err := filehistory.Get(ctx, rdb, fork.ID, "/file", "2", "")
	if err != nil || string(body) != "concurrent" {
		t.Fatalf("concurrent version = %q, %v", body, err)
	}
}

func TestHistoryRestoreQuotaFailureLeavesLiveTreeAndHistoryIntact(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "quota-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("before")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := filehistory.SetPolicy(ctx, rdb, meta.ID, filehistory.Policy{Mode: filehistory.ModeAll, MaxBytes: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err == nil {
		t.Fatal("quota did not stop restore")
	}
	body, err := c.Cat(ctx, "/file")
	if err != nil || string(body) != "after" {
		t.Fatalf("failed restore changed live body: %q, %v", body, err)
	}
	if page := historyPage(t, rdb, meta.ID, "/file"); len(page.Versions) != 2 {
		t.Fatalf("failed restore appended history: %+v", page)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryForkPreservesDescendantLineageAfterDirectoryRename(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "directory-history")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/old/file", []byte("body")); err != nil {
		t.Fatal(err)
	}
	initial := historyPage(t, rdb, meta.ID, "/old/file")
	if err := c.Rename(ctx, "/old", "/new", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForkWorkspace(ctx, meta.ID, "directory-fork", "renamed"); err != nil {
		t.Fatal(err)
	}
	fork, err := s.GetWorkspace(ctx, "directory-fork")
	if err != nil {
		t.Fatal(err)
	}
	page := historyPage(t, rdb, fork.ID, "/new/file")
	if page.FileID != initial.FileID || len(page.Versions) != len(initial.Versions) {
		t.Fatalf("directory rename split fork lineage: %+v; original %+v", page, initial)
	}
	if err := afsclient.New(rdb, fork.ID).Echo(ctx, "/new/file", []byte("edited")); err != nil {
		t.Fatal(err)
	}
	if page := historyPage(t, rdb, fork.ID, "/new/file"); page.FileID != initial.FileID || len(page.Versions) != 2 {
		t.Fatalf("new directory edit split fork lineage: %+v", page)
	}
}
