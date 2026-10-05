package mcpfiles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const MaxFileBytes = 1 << 20
const MaxListEntries = 1000

type fileRuleError struct{ code, message string }

func (e *fileRuleError) Error() string { return e.message }
func rule(code, message string) error  { return &fileRuleError{code, message} }

type Lifecycle interface {
	Check(context.Context) error
	Verify(context.Context) error
}

type Folder struct {
	root                 *os.File
	directory, workspace string
	info                 os.FileInfo
	lifecycle            Lifecycle
	gate                 chan struct{}
}

var localLocks sync.Map // device/inode -> process-local writer gate

// Open requires an existing mount. The operator owns mount/start/stop/recovery.
func Open(directory, workspace string, lifecycle Lifecycle) (*Folder, error) {
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), directory)
	info, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		root.Close()
		return nil, err
	}
	gate, _ := localLocks.LoadOrStore(fmt.Sprintf("%d:%d", st.Dev, st.Ino), make(chan struct{}, 1))
	return &Folder{root: root, directory: directory, workspace: workspace, info: info, lifecycle: lifecycle, gate: gate.(chan struct{})}, nil
}

func (f *Folder) Close() error { return f.root.Close() }

func (f *Folder) Check(ctx context.Context) error {
	info, err := os.Lstat(f.directory)
	if err != nil || !os.SameFile(info, f.info) {
		return errors.New("mount directory identity changed")
	}
	if err := unix.Faccessat(int(f.root.Fd()), ".", unix.R_OK|unix.W_OK|unix.X_OK, 0); err != nil {
		return errors.New("mount directory is not readable and writable")
	}
	// Custom ignore rules can make a successful local write invisible in Redis.
	// Version one therefore requires a dedicated mount without .afsignore.
	var st unix.Stat_t
	err = unix.Fstatat(int(f.root.Fd()), ".afsignore", &st, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.ENOENT) {
		return errors.New("MCP mount must not contain .afsignore")
	}
	return f.lifecycle.Check(ctx)
}

func reserved(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, ".afs") || n == ".ds_store" || strings.HasPrefix(n, "._") || strings.Contains(n, ".afssync.")
}

func validatePath(p string, directory bool) error {
	if directory && (p == "" || p == ".") {
		return nil
	}
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00:") || strings.HasPrefix(p, "/") {
		return errors.New("use a relative slash-separated path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || reserved(part) {
			return errors.New("traversal and AFS control paths are forbidden")
		}
	}
	return nil
}

// Open each directory relative to its pinned parent and reject symlinks in the
// same syscall. No check-then-follow of agent supplied paths is permitted.
func (f *Folder) directoryFD(p string, create bool) (*os.File, error) {
	fd, err := unix.Openat(int(f.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), ".")
	if p == "" || p == "." {
		return dir, nil
	}
	for _, part := range strings.Split(p, "/") {
		if create {
			if err := unix.Mkdirat(int(dir.Fd()), part, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				dir.Close()
				return nil, err
			}
		}
		next, err := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(next), part)
	}
	return dir, nil
}

func readAt(dir *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return readRegular(file, info)
}

func (f *Folder) Ready(ctx context.Context) error {
	unlock, err := f.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := f.cleanStages(); err != nil {
		return err
	}
	if err := f.Check(ctx); err != nil {
		return err
	}
	return f.lifecycle.Verify(ctx)
}

// A crash between link and unlink can leave the public file with two links.
// Only our recognizable staging names are removed, under the shared lock.
func (f *Folder) cleanStages() error {
	dir, err := f.directoryFD(".afs-lite-sync", false)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		names, err := dir.Readdirnames(100)
		for _, name := range names {
			if !strings.HasPrefix(name, "mcp-") || !strings.HasSuffix(name, ".tmp") || len(name) != 40 {
				continue
			}
			if _, err := hex.DecodeString(name[4:36]); err != nil {
				continue
			}
			if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readRegular(file *os.File, info os.FileInfo) ([]byte, error) {
	if !info.Mode().IsRegular() {
		return nil, rule("unsupported_file", "only regular files are supported")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return nil, err
	}
	if st.Nlink != 1 {
		return nil, rule("unsafe_path", "hard-linked files are forbidden")
	}
	if info.Size() > MaxFileBytes {
		return nil, rule("file_too_large", "file exceeds 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes || !utf8.Valid(data) {
		return nil, rule("unsupported_file", "file must be UTF-8 and at most 1 MiB")
	}
	return data, nil
}

func digest(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }

func (f *Folder) Read(p string) ([]byte, error) {
	if err := validatePath(p, false); err != nil {
		return nil, err
	}
	dir, err := f.directoryFD(filepath.ToSlash(filepath.Dir(p)), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return readAt(dir, filepath.Base(p))
}

type Entry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
}

func (f *Folder) List(p string) ([]Entry, error) {
	if err := validatePath(p, true); err != nil {
		return nil, err
	}
	dir, err := f.directoryFD(p, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(MaxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > MaxListEntries {
		return nil, rule("directory_too_large", "directory exceeds 1000 entries; list a smaller directory")
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		if reserved(name) || validatePath(name, false) != nil {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		kind := "file"
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			kind = "directory"
		case unix.S_IFREG:
			if st.Nlink != 1 {
				continue
			}
		default:
			continue
		}
		entries = append(entries, Entry{Path: filepath.ToSlash(filepath.Join(p, name)), Type: kind, Bytes: st.Size})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Writes from separate MCP processes share an advisory lock, independent of
// AFS's daemon lock. Local/remote non-MCP writers still follow AFS multiwriter
// rules and must not edit these immutable paths during a verification boundary.
func (f *Folder) lock(ctx context.Context) (func(), error) {
	select {
	case f.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	unlock, err := f.flock(ctx)
	if err != nil {
		<-f.gate
		return nil, err
	}
	return func() { unlock(); <-f.gate }, nil
}

func (f *Folder) flock(ctx context.Context) (func(), error) {
	dir, err := f.directoryFD(".afs-lite-sync", false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), "mcp-write.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { unix.Flock(fd, unix.LOCK_UN); unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (f *Folder) Write(ctx context.Context, p string, data []byte, sha string) (bool, error) {
	if err := validatePath(p, false); err != nil {
		return false, err
	}
	if len(data) > MaxFileBytes || !utf8.Valid(data) {
		return false, errors.New("content must be UTF-8 and at most 1 MiB")
	}
	if len(sha) != 64 || sha != digest(data) {
		return false, errors.New("sha256 must be the lowercase SHA-256 of the UTF-8 content")
	}
	unlock, err := f.lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := f.cleanStages(); err != nil {
		return false, err
	}
	if err := f.Check(ctx); err != nil {
		return false, err
	}
	dir, err := f.directoryFD(filepath.ToSlash(filepath.Dir(p)), true)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	name := filepath.Base(p)
	created := false
	existing, err := readAt(dir, name)
	if errors.Is(err, unix.ENOENT) {
		// Stage in the excluded control directory; link atomically without
		// replacing any existing destination. Watchers never see partial bytes.
		stage, stageErr := f.directoryFD(".afs-lite-sync", false)
		if stageErr != nil {
			return false, stageErr
		}
		defer stage.Close()
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return false, err
		}
		tmp := "mcp-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, openErr := unix.Openat(int(stage.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if openErr != nil {
			return false, openErr
		}
		defer unix.Unlinkat(int(stage.Fd()), tmp, 0)
		file := os.NewFile(uintptr(fd), tmp)
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return false, err
		}
		err = unix.Linkat(int(stage.Fd()), tmp, int(dir.Fd()), name, 0)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return false, err
		}
		created = err == nil
		if err = unix.Unlinkat(int(stage.Fd()), tmp, 0); err != nil {
			return created, err
		}
		if err = dir.Sync(); err != nil {
			return created, err
		}
		existing, err = readAt(dir, name)
	}
	if err != nil {
		return created, err
	}
	if digest(existing) != sha {
		return created, errors.New("file exists with different content; choose a new path")
	}
	if err := f.lifecycle.Verify(ctx); err != nil {
		return created, fmt.Errorf("publication unconfirmed; retry identical arguments: %w", err)
	}
	confirmed, err := f.Read(p)
	if err != nil || digest(confirmed) != sha {
		return created, errors.New("file changed during verification; publication unconfirmed")
	}
	return created, nil
}
