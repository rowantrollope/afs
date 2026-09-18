package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func TestRestoreVersionAtomicReplacementAndUndelete(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "restore-version"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/file", []byte("original\x00bytes")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkspaceGeneration(ctx, "g_test")
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: filehistory.ModeAll}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/file", []byte("new")); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/file")
	selected, data, err := filehistory.Get(ctx, rdb, id, "/file", versions[len(versions)-1].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, data, stat, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.FileID != selected.FileID || restored.Operation != "restore" || restored.ID == selected.ID {
		t.Fatalf("restore=%+v selected=%+v", restored, selected)
	}
	got, err := c.Cat(ctx, "/file")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("body=%q err=%v", got, err)
	}
	if _, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, data, stat, false); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	if err := c.Rm(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	revived, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, data, nil, true)
	if err != nil || revived.FileID != selected.FileID || revived.Operation != "undelete" {
		t.Fatalf("undelete=%+v: %v", revived, err)
	}
	if _, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, data, nil, true); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("second undelete: %v", err)
	}
}

type restorePublicationHook struct {
	fired   atomic.Bool
	before  func()
	lostAck bool
}

func (h *restorePublicationHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *restorePublicationHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *restorePublicationHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() != "eval" || len(cmd.Args()) < 2 || !strings.Contains(fmt.Sprint(cmd.Args()[1]), "HISTORY_RESTORE_STAGE_MISSING") || !h.fired.CompareAndSwap(false, true) {
			return next(ctx, cmd)
		}
		if h.before != nil {
			h.before()
		}
		err := next(ctx, cmd)
		if err == nil && h.lostAck {
			return errors.New("injected restore acknowledgment loss")
		}
		return err
	}
}

func TestRestoreVersionConcurrentPublicationAndLostAcknowledgment(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(fmt.Sprint(lostAck), func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			id := "restore-race"
			c := New(rdb, id)
			if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: filehistory.ModeAll}); err != nil {
				t.Fatal(err)
			}
			if err := c.Echo(ctx, "/file", []byte("first")); err != nil {
				t.Fatal(err)
			}
			selected, body, err := filehistory.Get(ctx, rdb, id, "/file", "latest", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Echo(ctx, "/file", []byte("second")); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
				t.Fatal(err)
			}
			ctx = WithWorkspaceGeneration(ctx, "g_test")
			stat, err := c.Stat(ctx, "/file")
			if err != nil {
				t.Fatal(err)
			}
			hook := &restorePublicationHook{lostAck: lostAck}
			if !lostAck {
				hook.before = func() {
					if err := New(rdb, id).Echo(ctx, "/file", []byte("peer")); err != nil {
						t.Fatal(err)
					}
				}
			}
			rdb.AddHook(hook)
			record, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, body, stat, false)
			if !hook.fired.Load() {
				t.Fatal("publication fault did not execute")
			}
			if lostAck && (err != nil || record.Operation != "restore") {
				t.Fatalf("ack recovery %+v: %v", record, err)
			}
			if !lostAck && !errors.Is(err, ErrWriteConflict) {
				t.Fatalf("concurrent write not rejected: %v", err)
			}
			want := "peer"
			if lostAck {
				want = "first"
			}
			got, err := c.Cat(ctx, "/file")
			if err != nil || string(got) != want {
				t.Fatalf("live=%q want=%q: %v", got, want, err)
			}
			versions := historyRecords(t, ctx, rdb, id, "/file")
			if len(versions) != 3 {
				t.Fatalf("failed/duplicated history publication: %+v", versions)
			}
		})
	}
}

func TestRestoreVersionTypeChangesAndDisabledPolicy(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "restore-types"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/file", []byte("file body")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkspaceGeneration(ctx, "g_test")
	stat, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	symlink := filehistory.Record{Type: "symlink", Mode: 0777, Size: 6, Target: "target", Path: "/file"}
	link, err := RestoreFileVersion(ctx, rdb, id, "/file", symlink, nil, stat, false)
	if err != nil {
		t.Fatal(err)
	}
	if link.Type != "symlink" {
		t.Fatalf("link %+v", link)
	}
	linkStat, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	if linkStat.Type != "symlink" || linkStat.Inode == stat.Inode {
		t.Fatalf("type replacement stat=%+v prior=%+v", linkStat, stat)
	}
	selected := filehistory.Record{Type: "file", Mode: 0600, Size: 4, Path: "/file"}
	record, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, []byte("data"), linkStat, false)
	if err != nil {
		t.Fatal(err)
	}
	if record.FileID != link.FileID {
		t.Fatalf("type change lost lineage %+v %+v", link, record)
	}
	current, err := c.Stat(ctx, "/file")
	if err != nil || current.Mode != 0600 {
		t.Fatalf("mode %+v: %v", current, err)
	}
	if _, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, []byte("data"), current, false); err != nil {
		t.Fatal(err)
	}
	versions := historyRecords(t, ctx, rdb, id, "/file")
	if len(versions) != 4 {
		t.Fatalf("explicit equivalent restore or baseline missing: %+v", versions)
	}
}

func TestRestoreVersionFailurePreservesLiveFile(t *testing.T) {
	for _, failure := range []string{"wrong-records-type", "budget", "generation"} {
		t.Run(failure, func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			id := "restore-failure"
			c := New(rdb, id)
			if err := c.Echo(ctx, "/file", []byte("keep")); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
				t.Fatal(err)
			}
			ctx = WithWorkspaceGeneration(ctx, "g_test")
			stat, err := c.Stat(ctx, "/file")
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "wrong-records-type":
				if err := rdb.Set(ctx, filehistory.Prefix(id)+"records", "bad", 0).Err(); err != nil {
					t.Fatal(err)
				}
			case "budget":
				if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: filehistory.ModeAll, MaxBytes: 1}); err != nil {
					t.Fatal(err)
				}
			case "generation":
				if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_new", 0).Err(); err != nil {
					t.Fatal(err)
				}
			}
			selected := filehistory.Record{Type: "file", Mode: 0600, Size: 7, Path: "/file"}
			if _, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, []byte("replace"), stat, false); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			got, err := c.Cat(context.Background(), "/file")
			if err != nil || string(got) != "keep" {
				t.Fatalf("failed restore modified body=%q: %v", got, err)
			}
			after, err := c.Stat(context.Background(), "/file")
			if err != nil || after.Revision != stat.Revision || after.Mode != stat.Mode {
				t.Fatalf("failed restore changed metadata %+v: %v", after, err)
			}
		})
	}
}

func TestRestoreVersionStaleParentIsNotRecreated(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "restore-parent"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/dir/file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkspaceGeneration(ctx, "g_test")
	stat, err := c.Stat(ctx, "/dir/file")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Rm(ctx, "/dir/file"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rm(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	selected := filehistory.Record{Type: "file", Mode: 0600, Size: 3, Path: "/dir/file"}
	if _, err := RestoreFileVersion(ctx, rdb, id, "/dir/file", selected, []byte("old"), stat, false); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("stale parent restore: %v", err)
	}
	parent, err := c.Stat(ctx, "/dir")
	if err != nil || parent != nil {
		t.Fatalf("stale restore recreated parent %+v: %v", parent, err)
	}
}

func TestRestoreVersionChecksRootTypeBeforeMutation(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "restore-root-type"
	c := New(rdb, id)
	if err := c.Echo(ctx, "/file", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{"+id+"}:generation", "g_test", 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkspaceGeneration(ctx, "g_test")
	stat, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	hook := &restorePublicationHook{before: func() {
		if err := rdb.HSet(ctx, "afs:{"+id+"}:inode:1", "type", "file").Err(); err != nil {
			t.Fatal(err)
		}
	}}
	rdb.AddHook(hook)
	selected := filehistory.Record{Type: "file", Mode: 0600, Size: 3, Path: "/file"}
	if _, err := RestoreFileVersion(ctx, rdb, id, "/file", selected, []byte("new"), stat, false); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("invalid parent allowed: %v", err)
	}
	if !hook.fired.Load() {
		t.Fatal("parent race not injected")
	}
	if got := rdb.HGet(ctx, fmt.Sprintf("afs:{%s}:inode:%d", id, stat.Inode), "revision").Val(); got != stat.Revision {
		t.Fatalf("failed parent validation modified leaf revision %q", got)
	}
	if n := rdb.HLen(ctx, filehistory.Prefix(id)+"records").Val(); n != 0 {
		t.Fatalf("invalid parent created %d history records", n)
	}
}
