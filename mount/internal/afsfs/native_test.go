package afsfs

import (
	"context"
	"fmt"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rowantrollope/afs/mount/internal/cache"
	"github.com/rowantrollope/afs/mount/internal/client"
	"syscall"
	"testing"
	"time"
)

func TestNativeFUSEMkdirRequestedMode(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-mkdir-mode")
	r := &FSRoot{FSNode: FSNode{client: c, attrCache: cache.New(time.Hour), dirCache: cache.New(time.Hour), opts: &Options{AttrTimeout: time.Hour}, fsPath: "/"}}
	raw := fs.NewNodeFS(r, mountOptions(r.opts))
	for _, mode := range []uint32{0, 0o750} {
		name := fmt.Sprintf("mode-%o", mode)
		out := &fuse.EntryOut{}
		if status := raw.Mkdir(nil, &fuse.MkdirIn{InHeader: fuse.InHeader{NodeId: 1}, Mode: mode}, name, out); status != 0 {
			t.Fatal(status)
		}
		st, err := c.Stat(ctx, "/"+name)
		if err != nil || st == nil || st.Mode != mode || out.Mode&0o7777 != mode {
			t.Fatalf("mkdir mode=%o: stored=%+v reply=%o error=%v", mode, st, out.Mode, err)
		}
	}
}

func nativeRoot(c client.NativeClient) *FSRoot {
	r := &FSRoot{FSNode: FSNode{client: c, attrCache: cache.New(time.Hour), dirCache: cache.New(time.Hour), opts: &Options{AttrTimeout: time.Hour}, fsPath: "/"}}
	fs.NewNodeFS(r, &fs.Options{})
	return r
}
func TestNativeFUSECachedNodeKeepsInodeAcrossRename(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-inode")
	r := nativeRoot(c)
	if err := c.Echo(ctx, "/before", []byte("old")); err != nil {
		t.Fatal(err)
	}
	inode, eno := r.Lookup(ctx, "before", &fuse.EntryOut{})
	if eno != 0 {
		t.Fatal(eno)
	}
	r.AddChild("before", inode, true)
	node := inode.Operations().(*FSNode)
	if err := c.Rename(ctx, "/before", "/after", 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/before", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	handle, _, eno := node.Open(ctx, syscall.O_RDWR)
	if eno != 0 {
		t.Fatal(eno)
	}
	if _, eno := node.Write(ctx, handle, []byte("NEW"), 0); eno != 0 {
		t.Fatal(eno)
	}
	got, err := c.Cat(ctx, "/after")
	if err != nil || string(got) != "NEW" {
		t.Fatalf("renamed inode=%q,%v", got, err)
	}
	got, err = c.Cat(ctx, "/before")
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement inode=%q,%v", got, err)
	}
	if err := c.Rm(ctx, "/after"); err != nil {
		t.Fatal(err)
	}
	if _, eno := node.Write(ctx, handle, []byte("bad"), 0); eno == 0 {
		t.Fatal("unlinked inode accepted write")
	}
}
func TestNativeFUSECacheHonorsGeneration(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-fence")
	r := nativeRoot(c)
	if err := c.Echo(ctx, "/file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	inode, eno := r.Lookup(ctx, "file", &fuse.EntryOut{})
	if eno != 0 {
		t.Fatal(eno)
	}
	r.AddChild("file", inode, true)
	stream, eno := r.Readdir(ctx)
	if eno != 0 {
		t.Fatal(eno)
	}
	stream.Close()
	if err := rdb.Set(ctx, "afs-lite:{fuse-fence}:generation", "new-generation", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, eno := r.Lookup(ctx, "file", &fuse.EntryOut{}); eno != syscall.ESTALE {
		t.Fatalf("cached lookup=%v", eno)
	}
	if _, eno := r.Readdir(ctx); eno != syscall.ESTALE {
		t.Fatalf("cached readdir=%v", eno)
	}
	if eno := inode.Operations().(*FSNode).Getattr(ctx, nil, &fuse.AttrOut{}); eno != syscall.ESTALE {
		t.Fatalf("cached getattr=%v", eno)
	}
}
func TestNativeFUSEPosixOwnerSharedAcrossDescriptors(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-posix")
	if err := c.Echo(ctx, "/file", []byte("old")); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	a := newFileHandle("/file", st.Inode, c, &FSNode{})
	b := newFileHandle("/file", st.Inode, c, &FSNode{})
	lock := &fuse.FileLock{Start: 0, End: 99, Typ: syscall.F_WRLCK}
	if eno := a.Setlk(ctx, 42, lock, 0); eno != 0 {
		t.Fatal(eno)
	}
	if eno := b.Setlk(ctx, 42, lock, 0); eno != 0 {
		t.Fatalf("owner conflicts with itself across FDs: %v", eno)
	}
	if eno := b.Setlk(ctx, 43, lock, 0); eno != syscall.EAGAIN {
		t.Fatalf("other owner=%v", eno)
	}
	if eno := a.Sync(context.Background()); eno != 0 {
		t.Fatal(eno)
	}
	if eno := b.Setlk(ctx, 43, lock, 0); eno != syscall.EAGAIN {
		t.Fatal("fsync released POSIX locks")
	}
	if eno := a.FlushWithOwner(ctx, 42); eno != 0 {
		t.Fatal(eno)
	}
	if eno := b.Setlk(ctx, 43, lock, 0); eno != 0 {
		t.Fatalf("close did not release process locks: %v", eno)
	}
}
