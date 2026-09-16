// Package afsfs implements a FUSE filesystem backed by the native Redis
// workspace client.
package afsfs

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rowantrollope/afs/mount/internal/cache"
	"github.com/rowantrollope/afs/mount/internal/client"
)

// Options configures the FUSE mount.
type Options struct {
	MountContext context.Context
	AttrTimeout  time.Duration
	ReadOnly     bool
	AllowOther   bool
	Debug        bool
	UID          uint32
	GID          uint32
	// DisableCrossClientInvalidation skips starting the pub/sub subscriber
	// and tells the client to stop publishing cache invalidations. Mounts
	// then only see each other's writes after their TTL expires.
	DisableCrossClientInvalidation bool
}

// FSRoot is the root of the FUSE filesystem.
type FSRoot struct {
	FSNode
	// server is populated by Mount() after fs.Mount returns. It's needed
	// by the invalidation subscriber to issue kernel notifications. Reads
	// use atomic.Pointer because the subscriber goroutine may run before
	// the handshake between Mount and the first request completes.
	server atomic.Pointer[fuse.Server]
}

// FSNode represents a node (file, directory, or symlink) in the filesystem.
type FSNode struct {
	fs.Inode

	client    client.NativeClient
	attrCache *cache.Cache
	dirCache  *cache.Cache
	opts      *Options
	fsPath    string // absolute path in AFS mount storage (e.g. "/", "/foo/bar")
}

// currentPath follows the live inode tree after a rename. New children are not
// attached yet and use their construction path until NewInode assigns identity.
func (n *FSNode) currentPath() string {
	if n.StableAttr().Ino == 0 {
		return n.fsPath
	}
	rel := n.Path(n.Root())
	if rel == "" {
		return "/"
	}
	return "/" + rel
}

// root returns the FSRoot from any node.
func (n *FSNode) root() *FSRoot {
	return n.Root().Operations().(*FSRoot)
}

// invalidatePath invalidates caches for a path and its parent directory.
func (r *FSRoot) invalidatePath(path string) {
	r.attrCache.Invalidate(path)
	parent := filepath.Dir(path)
	r.dirCache.Invalidate(parent)
	r.attrCache.Invalidate(parent)
}

// invalidatePathPrefix invalidates caches for a subtree and its parent directory.
func (r *FSRoot) invalidatePathPrefix(path string) {
	r.attrCache.InvalidatePrefix(path)
	r.dirCache.InvalidatePrefix(path)
	r.invalidatePath(path)
}

// newChild creates a child FSNode for the given basename.
func (n *FSNode) newChild(name string) *FSNode {
	childPath := n.currentPath() + "/" + name
	if n.currentPath() == "/" {
		childPath = "/" + name
	}
	return &FSNode{
		client:    n.client,
		attrCache: n.attrCache,
		dirCache:  n.dirCache,
		opts:      n.opts,
		fsPath:    childPath,
	}
}

// Mount mounts the AFS filesystem at the given mountpoint.
//
// ctx, when non-nil, controls the lifetime of the cross-client invalidation
// subscriber: cancel it to stop the subscriber goroutine (e.g. during
// shutdown). Passing a nil ctx uses context.Background() and relies on the
// process exiting to tear the subscriber down.
func Mount(ctx context.Context, mountpoint string, c client.NativeClient, opts *Options) (*fuse.Server, error) {
	if opts.AttrTimeout == 0 {
		opts.AttrTimeout = time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}

	attrCache := cache.New(opts.AttrTimeout)
	dirCache := cache.New(opts.AttrTimeout)

	root := &FSRoot{
		FSNode: FSNode{
			client:    c,
			attrCache: attrCache,
			dirCache:  dirCache,
			opts:      opts,
			fsPath:    "/",
		},
	}

	server, err := fs.Mount(mountpoint, root, mountOptions(opts))
	if err != nil {
		return server, err
	}
	root.server.Store(server)

	if opts.DisableCrossClientInvalidation {
		c.DisableInvalidationPublishing()
	} else {
		if err := c.SubscribeInvalidationsWithReconnect(ctx, root.handleInvalidation, func() {
			c.InvalidateCache()
			root.attrCache.InvalidateAll()
			root.dirCache.InvalidateAll()
			root.notifyPrefixChange("/")
		}); err != nil {
			// Non-fatal: log and fall back to TTL-based consistency.
			log.Printf("afs: failed to start invalidation subscriber: %v", err)
		}
	}

	return server, nil
}

func mountOptions(opts *Options) *fs.Options {
	fuseOpts := &fs.Options{
		// All attributes carry explicit POSIX modes, including mode 000.
		NullPermissions: true,
		MountOptions: fuse.MountOptions{
			AllowOther:   opts.AllowOther,
			FsName:       "afs",
			Name:         "afs",
			Debug:        opts.Debug,
			EnableLocks:  true,
			MountContext: opts.MountContext,
		},
		EntryTimeout: &opts.AttrTimeout,
		AttrTimeout:  &opts.AttrTimeout,

		UID: opts.UID,
		GID: opts.GID,
	}

	if opts.AllowOther {
		// The adapters trust the mount owner. When other users can enter,
		// have the kernel enforce the reported POSIX modes and ownership.
		fuseOpts.MountOptions.Options = append(fuseOpts.MountOptions.Options, "default_permissions")
	}
	if opts.ReadOnly {
		fuseOpts.MountOptions.Options = append(fuseOpts.MountOptions.Options, "ro")
	}
	return fuseOpts
}

// handleInvalidation is the subscriber callback. It drops matching entries
// from the FUSE-layer attrCache/dirCache and pushes kernel-level notifies
// through go-fuse so the in-kernel dentry and page caches forget the stale
// data. Messages originating from this client have already been filtered out
// upstream, so every call here is a peer's mutation.
func (r *FSRoot) handleInvalidation(ev client.InvalidateEvent) {
	for _, p := range ev.Paths {
		if p == "" {
			continue
		}
		switch ev.Op {
		case client.InvalidateOpInode:
			r.invalidatePath(p)
			r.notifyContentChange(p)
		case client.InvalidateOpDir:
			r.dirCache.Invalidate(p)
			// Don't touch the kernel here: afsfs Readdir will re-populate
			// from the fresh LsLong on next READDIR, and go-fuse has no
			// "invalidate the listing only" operation.
		case client.InvalidateOpPrefix, client.InvalidateOpRootReplace:
			r.invalidatePathPrefix(p)
			r.notifyPrefixChange(p)
		case client.InvalidateOpContent:
			r.invalidatePath(p)
			r.notifyContentChange(p)
		}
	}
}

// notifyEntryChange tells the kernel to forget the cached dentry for path's
// basename under its parent directory. Used for create, delete, metadata
// changes — anything that changes what a Lookup would return.
func (r *FSRoot) notifyEntryChange(path string) {
	if r.server.Load() == nil {
		return
	}
	if path == "/" {
		return
	}
	parent, name := splitParent(path)
	parentInode := r.findInode(parent)
	if parentInode == nil {
		// Kernel never saw this parent; nothing to invalidate.
		return
	}
	_ = parentInode.NotifyEntry(name)
}

// notifyPrefixChange walks the kernel-known inodes under path and issues
// NotifyEntry on each. Paths the kernel doesn't know about are silently
// skipped (GetChild returns nil).
func (r *FSRoot) notifyPrefixChange(prefix string) {
	if r.server.Load() == nil {
		return
	}
	// First, invalidate the entry for the prefix itself.
	r.notifyEntryChange(prefix)

	// Then walk the subtree. We bound this to nodes currently known to the
	// kernel: unknown descendants return nil from GetChild and we stop.
	root := r.findInode(prefix)
	if root == nil {
		return
	}
	r.walkNotifyEntries(root)
}

func (r *FSRoot) walkNotifyEntries(n *fs.Inode) {
	for name, child := range n.Children() {
		_ = n.NotifyEntry(name)
		if child != nil {
			_ = child.NotifyContent(0, -1)
		}
		if child != nil {
			r.walkNotifyEntries(child)
		}
	}
}

// notifyContentChange tells the kernel to drop cached bytes for the file at
// path. Full-file invalidation for v1.
func (r *FSRoot) notifyContentChange(path string) {
	if r.server.Load() == nil {
		return
	}
	node := r.findInode(path)
	if node == nil {
		return
	}
	_ = node.NotifyContent(0, -1)
	// Also drop the dentry so the next Getattr re-reads size/mtime.
	r.notifyEntryChange(path)
}

// findInode walks the live go-fuse Inode tree from the root down, returning
// the Inode for path or nil if the kernel doesn't have it cached. This is a
// read-only walk: it never creates new Inodes.
func (r *FSRoot) findInode(path string) *fs.Inode {
	node := &r.Inode
	if path == "" || path == "/" {
		return node
	}
	trimmed := strings.TrimPrefix(path, "/")
	for _, comp := range strings.Split(trimmed, "/") {
		if comp == "" {
			continue
		}
		child := node.GetChild(comp)
		if child == nil {
			return nil
		}
		node = child
	}
	return node
}

// splitParent returns (parent dir, basename) for an absolute path. For "/foo"
// it returns ("/", "foo"). For "/" it returns ("/", "").
func splitParent(path string) (string, string) {
	if path == "/" || path == "" {
		return "/", ""
	}
	parent := filepath.Dir(path)
	name := filepath.Base(path)
	if parent == "." {
		parent = "/"
	}
	return parent, name
}

// Statfs implements fs.NodeStatfser.
func (n *FSNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	info, err := n.client.Info(ctx)
	if err != nil {
		log.Printf("Statfs error: %v", err)
		return syscall.EIO
	}

	const blockSize = 4096
	totalBlocks := uint64(info.TotalDataBytes+blockSize-1) / blockSize
	if totalBlocks < 1024 {
		totalBlocks = 1024
	}

	out.Bsize = blockSize
	out.Frsize = blockSize
	out.Blocks = totalBlocks * 10 // report 10x used as total
	out.Bfree = totalBlocks * 9
	out.Bavail = totalBlocks * 9
	out.Files = uint64(info.TotalInodes)
	out.Ffree = 1000000
	out.NameLen = 255
	return 0
}

// Getattr implements fs.NodeGetattrer.
func (n *FSNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if handle, ok := fh.(*FileHandle); ok {
		st, err := n.client.StatInode(ctx, handle.inode)
		if err != nil {
			return mapError(err)
		}
		if st == nil {
			return syscall.ESTALE
		}
		out.Attr = statToAttr(st)
		out.SetTimeout(n.opts.AttrTimeout)
		return 0
	}
	// Check cache first.
	if cached, ok := n.attrCache.Get(n.currentPath()); ok && (n.StableAttr().Ino == 0 || cached.(fuse.Attr).Ino == n.StableAttr().Ino) {
		if err := n.client.Check(ctx); err != nil {
			return mapError(err)
		}
		out.Attr = cached.(fuse.Attr)
		out.SetTimeout(n.opts.AttrTimeout)
		return 0
	}

	st, err := n.stat(ctx)
	if err != nil {
		return mapError(err)
	}
	if st == nil {
		return syscall.ENOENT
	}

	attr := statToAttr(st)
	n.attrCache.Set(n.currentPath(), attr)
	out.Attr = attr
	out.SetTimeout(n.opts.AttrTimeout)
	return 0
}

// Setattr implements fs.NodeSetattrer.
func (n *FSNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.opts.ReadOnly {
		return syscall.EROFS
	}

	st, err := n.stat(ctx)
	if err != nil {
		return mapError(err)
	}
	if st == nil {
		return syscall.ESTALE
	}
	ctx = client.WithExpectedStat(ctx, st)

	// Handle truncate.
	if sz, ok := in.GetSize(); ok {
		var err error
		if handle, ok := fh.(*FileHandle); ok {
			err = n.client.TruncateInodeAtPath(ctx, handle.inode, n.currentPath(), int64(sz))
		} else if n.StableAttr().Ino != 0 {
			err = n.client.TruncateInodeAtPath(ctx, n.StableAttr().Ino, n.currentPath(), int64(sz))
		} else {
			err = n.client.Truncate(ctx, n.currentPath(), int64(sz))
		}
		if err != nil {
			return mapError(err)
		}
	}

	st, err = n.stat(ctx)
	if err != nil {
		return mapError(err)
	}
	if st == nil {
		return syscall.ESTALE
	}
	ctx = client.WithExpectedStat(ctx, st)

	var upd client.AttrUpdate
	if mode, ok := in.GetMode(); ok {
		mode &= 07777
		upd.Mode = &mode
	}
	if uid, ok := in.GetUID(); ok {
		upd.UID = &uid
	}
	if gid, ok := in.GetGID(); ok {
		upd.GID = &gid
	}
	if atime, ok := in.GetATime(); ok {
		ms := atime.UnixMilli()
		upd.AtimeMs = &ms
	}
	if mtime, ok := in.GetMTime(); ok {
		ms := mtime.UnixMilli()
		upd.MtimeMs = &ms
	}
	if !upd.IsEmpty() {
		p, err := n.client.InodePath(ctx, st.Inode)
		if err != nil {
			return mapError(err)
		}
		if err := n.client.SetAttrs(ctx, p, upd); err != nil {
			return mapError(err)
		}
	}

	n.attrCache.Invalidate(n.currentPath())

	status := n.Getattr(ctx, fh, out)
	if status == 0 {
		// go-fuse applies the mount's ownership defaults after GETATTR and
		// LOOKUP, but passes SETATTR replies through unchanged. Match those
		// replies so chmod/truncate cannot cache Redis's default 0:0 owner.
		if out.Uid == 0 {
			out.Uid = n.opts.UID
		}
		if out.Gid == 0 {
			out.Gid = n.opts.GID
		}
	}
	return status
}

// GetOwnership returns the calling process's uid/gid, the default owner for
// files in a mount. Callers that need a different owner set Options.UID and
// Options.GID instead.
func GetOwnership() (uint32, uint32) {
	return uint32(os.Getuid()), uint32(os.Getgid())
}

// Ensure interfaces are satisfied.
var _ fs.NodeStatfser = (*FSNode)(nil)
var _ fs.NodeGetattrer = (*FSNode)(nil)
var _ fs.NodeSetattrer = (*FSNode)(nil)

func (n *FSNode) stat(ctx context.Context) (*client.StatResult, error) {
	if inode := n.StableAttr().Ino; inode != 0 {
		return n.client.StatInode(ctx, inode)
	}
	return n.client.Stat(ctx, n.currentPath())
}

// childContext resolves a kernel-held directory by inode and retains that
// identity as a precondition through the native namespace mutation.
func (n *FSNode) childContext(ctx context.Context, name string) (*FSNode, context.Context, error) {
	inode := n.StableAttr().Ino
	if inode == 0 {
		return n.newChild(name), ctx, nil
	}
	parent, err := n.client.InodePath(ctx, inode)
	if err != nil {
		return nil, ctx, err
	}
	child := n.newChild(name)
	child.fsPath = filepath.Join(parent, name)
	return child, client.WithExpectedParent(ctx, parent, inode), nil
}
