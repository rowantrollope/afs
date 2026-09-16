package nfsfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/client"
)

var _ billy.Filesystem = (*FS)(nil)
var _ billy.Change = (*FS)(nil)

type FS struct {
	client      client.NativeClient
	readOnly    bool
	debug       bool
	handleInode uint64
	handlePath  string
	handleDir   bool
}

func New(c client.NativeClient, readOnly bool) *FS {
	return &FS{
		client:   c,
		readOnly: readOnly,
		debug:    os.Getenv("AFS_NFS_DEBUG") == "1",
	}
}

func (f *FS) withTimeout() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if f.handleDir {
		ctx = client.WithExpectedParent(ctx, f.handlePath, f.handleInode)
	}
	return ctx, cancel
}

func (f *FS) normalize(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p)
	if clean == "." {
		return "/"
	}
	return clean
}

func (f *FS) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

func (f *FS) debugf(format string, args ...interface{}) {
	if !f.debug {
		return
	}
	log.Printf("nfsfs: "+format, args...)
}

func (f *FS) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	p := f.normalize(filename)
	f.debugf("OpenFile path=%q flag=%#x perm=%#o", p, flag, perm.Perm())

	ctx, cancel := f.withTimeout()
	defer cancel()

	st, err := f.stat(ctx, p)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			err = os.ErrNotExist
		}
		return nil, err
	}
	if st == nil && f.handleInode != 0 && p == f.handlePath {
		return nil, syscall.ESTALE
	}
	missing := st == nil
	existedBeforeOpen := !missing
	if missing {
		if flag&os.O_CREATE == 0 {
			return nil, os.ErrNotExist
		}
		if f.readOnly {
			return nil, os.ErrPermission
		}
		created, _, err := f.client.CreateFile(ctx, p, uint32(perm.Perm()), flag&os.O_EXCL != 0)
		if err != nil {
			if errors.Is(err, client.ErrAlreadyExists) {
				return nil, os.ErrExist
			}
			return nil, err
		}
		st = created
		missing = false
	}

	if st.Type == "dir" {
		return nil, fmt.Errorf("%s is a directory", p)
	}
	if flag&os.O_EXCL != 0 && flag&os.O_CREATE != 0 && existedBeforeOpen {
		return nil, os.ErrExist
	}

	if flag&os.O_TRUNC != 0 {
		if f.readOnly {
			return nil, os.ErrPermission
		}
		f.debugf("OpenFile truncating inode=%d path=%q", st.Inode, p)
		if err := f.client.TruncateInodeAtPath(ctx, st.Inode, p, 0); err != nil {
			return nil, err
		}
		st.Size = 0
	}

	fh := &fileHandle{
		inode:    st.Inode,
		size:     st.Size,
		fs:       f,
		path:     p,
		writable: flag&(os.O_WRONLY|os.O_RDWR) != 0 || flag&(os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0,
		append:   flag&os.O_APPEND != 0,
	}
	if fh.append {
		fh.pos = st.Size
	}
	return fh, nil
}

func (f *FS) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (f *FS) Stat(filename string) (os.FileInfo, error) {
	p := f.normalize(filename)

	ctx, cancel := f.withTimeout()
	defer cancel()
	st, err := f.stat(ctx, p)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	if st == nil {
		return nil, os.ErrNotExist
	}
	return newFileInfo(path.Base(p), st), nil
}

func (f *FS) Lstat(filename string) (os.FileInfo, error) {
	return f.Stat(filename)
}

func (f *FS) Rename(oldpath, newpath string) error {
	if f.readOnly {
		return os.ErrPermission
	}
	oldN := f.normalize(oldpath)
	newN := f.normalize(newpath)
	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Rename(ctx, oldN, newN, 0)
}

func (f *FS) Remove(filename string) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(filename)

	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Rm(ctx, p)
}

func (f *FS) Join(elem ...string) string {
	if len(elem) == 0 {
		return "/"
	}
	return f.normalize(path.Join(elem...))
}

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	base := f.normalize(dir)
	name := fmt.Sprintf("%s-%d.tmp", prefix, time.Now().UnixNano())
	if base == "/" {
		return f.Create("/" + name)
	}
	return f.Create(base + "/" + name)
}

func (f *FS) ReadDir(p string) ([]os.FileInfo, error) {
	dir := f.normalize(p)
	ctx, cancel := f.withTimeout()
	defer cancel()
	entries, err := f.client.LsLong(ctx, dir)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		st := &client.StatResult{
			Inode: entry.Inode,
			Type:  entry.Type,
			Mode:  entry.Mode,
			UID:   entry.UID,
			GID:   entry.GID,
			Size:  entry.Size,
			Mtime: entry.Mtime,
		}
		out = append(out, newFileInfo(entry.Name, st))
	}
	return out, nil
}

func (f *FS) MkdirAll(filename string, perm os.FileMode) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(filename)
	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.MkdirMode(ctx, p, uint32(perm.Perm()))
}

func (f *FS) Readlink(link string) (string, error) {
	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Readlink(ctx, f.normalize(link))
}

func (f *FS) Symlink(target, link string) error {
	if f.readOnly {
		return os.ErrPermission
	}
	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Ln(ctx, target, f.normalize(link))
}

func (f *FS) Chroot(string) (billy.Filesystem, error) {
	return nil, errors.New("chroot is not supported")
}

func (f *FS) Root() string { return "/" }

func (f *FS) Chmod(name string, mode os.FileMode) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(name)

	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Chmod(ctx, p, uint32(mode.Perm()))
}

func (f *FS) Lchown(name string, uid, gid int) error {
	// Agent Filesystem stores ownership per path; symlink-vs-target semantics are not distinguished.
	return f.Chown(name, uid, gid)
}

func (f *FS) Chown(name string, uid, gid int) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(name)

	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Chown(ctx, p, uint32(uid), uint32(gid))
}

func (f *FS) Chtimes(name string, atime time.Time, mtime time.Time) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(name)

	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Utimens(ctx, p, atime.UnixMilli(), mtime.UnixMilli())
}

// SetAttrs is the batched counterpart of Chmod / Chown / Chtimes used by the
// go-nfs SETATTR and CREATE fast paths. It satisfies third_party/go-nfs's
// optional BatchSetAttrer interface via structural typing — the go-nfs
// package uses a type assertion on billy.Change to detect this method and
// dispatch a single client round trip instead of a sequential Chmod+Lchown+
// Chtimes triple. See third_party/go-nfs/file.go SetFileAttributes.Apply.
//
// Nil pointers mean "do not change this field." An all-nil call returns nil
// immediately after the readOnly check.
func (f *FS) SetAttrs(
	name string,
	mode *os.FileMode,
	uid *int,
	gid *int,
	atime *time.Time,
	mtime *time.Time,
) error {
	if f.readOnly {
		return os.ErrPermission
	}
	p := f.normalize(name)

	var upd client.AttrUpdate
	if mode != nil {
		m := uint32(mode.Perm())
		upd.Mode = &m
	}
	if uid != nil {
		u := uint32(*uid)
		upd.UID = &u
	}
	if gid != nil {
		g := uint32(*gid)
		upd.GID = &g
	}
	if atime != nil {
		ms := atime.UnixMilli()
		upd.AtimeMs = &ms
	}
	if mtime != nil {
		ms := mtime.UnixMilli()
		upd.MtimeMs = &ms
	}
	if upd.IsEmpty() {
		return nil
	}
	ctx, cancel := f.withTimeout()
	defer cancel()
	if f.handleInode != 0 && p == f.handlePath {
		st, err := f.client.StatInode(ctx, f.handleInode)
		if err != nil {
			return err
		}
		if st == nil {
			return syscall.ESTALE
		}
		ctx = client.WithExpectedStat(ctx, st)
	}
	err := f.client.SetAttrs(ctx, p, upd)
	if errors.Is(err, redis.Nil) || errors.Is(err, client.ErrNotFound) {
		return os.ErrNotExist
	}
	return err
}

type fileInfo struct {
	name string
	st   *client.StatResult
}

func newFileInfo(name string, st *client.StatResult) os.FileInfo {
	return fileInfo{name: name, st: st}
}

func (fi fileInfo) Name() string { return fi.name }
func (fi fileInfo) Size() int64  { return fi.st.Size }
func (fi fileInfo) Mode() os.FileMode {
	mode := os.FileMode(fi.st.Mode & 0o777)
	if fi.st.Mode&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if fi.st.Mode&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if fi.st.Mode&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	switch fi.st.Type {
	case "dir":
		mode |= os.ModeDir
	case "symlink":
		mode |= os.ModeSymlink
	}
	return mode
}
func (fi fileInfo) ModTime() time.Time { return time.UnixMilli(fi.st.Mtime) }
func (fi fileInfo) IsDir() bool        { return fi.st.Type == "dir" }
func (fi fileInfo) Sys() interface{} {
	stat := &syscall.Stat_t{
		Ino: fi.st.Inode,
		Uid: fi.st.UID,
		Gid: fi.st.GID,
	}
	if fi.st.Type == "dir" {
		stat.Nlink = 2
	} else {
		stat.Nlink = 1
	}
	return stat
}

type fileHandle struct {
	mu       sync.Mutex
	fs       *FS
	path     string
	inode    uint64
	size     int64
	pos      int64
	writable bool
	append   bool
	closed   bool
}

func (fh *fileHandle) Name() string { return fh.path }

func (fh *fileHandle) ensureOpen() error {
	if fh.closed {
		return os.ErrClosed
	}
	return nil
}

func (fh *fileHandle) Read(p []byte) (int, error) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if err := fh.ensureOpen(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := fh.fs.client.ReadInodeAt(ctx, fh.inode, fh.pos, len(p))
	if err != nil {
		return 0, err
	}
	fh.fs.debugf("Read inode=%d path=%q off=%d size=%d -> %d bytes", fh.inode, fh.path, fh.pos, len(p), len(data))
	if len(data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, data)
	fh.pos += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (fh *fileHandle) ReadAt(p []byte, off int64) (int, error) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if err := fh.ensureOpen(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := fh.fs.client.ReadInodeAt(ctx, fh.inode, off, len(p))
	if err != nil {
		return 0, err
	}
	fh.fs.debugf("ReadAt inode=%d path=%q off=%d size=%d -> %d bytes", fh.inode, fh.path, off, len(p), len(data))
	if len(data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, data)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (fh *fileHandle) Write(p []byte) (int, error) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if err := fh.ensureOpen(); err != nil {
		return 0, err
	}
	if !fh.writable || fh.fs.readOnly {
		return 0, os.ErrPermission
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fh.fs.debugf("Write inode=%d path=%q off=%d len=%d", fh.inode, fh.path, fh.pos, len(p))
	// Pass the known path so the client can update the attribute cache in
	// place and avoid wiping the warm path cache on every write.
	offset := fh.pos
	if fh.append {
		offset = -1
	}
	if err := fh.fs.client.WriteInodeAtPath(ctx, fh.inode, fh.path, p, offset); err != nil {
		return 0, err
	}
	end := fh.pos + int64(len(p))
	if end > fh.size {
		fh.size = end
	}
	fh.pos = end
	fh.fs.debugf("Write complete inode=%d path=%q new_size=%d new_pos=%d", fh.inode, fh.path, fh.size, fh.pos)
	return len(p), nil
}

func (fh *fileHandle) Seek(offset int64, whence int) (int64, error) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if err := fh.ensureOpen(); err != nil {
		return 0, err
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = fh.pos + offset
	case io.SeekEnd:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := fh.fs.client.StatInode(ctx, fh.inode)
		if err != nil {
			return 0, err
		}
		if st == nil {
			return 0, os.ErrNotExist
		}
		fh.size = st.Size
		next = st.Size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if next < 0 {
		return 0, errors.New("negative position")
	}
	fh.pos = next
	return fh.pos, nil
}

func (fh *fileHandle) Close() error {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		return nil
	}
	fh.closed = true
	fh.fs.debugf("Close inode=%d path=%q", fh.inode, fh.path)
	return nil
}

func (fh *fileHandle) Lock() error {
	return errors.New("nfs advisory locking is disabled; mount clients should use nolock/nolocks")
}

func (fh *fileHandle) Unlock() error {
	return errors.New("nfs advisory locking is disabled; mount clients should use nolock/nolocks")
}

func (fh *fileHandle) Truncate(size int64) error {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if err := fh.ensureOpen(); err != nil {
		return err
	}
	if !fh.writable || fh.fs.readOnly {
		return os.ErrPermission
	}
	if size < 0 {
		return errors.New("negative size")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fh.fs.debugf("Truncate inode=%d path=%q size=%d", fh.inode, fh.path, size)
	if err := fh.fs.client.TruncateInodeAtPath(ctx, fh.inode, fh.path, size); err != nil {
		return err
	}
	fh.size = size
	if fh.pos > size {
		fh.pos = size
	}
	return nil
}

// Sync certifies that requests already admitted by the native client completed
// and its pinned workspace is still live. It does not promise Redis fsync.
func (f *FS) Sync(ctx context.Context) error { return f.client.Barrier(ctx) }

// Check fences handle lookups and responses supplied by adapter-local state.
func (f *FS) Check() error {
	ctx, cancel := f.withTimeout()
	defer cancel()
	return f.client.Check(ctx)
}
func (f *FS) stat(ctx context.Context, p string) (*client.StatResult, error) {
	if f.handleInode != 0 && p == f.handlePath {
		return f.client.StatInode(ctx, f.handleInode)
	}
	return f.client.Stat(ctx, p)
}

// StableHandle identifies the same Redis inode across peer renames.
func (f *FS) StableHandle(p string) (uint64, error) {
	p = f.normalize(p)

	ctx, cancel := f.withTimeout()
	defer cancel()
	st, err := f.stat(ctx, p)
	if err != nil {
		f.debugf("StableHandle path=%q error=%T %v", p, err, err)
		return 0, err
	}
	if st == nil {
		f.debugf("StableHandle path=%q missing", p)
		return 0, os.ErrNotExist
	}
	f.debugf("StableHandle path=%q inode=%d", p, st.Inode)
	return st.Inode, nil
}
func (f *FS) FromStableHandle(inode uint64) (billy.Filesystem, []string, error) {
	ctx, cancel := f.withTimeout()
	defer cancel()
	p, err := f.client.InodePath(ctx, inode)
	if err != nil {
		f.debugf("FromStableHandle inode=%d path error=%T %v", inode, err, err)
		return nil, nil, err
	}
	st, err := f.client.StatInode(ctx, inode)
	if err != nil {
		f.debugf("FromStableHandle inode=%d path=%q stat error=%T %v", inode, p, err, err)
		return nil, nil, err
	}
	if st == nil {
		f.debugf("FromStableHandle inode=%d path=%q stat missing", inode, p)
		return nil, nil, syscall.ESTALE
	}
	f.debugf("FromStableHandle inode=%d path=%q type=%s", inode, p, st.Type)
	view := *f
	view.handleDir = st.Type == "dir"
	view.handleInode = inode
	view.handlePath = p
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if p == "/" {
		parts = nil
	}
	return &view, parts, nil
}

// RenameTo carries both NFS directory-handle identities into one native rename.
func (f *FS) RenameTo(oldpath string, destination billy.Filesystem, newpath string) error {
	target, ok := destination.(*FS)
	if !ok || target.client.OriginID() != f.client.OriginID() {
		return syscall.EXDEV
	}
	if f.readOnly || target.readOnly {
		return os.ErrPermission
	}
	ctx, cancel := f.withTimeout()
	defer cancel()
	if target.handleDir {
		ctx = client.WithExpectedParent(ctx, target.handlePath, target.handleInode)
	}
	return f.client.Rename(ctx, f.normalize(oldpath), target.normalize(newpath), 0)
}

// Redis I/O and a following Stat are separate publications. Returning that
// later Stat as post-op attributes could label old kernel pages with a peer's
// newer timestamp. Omit optional I/O post-attrs so NFS revalidates its cache.
func (f *FS) OmitPostOpAttrs() bool { return true }
func (fi fileInfo) AccessTime() time.Time {
	if fi.st.Atime == 0 {
		return fi.ModTime()
	}
	return time.UnixMilli(fi.st.Atime)
}
func (fi fileInfo) ChangeTime() time.Time {
	if fi.st.Ctime == 0 {
		return fi.ModTime()
	}
	return time.UnixMilli(fi.st.Ctime)
}
