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

func TestCheckpointRestoreActivitySurvivesInterruptedMaterializationWithoutHistory(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "restore-activity")
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("before restore")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/removed", []byte("removed body")); err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "hset" && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":inode:") && failed.CompareAndSwap(false, true) {
				return errors.New("stop materialization")
			}
		}
		return next(ctx, cmds)
	}})
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err == nil {
		t.Fatal("failure not injected")
	}
	page, err := s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if entry.Source == "checkpoint_restore" {
			t.Fatalf("failed restore published accepted activity: %+v", entry)
		}
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	page, err = s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	changed := map[string]FileHistoryChange{}
	for _, entry := range page.Entries {
		if entry.Source == "checkpoint_restore" {
			if _, ok := changed[entry.Path]; ok {
				t.Fatalf("duplicate restore activity %+v", entry)
			}
			changed[entry.Path] = entry
		}
	}
	if len(changed) != 2 || changed["/file"].Op != "put" || changed["/removed"].Op != "delete" || changed["/file"].CheckpointID != "saved" || changed["/file"].PrevHash == "" || changed["/file"].ContentHash == "" {
		t.Fatalf("restore activity %+v", changed)
	}
	if got := rdb.HLen(ctx, filehistory.Prefix(meta.ID)+"records").Val(); got != 0 {
		t.Fatalf("disabled history captured %d versions", got)
	}
	if exists := rdb.Exists(ctx, restoreActivityBeforeKey(meta.ID)).Val(); exists != 0 {
		t.Fatal("completed activity retained restore staging")
	}
	if body, err := c.Cat(ctx, "/file"); err != nil || string(body) != "checkpoint" {
		t.Fatalf("restored bytes %q: %v", body, err)
	}
}

func TestCheckpointRestoreActivityReferencesCapturedVersions(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "restore-activity-versions")
	if err != nil {
		t.Fatal(err)
	}
	enableWorkspaceHistory(t, rdb, meta.ID)
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{Path: "/file", NewestFirst: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].VersionID == "" || page.Entries[0].FileID == "" {
		t.Fatalf("missing activity version association %+v", page)
	}
	version, err := filehistory.RecordByID(ctx, rdb, meta.ID, page.Entries[0].VersionID)
	if err != nil || version.Operation != "checkpoint_restore" || version.Source != "checkpoint_restore" {
		t.Fatalf("wrong referenced version %+v: %v", version, err)
	}
}

func TestResumePreActivityRestoreDoesNotRequireNewBaseline(t *testing.T) {
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "legacy-restore")
	if err != nil {
		t.Fatal(err)
	}
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("saved")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, WorkspaceGenerationKey(meta.ID), "restoring:g_old_binary", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if body, err := c.Cat(ctx, "/file"); err != nil || string(body) != "saved" {
		t.Fatalf("old restore recovery %q: %v", body, err)
	}
	page, err := s.GetFileHistoryChanges(ctx, meta.ID, FileHistoryChangesRequest{NewestFirst: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Op != "root-replace" {
		t.Fatalf("missing truthful workspace activity: %+v", page)
	}
}

func interruptedActivityRestore(t *testing.T, capture bool) (*Service, *redis.Client, string, string) {
	t.Helper()
	s, rdb := serviceFixture(t)
	ctx := context.Background()
	meta, err := s.CreateWorkspace(ctx, "interrupted-activity")
	if err != nil {
		t.Fatal(err)
	}
	if capture {
		enableWorkspaceHistory(t, rdb, meta.ID)
	}
	c := afsclient.New(rdb, meta.ID)
	if err := c.Echo(ctx, "/file", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheckpointFromLive(ctx, meta.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("original before restore")); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/removed", []byte("removed")); err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	rdb.AddHook(faultHook{pipeline: func(ctx context.Context, cmds []redis.Cmder, next redis.ProcessPipelineHook) error {
		for _, cmd := range cmds {
			if cmd.Name() == "hset" && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":inode:") && failed.CompareAndSwap(false, true) {
				return errors.New("stop restore materialization")
			}
		}
		return next(ctx, cmds)
	}})
	if _, err := s.RestoreCheckpoint(ctx, meta.ID, "saved"); err == nil {
		t.Fatal("materialization failure not injected")
	}
	baseline, err := rdb.Get(ctx, restoreActivityBeforeKey(meta.ID)).Result()
	if err != nil || baseline == "" {
		t.Fatalf("restore baseline=%q: %v", baseline, err)
	}
	return s, rdb, meta.ID, baseline
}

func TestCheckpointRestoreRetryPreparationPreservesBaseline(t *testing.T) {
	s, rdb, id, baseline := interruptedActivityRestore(t, false)
	ctx := context.Background()
	var failed atomic.Bool
	rdb.AddHook(faultHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
		if cmd.Name() == "get" && fmt.Sprint(cmd.Args()[1]) == restoreActivityBeforeKey(id) && failed.CompareAndSwap(false, true) {
			return errors.New("stop recovery preparation")
		}
		return next(ctx, cmd)
	}})
	if _, err := s.RestoreCheckpoint(ctx, id, "saved"); err == nil {
		t.Fatal("recovery preparation failure not injected")
	}
	if got := rdb.Get(ctx, WorkspaceGenerationKey(id)).Val(); !strings.HasPrefix(got, "restoring:") {
		t.Fatalf("interrupted recovery lost restoring state: %q", got)
	}
	if got := rdb.Get(ctx, restoreActivityBeforeKey(id)).Val(); got != baseline {
		t.Fatalf("original baseline changed: %q -> %q", baseline, got)
	}
	result, err := s.RestoreCheckpoint(ctx, id, "saved")
	if err != nil {
		t.Fatal(err)
	}
	if result.SafetyCheckpointCreated {
		t.Fatal("recovery captured a partially materialized safety checkpoint")
	}
	page, err := s.GetFileHistoryChanges(ctx, id, FileHistoryChangesRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.store.GetManifest(ctx, id, baseline)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := activityEntryHash(before.Entries["/file"])
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]FileHistoryChange{}
	for _, change := range page.Entries {
		if change.Source == "checkpoint_restore" {
			changes[change.Path] = change
		}
	}
	if len(changes) != 2 || changes["/file"].PrevHash != wantHash || changes["/removed"].Op != "delete" {
		t.Fatalf("recovery activity lost original preimage: %+v", changes)
	}
}

func TestCheckpointRestoreRecoveryActivityKeepsVersionLinks(t *testing.T) {
	s, rdb, id, _ := interruptedActivityRestore(t, true)
	ctx := context.Background()
	captured := map[string]string{}
	for _, path := range []string{"/file", "/removed"} {
		page, err := filehistory.List(ctx, rdb, id, path, 1, 0, "")
		if err != nil || len(page.Versions) != 1 {
			t.Fatalf("captured restore version for %s: %+v %v", path, page, err)
		}
		captured[path] = page.Versions[0].ID
	}
	if _, err := s.RestoreCheckpoint(ctx, id, "saved"); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetFileHistoryChanges(ctx, id, FileHistoryChangesRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, change := range page.Entries {
		if change.Source == "checkpoint_restore" {
			if change.VersionID != captured[change.Path] || change.FileID == "" {
				t.Fatalf("recovery activity lost captured version link: %+v, captured=%v", change, captured)
			}
			linked++
		}
	}
	if linked != 2 {
		t.Fatalf("linked %d restore activities", linked)
	}
}

func TestCheckpointRestoreProtectsRecoveryCheckpoint(t *testing.T) {
	s, rdb, id, baseline := interruptedActivityRestore(t, false)
	ctx := context.Background()
	if err := s.DeleteCheckpoint(ctx, id, baseline); err == nil || !strings.Contains(err.Error(), "restore baseline") {
		t.Fatalf("deleting interrupted restore baseline: %v", err)
	}
	if _, err := s.RestoreCheckpoint(ctx, id, "saved"); err != nil {
		t.Fatal(err)
	}
	if rdb.Exists(ctx, restoreActivityBeforeKey(id)).Val() != 0 {
		t.Fatal("completed restore retained baseline protection")
	}
	if err := s.DeleteCheckpoint(ctx, id, baseline); err != nil {
		t.Fatalf("completed restore baseline remains protected: %v", err)
	}
}
