package controlplane

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

func TestHistoryRestoreWhileDisabledRetainsRenamedLineage(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "review-disabled")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/before", []byte("initial")); err != nil {
		t.Fatal(err)
	}
	initial := historyPage(t, rdb, meta.ID, "/before")
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "before"); err != nil {
		t.Fatal(err)
	}
	if err := c.Mv(ctx, "/before", "/after"); err != nil {
		t.Fatal(err)
	}
	if err := filehistory.SetPolicy(ctx, rdb, meta.ID, filehistory.Policy{Mode: filehistory.ModeOff}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "before"); err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c = afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/before", []byte("edited")); err != nil {
		t.Fatal(err)
	}
	current := historyPage(t, rdb, meta.ID, "/before")
	if current.FileID != initial.FileID {
		t.Fatalf("restoring while disabled broke rename lineage: initial %s, current %+v", initial.FileID, current)
	}
}

func TestHistoryForkRejectsReusedSourceName(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "review-reused")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/original", []byte("original checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	peer := redis.NewClient(rdb.Options())
	defer peer.Close()
	other := NewService(NewStore(peer))
	var raced atomic.Bool
	rdb.AddHook(faultHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
		err := next(ctx, cmd)
		if err == nil && cmd.Name() == "get" && fmt.Sprint(cmd.Args()[1]) == savepointManifestKey(meta.ID, "saved") && raced.CompareAndSwap(false, true) {
			if err := other.DeleteWorkspace(ctx, "review-reused"); err != nil {
				return err
			}
			replacement, err := other.CreateWorkspace(ctx, "review-reused")
			if err != nil {
				return err
			}
			if err := filehistory.SetPolicy(ctx, peer, replacement.ID, filehistory.Policy{Mode: filehistory.ModeAll}); err != nil {
				return err
			}
			return afsclient.New(peer, replacement.ID).Echo(ctx, "/replacement", []byte("replacement history"))
		}
		return err
	}})
	if err := s.ForkWorkspace(ctx, "review-reused", "wrong-source-fork", "saved"); err == nil {
		t.Fatal("fork combined old checkpoint with replacement workspace history")
	}
	if !raced.Load() {
		t.Fatal("name replacement race did not execute")
	}
	if _, err := s.GetWorkspace(ctx, "wrong-source-fork"); err == nil {
		t.Fatal("mixed-source fork became visible")
	}
}

func TestHistoryForkRejectsMissingHistoricalBody(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "review-missing")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("first")); err != nil {
		t.Fatal(err)
	}
	first := historyPage(t, rdb, meta.ID, "/file").Versions[0]
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Del(ctx, filehistory.BodyKey(meta.ID, first)).Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.ForkWorkspace(ctx, meta.ID, "review-invalid-fork", "first"); err == nil {
		t.Fatal("fork accepted history referencing a missing body")
	}
	if _, err := s.GetWorkspace(ctx, "review-invalid-fork"); err == nil {
		t.Fatal("invalid history fork became visible")
	}
}
