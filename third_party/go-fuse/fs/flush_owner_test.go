package fs_test

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type flushBridgeRoot struct {
	fs.Inode
	handle fs.FileHandle
}

func (n *flushBridgeRoot) Open(context.Context, uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return n.handle, 0, 0
}

type flushBridgeLegacyFile struct{ calls int }

func (f *flushBridgeLegacyFile) Flush(context.Context) syscall.Errno {
	f.calls++
	return syscall.EIO
}

type flushBridgeOwnerFile struct {
	flushBridgeLegacyFile
	owner uint64
}

func (f *flushBridgeOwnerFile) FlushWithOwner(_ context.Context, owner uint64) syscall.Errno {
	f.owner = owner
	return syscall.EPERM
}

type flushBridgeLegacyNode struct {
	flushBridgeRoot
	calls int
}

func (n *flushBridgeLegacyNode) Flush(context.Context, fs.FileHandle) syscall.Errno {
	n.calls++
	return syscall.EIO
}

type flushBridgeOwnerNode struct {
	flushBridgeLegacyNode
	owner uint64
}

func (n *flushBridgeOwnerNode) FlushWithOwner(_ context.Context, _ fs.FileHandle, owner uint64) syscall.Errno {
	n.owner = owner
	return syscall.EPERM
}

func TestFlushOwnerBridge(t *testing.T) {
	const owner = uint64(0x123456789abcdef0)
	ownerNode := &flushBridgeOwnerNode{}
	ownerFile := &flushBridgeOwnerFile{}
	legacyNode := &flushBridgeLegacyNode{}
	legacyFile := &flushBridgeLegacyFile{}
	tests := []struct {
		name  string
		root  fs.InodeEmbedder
		want  syscall.Errno
		check func() bool
	}{
		{"node_owner", ownerNode, syscall.EPERM, func() bool { return ownerNode.owner == owner && ownerNode.calls == 0 }},
		{"file_owner", &flushBridgeRoot{handle: ownerFile}, syscall.EPERM, func() bool { return ownerFile.owner == owner && ownerFile.calls == 0 }},
		{"legacy_node", legacyNode, syscall.EIO, func() bool { return legacyNode.calls == 1 }},
		{"legacy_file", &flushBridgeRoot{handle: legacyFile}, syscall.EIO, func() bool { return legacyFile.calls == 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := fs.NewNodeFS(test.root, &fs.Options{})
			header := fuse.InHeader{NodeId: 1}
			out := fuse.OpenOut{}
			if status := raw.Open(nil, &fuse.OpenIn{InHeader: header}, &out); status != fuse.OK {
				t.Fatal(status)
			}
			status := raw.Flush(nil, &fuse.FlushIn{InHeader: header, Fh: out.Fh, LockOwner: owner})
			if status != fuse.Status(test.want) || !test.check() {
				t.Fatalf("Flush owner forwarding/fallback failed: status=%v want=%v", status, test.want)
			}
		})
	}
}
