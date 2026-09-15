package afsfs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Create implements fs.NodeCreater.
func (n *FSNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	if n.opts.ReadOnly {
		return nil, nil, 0, syscall.EROFS
	}

	child, ctx, err := n.childContext(ctx, name)
	if err != nil {
		return nil, nil, 0, mapError(err)
	}
	exclusive := flags&syscall.O_EXCL != 0

	st, _, err := n.client.CreateFile(ctx, child.fsPath, mode&07777, exclusive)
	if err != nil {
		return nil, nil, 0, mapError(err)
	}

	n.root().invalidatePath(child.fsPath)
	attr := statToAttr(st)
	out.Attr = attr
	out.SetEntryTimeout(n.opts.AttrTimeout)
	out.SetAttrTimeout(n.opts.AttrTimeout)

	node := n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: st.Inode})

	handle := newFileHandle(child.fsPath, st.Inode, n.client, child)
	handle.appendMode = flags&syscall.O_APPEND != 0
	if flags&syscall.O_TRUNC != 0 {
		// *AtPath updates the path cache in place; the no-path variant flushes
		// the whole cache.
		if err := n.client.TruncateInodeAtPath(ctx, st.Inode, child.fsPath, 0); err != nil {
			return nil, nil, 0, mapError(err)
		}
		n.root().invalidatePath(child.fsPath)
	}

	return node, handle, 0, 0
}

// Open implements fs.NodeOpener.
func (n *FSNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.opts.ReadOnly && (flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT)) != 0 {
		return nil, 0, syscall.EROFS
	}

	st, err := n.stat(ctx)
	if err != nil {
		return nil, 0, mapError(err)
	}
	if st == nil {
		return nil, 0, syscall.ENOENT
	}

	handle := newFileHandle(n.currentPath(), st.Inode, n.client, n)
	handle.appendMode = flags&syscall.O_APPEND != 0

	if flags&syscall.O_TRUNC != 0 {
		// *AtPath updates the path cache in place; the no-path variant flushes
		// the whole cache.
		if err := n.client.TruncateInodeAtPath(ctx, st.Inode, n.currentPath(), 0); err != nil {
			return nil, 0, mapError(err)
		}
		n.root().invalidatePath(n.currentPath())
	}

	return handle, 0, 0
}

// Read implements fs.NodeReader.
func (n *FSNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if h, ok := fh.(*FileHandle); ok {
		return h.Read(ctx, dest, off)
	}

	if off < 0 {
		return nil, syscall.EINVAL
	}
	if inode := n.StableAttr().Ino; inode != 0 {
		data, err := n.client.ReadInodeAt(ctx, inode, off, len(dest))
		if err != nil {
			return nil, mapError(err)
		}
		return fuse.ReadResultData(data), 0
	}
	// Fallback: direct read without handle.
	data, err := n.client.Cat(ctx, n.currentPath())
	if err != nil {
		return nil, mapError(err)
	}

	size := int64(len(data))
	if off >= size {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > size {
		end = size
	}
	return fuse.ReadResultData(data[off:end]), 0
}

// Write implements fs.NodeWriter.
func (n *FSNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if n.opts.ReadOnly {
		return 0, syscall.EROFS
	}

	if h, ok := fh.(*FileHandle); ok {
		return h.Write(ctx, data, off)
	}

	return 0, syscall.EIO
}

// Fsync implements fs.NodeFsyncer.
func (n *FSNode) Fsync(ctx context.Context, fh fs.FileHandle, flags uint32) syscall.Errno {
	if h, ok := fh.(*FileHandle); ok {
		return h.Sync(ctx)
	}
	return mapError(n.client.Barrier(ctx))
}

// Flush implements fs.NodeFlusher.
func (n *FSNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	if h, ok := fh.(*FileHandle); ok {
		return h.Flush(ctx)
	}
	return 0
}

func (n *FSNode) FlushWithOwner(ctx context.Context, fh fs.FileHandle, owner uint64) syscall.Errno {
	if h, ok := fh.(*FileHandle); ok {
		return h.FlushWithOwner(ctx, owner)
	}
	return 0
}

// Release implements fs.NodeReleaser.
func (n *FSNode) Release(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	if h, ok := fh.(*FileHandle); ok {
		return h.Release(ctx)
	}
	return 0
}

// Unlink implements fs.NodeUnlinker.
func (n *FSNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.opts.ReadOnly {
		return syscall.EROFS
	}

	child, ctx, err := n.childContext(ctx, name)
	if err != nil {
		return mapError(err)
	}
	if err := n.client.Rm(ctx, child.fsPath); err != nil {
		return mapError(err)
	}

	n.root().invalidatePath(child.fsPath)
	return 0
}

// Link implements fs.NodeLinker — returns ENOTSUP (no hard links in Agent Filesystem).
func (n *FSNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.ENOTSUP
}

// Getxattr implements fs.NodeGetxattrer — returns ENODATA (no xattr support).
func (n *FSNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	return 0, syscall.ENODATA
}

// Setxattr implements fs.NodeSetxattrer — returns ENOTSUP.
func (n *FSNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	return syscall.ENOTSUP
}

// Listxattr implements fs.NodeListxattrer — returns empty.
func (n *FSNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	return 0, 0
}

// Ensure interfaces are satisfied.
var _ fs.NodeCreater = (*FSNode)(nil)
var _ fs.NodeOpener = (*FSNode)(nil)
var _ fs.NodeReader = (*FSNode)(nil)
var _ fs.NodeWriter = (*FSNode)(nil)
var _ fs.NodeFsyncer = (*FSNode)(nil)
var _ fs.NodeFlusher = (*FSNode)(nil)
var _ fs.NodeFlusherWithOwner = (*FSNode)(nil)
var _ fs.NodeReleaser = (*FSNode)(nil)
var _ fs.NodeUnlinker = (*FSNode)(nil)
var _ fs.NodeLinker = (*FSNode)(nil)
var _ fs.NodeGetxattrer = (*FSNode)(nil)
var _ fs.NodeSetxattrer = (*FSNode)(nil)
var _ fs.NodeListxattrer = (*FSNode)(nil)
