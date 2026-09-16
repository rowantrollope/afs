package afsfs

import (
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rowantrollope/afs/mount/internal/cache"
)

// go-fuse normalizes ownership after LOOKUP/GETATTR but not SETATTR. Exercise
// the raw reply a kernel caches after chmod/truncate, including open handles.
func TestFUSESetattrPreservesMountOwnership(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "fuse-owner-response")
	if err := c.Echo(ctx, "/file", []byte("contents")); err != nil {
		t.Fatal(err)
	}
	r := &FSRoot{FSNode: FSNode{client: c, attrCache: cache.New(time.Hour), dirCache: cache.New(time.Hour), opts: &Options{AttrTimeout: time.Hour, UID: 1000, GID: 2000}, fsPath: "/"}}
	raw := fs.NewNodeFS(r, mountOptions(r.opts))
	rootAttr := fuse.AttrOut{}
	if status := raw.GetAttr(nil, &fuse.GetAttrIn{InHeader: fuse.InHeader{NodeId: 1}}, &rootAttr); status != fuse.OK || rootAttr.Uid != 1000 || rootAttr.Gid != 2000 {
		t.Fatalf("root getattr: status=%v owner=%d:%d", status, rootAttr.Uid, rootAttr.Gid)
	}
	rootMode := fuse.SetAttrIn{}
	rootMode.InHeader = fuse.InHeader{NodeId: 1}
	rootMode.Valid, rootMode.Mode = fuse.FATTR_MODE, 0755
	if status := raw.SetAttr(nil, &rootMode, &rootAttr); status != fuse.OK || rootAttr.Uid != 1000 || rootAttr.Gid != 2000 {
		t.Fatalf("root setattr: status=%v owner=%d:%d", status, rootAttr.Uid, rootAttr.Gid)
	}
	created := fuse.CreateOut{}
	if status := raw.Create(nil, &fuse.CreateIn{InHeader: fuse.InHeader{NodeId: 1}, Flags: syscall.O_RDWR, Mode: 0640}, "created", &created); status != fuse.OK || created.Uid != 1000 || created.Gid != 2000 {
		t.Fatalf("create: status=%v owner=%d:%d", status, created.Uid, created.Gid)
	}
	entry := fuse.EntryOut{}
	if status := raw.Lookup(nil, &fuse.InHeader{NodeId: 1}, "file", &entry); status != fuse.OK {
		t.Fatal(status)
	}
	if entry.Uid != 1000 || entry.Gid != 2000 {
		t.Fatalf("lookup owner=%d:%d", entry.Uid, entry.Gid)
	}
	header := fuse.InHeader{NodeId: entry.NodeId}
	opened := fuse.OpenOut{}
	if status := raw.Open(nil, &fuse.OpenIn{InHeader: header, Flags: syscall.O_RDWR}, &opened); status != fuse.OK {
		t.Fatal(status)
	}
	for _, withHandle := range []bool{false, true} {
		for _, valid := range []uint32{fuse.FATTR_MODE, fuse.FATTR_SIZE} {
			request := fuse.SetAttrIn{}
			request.InHeader = header
			request.Valid, request.Mode, request.Size = valid, 0640, 4
			if withHandle {
				request.Valid |= fuse.FATTR_FH
				request.Fh = opened.Fh
			}
			out := fuse.AttrOut{}
			if status := raw.SetAttr(nil, &request, &out); status != fuse.OK {
				t.Fatalf("setattr handle=%v valid=%d: %v", withHandle, valid, status)
			}
			if out.Uid != 1000 || out.Gid != 2000 {
				t.Fatalf("setattr handle=%v valid=%d owner=%d:%d, want1000:2000", withHandle, valid, out.Uid, out.Gid)
			}
		}
	}
	stored, err := c.Stat(ctx, "/file")
	if err != nil || stored.UID != 0 || stored.GID != 0 {
		t.Fatalf("presentation override changed stored ownership: %+v, %v", stored, err)
	}
	if err := c.Chown(ctx, "/file", 333, 444); err != nil {
		t.Fatal(err)
	}
	request := fuse.SetAttrIn{}
	request.InHeader, request.Valid, request.Mode = header, fuse.FATTR_MODE, 0640
	out := fuse.AttrOut{}
	if status := raw.SetAttr(nil, &request, &out); status != fuse.OK || out.Uid != 333 || out.Gid != 444 {
		t.Fatalf("explicit Redis owner replaced: status=%v owner=%d:%d", status, out.Uid, out.Gid)
	}
}
