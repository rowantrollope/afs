package afsfs

import (
	"context"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/cache"
	"github.com/rowantrollope/afs/mount/internal/client"
)

// Drive the raw FUSE bridge, including the close owner's token. No kernel
// mount is needed to reproduce either a second-FD close or an inherited FD
// whose different processes hold disjoint record locks.
func TestNativeFUSEFlushUsesClosingOwner(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlocked_second_descriptor", true: "shared_descriptor_other_owner"}[shared], func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			c := nativeTestClient(t, rdb, "fuse-close-owner")
			if err := c.Echo(ctx, "/file", []byte("contents")); err != nil {
				t.Fatal(err)
			}
			r := &FSRoot{FSNode: FSNode{client: c, attrCache: cache.New(time.Hour), dirCache: cache.New(time.Hour), opts: &Options{AttrTimeout: time.Hour}, fsPath: "/"}}
			raw := fs.NewNodeFS(r, &fs.Options{})
			entry := fuse.EntryOut{}
			if status := raw.Lookup(nil, &fuse.InHeader{NodeId: 1}, "file", &entry); status != fuse.OK {
				t.Fatal(status)
			}
			header := fuse.InHeader{NodeId: entry.NodeId}
			open := func() uint64 {
				out := fuse.OpenOut{}
				if status := raw.Open(nil, &fuse.OpenIn{InHeader: header, Flags: syscall.O_RDWR}, &out); status != fuse.OK {
					t.Fatal(status)
				}
				return out.Fh
			}
			a := open()
			closing := a
			if !shared {
				closing = open()
			}
			lock := func(fh, owner, start, end uint64) {
				if status := raw.SetLk(nil, &fuse.LkIn{InHeader: header, Fh: fh, Owner: owner, Lk: fuse.FileLock{Start: start, End: end, Typ: syscall.F_WRLCK}}); status != fuse.OK {
					t.Fatal(status)
				}
			}
			lock(a, 42, 0, 9)
			if shared {
				lock(a, 43, 10, 19)
			}
			if status := raw.Flush(nil, &fuse.FlushIn{InHeader: header, Fh: closing, LockOwner: 42}); status != fuse.OK {
				t.Fatal(status)
			}
			query := func(start, end uint64, want uint32) {
				out := fuse.LkOut{}
				if status := raw.GetLk(nil, &fuse.LkIn{InHeader: header, Fh: a, Owner: 44, Lk: fuse.FileLock{Start: start, End: end, Typ: syscall.F_WRLCK}}, &out); status != fuse.OK {
					t.Fatal(status)
				}
				if out.Lk.Typ != want {
					t.Fatalf("lock [%d,%d] after owner42 close: type=%d want=%d", start, end, out.Lk.Typ, want)
				}
			}
			query(0, 9, syscall.F_UNLCK)
			if shared {
				query(10, 19, syscall.F_WRLCK)
			}
		})
	}
}

type lockWaitStarted struct {
	ready chan struct{}
	once  sync.Once
}

func (h *lockWaitStarted) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *lockWaitStarted) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *lockWaitStarted) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "hgetall" && strings.Contains(cmd.Args()[1].(string), ":locks:") {
			h.once.Do(func() { close(h.ready) })
		}
		return err
	}
}

func TestNativeFUSEClosingOwnerReleasesBlockedWaiter(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-close-waiter")
	if err := c.Echo(ctx, "/file", []byte("contents")); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	lock := &client.FileLock{Start: 0, End: 99, Type: syscall.F_WRLCK}
	if err := c.Setlk(ctx, st.Inode, posixLockOwner(42), lock, false); err != nil {
		t.Fatal(err)
	}
	waiting := &lockWaitStarted{ready: make(chan struct{})}
	rdb.AddHook(waiting)
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.Setlk(waitCtx, st.Inode, posixLockOwner(43), lock, true) }()
	select {
	case <-waiting.ready:
	case <-waitCtx.Done():
		t.Fatal("lock waiter never entered the native request")
	}
	closeCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	closing := newFileHandle("/file", st.Inode, c, &FSNode{})
	if errno := closing.FlushWithOwner(closeCtx, 42); errno != 0 {
		t.Fatalf("close could not release the lock blocking its own barrier: %v", errno)
	}
	if err := <-result; err != nil {
		t.Fatalf("blocked waiter was not released: %v", err)
	}
}

func TestNativeFUSEReleaseFlockWithBlockedWaiter(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-flock-waiter")
	if err := c.Echo(ctx, "/file", []byte("contents")); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	a := newFileHandle("/file", st.Inode, c, &FSNode{})
	b := newFileHandle("/file", st.Inode, c, &FSNode{})
	lock := &fuse.FileLock{Start: 0, End: 99, Typ: syscall.F_WRLCK}
	if errno := a.Setlk(ctx, 0, lock, fuse.FUSE_LK_FLOCK); errno != 0 {
		t.Fatal(errno)
	}
	waiting := &lockWaitStarted{ready: make(chan struct{})}
	rdb.AddHook(waiting)
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := make(chan syscall.Errno, 1)
	go func() { result <- b.Setlkw(waitCtx, 0, lock, fuse.FUSE_LK_FLOCK) }()
	select {
	case <-waiting.ready:
	case <-waitCtx.Done():
		t.Fatal("flock waiter never attempted lock")
	}
	closeCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if errno := a.FlushWithOwner(closeCtx, 42); errno != 0 {
		t.Fatalf("close blocked before flock release: %v", errno)
	}
	if errno := a.Release(closeCtx); errno != 0 {
		t.Fatalf("last-descriptor release blocked: %v", errno)
	}
	if errno := <-result; errno != 0 {
		t.Fatalf("flock waiter: %v", errno)
	}
}
