package nfsfs

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/client"
	nfs "github.com/willscott/go-nfs"
)

func setupTestRedis(t *testing.T) (*redis.Client, context.Context) {
	t.Helper()

	port := freeTCPPort(t)
	cmd := exec.Command(
		"redis-server",
		"--port", strconv.Itoa(port),
		"--save", "",
		"--appendonly", "no",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:" + strconv.Itoa(port)})
	t.Cleanup(func() { _ = rdb.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := rdb.Ping(ctx).Err(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("redis-server did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}

	return rdb, ctx
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMkdirAllPreservesExistingModeAndAcceptsZero(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-mkdir-mode")
	f := New(c, false)
	if err := f.MkdirAll("/existing", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := f.MkdirAll("/existing", 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stat(ctx, "/existing")
	if err != nil || st == nil || st.Mode != 0o750 {
		t.Fatalf("MkdirAll changed existing directory: %+v, %v", st, err)
	}
	if err := f.MkdirAll("/closed", 0); err != nil {
		t.Fatal(err)
	}
	st, err = c.Stat(ctx, "/closed")
	if err != nil || st == nil || st.Mode != 0 {
		t.Fatalf("mkdir mode 000: %+v, %v", st, err)
	}
}

func TestOpenFileCreateIsImmediate(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-create")
	fs := New(c, false)

	fh, err := fs.OpenFile("/created.txt", os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatalf("open create: %v", err)
	}
	defer func() { _ = fh.Close() }()

	st, err := c.Stat(ctx, "/created.txt")
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if st == nil {
		t.Fatal("expected created file to exist before close")
	}
	if st.Mode != 0o640 {
		t.Fatalf("mode = %o, want 640", st.Mode)
	}
}

func TestOpenFileExclusiveCreateFailsWhenPresent(t *testing.T) {
	t.Parallel()
	rdb, _ := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-exclusive")
	fs := New(c, false)

	if _, err := fs.OpenFile("/exists.txt", os.O_RDWR|os.O_CREATE, 0o644); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	if _, err := fs.OpenFile("/exists.txt", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
}

func TestRenameReplacesExistingFile(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-rename")
	fs := New(c, false)

	if err := c.Echo(ctx, "/src.txt", []byte("src")); err != nil {
		t.Fatalf("echo src: %v", err)
	}
	if err := c.Echo(ctx, "/dst.txt", []byte("dst")); err != nil {
		t.Fatalf("echo dst: %v", err)
	}

	if err := fs.Rename("/src.txt", "/dst.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	data, err := c.Cat(ctx, "/dst.txt")
	if err != nil {
		t.Fatalf("cat renamed dst: %v", err)
	}
	if string(data) != "src" {
		t.Fatalf("expected dst content from src, got %q", string(data))
	}
}

func TestWriteIsVisibleBeforeClose(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-range")
	fs := New(c, false)

	fh, err := fs.OpenFile("/range.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open create: %v", err)
	}
	defer func() { _ = fh.Close() }()

	if _, err := fh.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := c.Cat(ctx, "/range.txt")
	if err != nil {
		t.Fatalf("cat before close: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected immediate write visibility, got %q", string(data))
	}

	if _, err := fh.Seek(1, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	buf := make([]byte, 3)
	n, err := fh.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "ell" {
		t.Fatalf("read after seek = %q, want ell", string(buf[:n]))
	}
}

func TestWriteThenMetadataUpdatePreservesVisibleSize(t *testing.T) {
	t.Parallel()
	rdb, _ := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-cache-size", time.Hour)
	fs := New(c, false)

	fh, err := fs.OpenFile("/size.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open create: %v", err)
	}
	defer func() { _ = fh.Close() }()

	if _, err := fh.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := fs.Stat("/size.txt")
	if err != nil {
		t.Fatalf("stat after write: %v", err)
	}
	if info.Size() != 5 {
		t.Fatalf("size after write = %d, want 5", info.Size())
	}

	now := time.Now()
	if err := fs.Chtimes("/size.txt", now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	info, err = fs.Stat("/size.txt")
	if err != nil {
		t.Fatalf("stat after chtimes: %v", err)
	}
	if info.Size() != 5 {
		t.Fatalf("size after chtimes = %d, want 5", info.Size())
	}

	buf := make([]byte, 5)
	n, err := fh.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("readat after chtimes: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("readat after chtimes = %q, want hello", string(buf[:n]))
	}
}

func TestLockingReportsDisabled(t *testing.T) {
	t.Parallel()
	rdb, _ := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-lock-disabled")
	fs := New(c, false)

	fh, err := fs.OpenFile("/lock.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open create: %v", err)
	}
	defer func() { _ = fh.Close() }()

	if err := fh.Lock(); err == nil {
		t.Fatal("expected explicit nolock error")
	}
}

// ---------------------------------------------------------------------------
// Fix 2 — nfsfs.FS.SetAttrs (batched SETATTR fast path)
// ---------------------------------------------------------------------------

func TestFSSetAttrsFlowsThroughToClient(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-setattrs-flow", time.Hour)
	fs := New(c, false)

	if _, _, err := c.CreateFile(ctx, "/f.txt", 0o644, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mode := os.FileMode(0o600)
	uid := 5000
	gid := 5001
	atime := time.UnixMilli(1700000000000)
	mtime := time.UnixMilli(1700000001000)
	if err := fs.SetAttrs("/f.txt", &mode, &uid, &gid, &atime, &mtime); err != nil {
		t.Fatalf("SetAttrs: %v", err)
	}

	st, err := c.Stat(ctx, "/f.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st == nil {
		t.Fatal("stat nil")
	}
	if st.Mode != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode)
	}
	if st.UID != 5000 {
		t.Errorf("uid = %d, want 5000", st.UID)
	}
	if st.GID != 5001 {
		t.Errorf("gid = %d, want 5001", st.GID)
	}
	if st.Atime != 1700000000000 {
		t.Errorf("atime_ms = %d, want 1700000000000", st.Atime)
	}
	if st.Mtime != 1700000001000 {
		t.Errorf("mtime_ms = %d, want 1700000001000", st.Mtime)
	}
}

func TestFSSetAttrsAppleDoublePersists(t *testing.T) {
	t.Parallel()
	rdb, _ := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-setattrs-sidecar")
	fs := New(c, false)

	// Seed an ordinary "._x.txt" file via the normal OpenFile path.
	fh, err := fs.OpenFile("/._x.txt", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open shadow: %v", err)
	}
	if _, err := fh.Write([]byte("sidecar payload")); err != nil {
		t.Fatalf("write shadow: %v", err)
	}
	_ = fh.Close()

	newMode := os.FileMode(0o600)
	newAtime := time.UnixMilli(1710000000000)
	newMtime := time.UnixMilli(1710000001000)

	if err := fs.SetAttrs("/._x.txt", &newMode, nil, nil, &newAtime, &newMtime); err != nil {
		t.Fatalf("SetAttrs shadow: %v", err)
	}

	info, err := fs.Stat("/._x.txt")
	if err != nil {
		t.Fatalf("stat shadow: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("shadow mode = %o, want 600", info.Mode().Perm())
	}
	if !info.ModTime().Equal(newMtime) {
		t.Errorf("shadow mtime = %v, want %v", info.ModTime(), newMtime)
	}
	// A second native session sees both bytes and attributes after the first closes.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	peer := nativeTestClient(t, rdb, "nfs-setattrs-sidecar")
	got, err := peer.Cat(context.Background(), "/._x.txt")
	if err != nil || string(got) != "sidecar payload" {
		t.Fatalf("persisted sidecar=%q, %v", got, err)
	}
	entries, err := New(peer, false).ReadDir("/")
	if err != nil || len(entries) != 1 || entries[0].Name() != "._x.txt" {
		t.Fatalf("sidecar listing=%v, %v", entries, err)
	}

}

func TestFSSetAttrsMissingFile(t *testing.T) {
	t.Parallel()
	rdb, _ := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-setattrs-sidecar-missing")
	fs := New(c, false)

	newMode := os.FileMode(0o600)
	err := fs.SetAttrs("/._missing.txt", &newMode, nil, nil, nil, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SetAttrs missing shadow: got %v, want os.ErrNotExist", err)
	}
}

func TestFSSetAttrsReadOnly(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	// Seed using a read-write client, then mount read-only.
	cRW := nativeTestClient(t, rdb, "nfs-setattrs-ro")
	if _, _, err := cRW.CreateFile(ctx, "/f.txt", 0o644, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cRO := nativeTestClient(t, rdb, "nfs-setattrs-ro")
	fs := New(cRO, true)
	newMode := os.FileMode(0o600)
	if err := fs.SetAttrs("/f.txt", &newMode, nil, nil, nil, nil); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("SetAttrs on read-only FS: got %v, want os.ErrPermission", err)
	}
}

func TestFSSetAttrsEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-setattrs-empty")
	fs := New(c, false)
	if _, _, err := c.CreateFile(ctx, "/f.txt", 0o644, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Passing all-nil pointers must no-op on the real path; just verify
	// it returns nil without mutating anything.
	if err := fs.SetAttrs("/f.txt", nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("SetAttrs empty: %v", err)
	}

	st, err := c.Stat(ctx, "/f.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode != 0o644 {
		t.Errorf("mode drifted to %o after empty SetAttrs, want 0o644", st.Mode)
	}
}

// TestSetFileAttributesApplyDispatchesToBatchSetAttrer exercises the fast
// path in third_party/go-nfs: SetFileAttributes.Apply must notice that our
// nfsfs.FS implements BatchSetAttrer and dispatch a single SetAttrs call
// instead of the legacy Chmod / Lchown / Chtimes sequence. We verify by
// counting SetAttrs calls into the real native client, whose metadata publication
// is now atomic Lua. The legacy path would dispatch three separate changes.
func TestSetFileAttributesApplyDispatchesToBatchSetAttrer(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)

	const fsKey = "nfs-batchsetattrs"
	var inodeHSets atomic.Int64
	c := &setAttrsCounter{NativeClient: nativeTestClient(t, rdb, fsKey), count: &inodeHSets}
	fs := New(c, false)
	if _, _, err := c.CreateFile(ctx, "/apply.txt", 0o644, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mode := uint32(0o600)
	uid := uint32(7000)
	gid := uint32(7001)
	// Pick atime/mtime that are definitely different from the seed mtime
	// set by CreateFile so the no-op skip doesn't hide the HSET.
	atime := time.UnixMilli(2000000000000)
	mtime := time.UnixMilli(2000000001000)
	sfa := &nfs.SetFileAttributes{
		SetMode:  &mode,
		SetUID:   &uid,
		SetGID:   &gid,
		SetAtime: &atime,
		SetMtime: &mtime,
	}

	inodeHSets.Store(0)
	if err := sfa.Apply(fs, fs, "/apply.txt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := inodeHSets.Load(); got != 1 {
		t.Fatalf("SetFileAttributes.Apply issued %d metadata publications, want 1 (fast path)", got)
	}

	// Double-check the values actually landed.
	st, err := c.Stat(ctx, "/apply.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode)
	}
	if st.UID != 7000 {
		t.Errorf("uid = %d, want 7000", st.UID)
	}
	if st.GID != 7001 {
		t.Errorf("gid = %d, want 7001", st.GID)
	}
	if st.Atime != 2000000000000 {
		t.Errorf("atime_ms = %d, want 2000000000000", st.Atime)
	}
	if st.Mtime != 2000000001000 {
		t.Errorf("mtime_ms = %d, want 2000000001000", st.Mtime)
	}
}

// TestSetFileAttributesApplyEmptyDiffIsFreeRider verifies the no-op skip
// ("candidate 7" free-rider win): when SetFileAttributes.Apply receives a
// request whose mode/uid/gid/atime/mtime already match the current state,
// it must not touch Redis at all. Historically that would have been 1-3
// redundant round trips.
func TestSetFileAttributesApplyEmptyDiffIsFreeRider(t *testing.T) {
	t.Parallel()
	rdb, ctx := setupTestRedis(t)

	const fsKey = "nfs-batchsetattrs-noop"
	var inodeHSets atomic.Int64
	c := &setAttrsCounter{NativeClient: nativeTestClient(t, rdb, fsKey), count: &inodeHSets}
	fs := New(c, false)
	if _, _, err := c.CreateFile(ctx, "/noop.txt", 0o644, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Read the seed state. Then construct a SetFileAttributes whose
	// fields match exactly. Apply must skip the call entirely.
	st, err := c.Stat(ctx, "/noop.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	currentMode := uint32(st.Mode)
	currentUID := uint32(st.UID)
	currentGID := uint32(st.GID)
	currentAtime := time.UnixMilli(st.Atime)
	currentMtime := time.UnixMilli(st.Mtime)
	sfa := &nfs.SetFileAttributes{
		SetMode:  &currentMode,
		SetUID:   &currentUID,
		SetGID:   &currentGID,
		SetAtime: &currentAtime,
		SetMtime: &currentMtime,
	}

	inodeHSets.Store(0)
	if err := sfa.Apply(fs, fs, "/noop.txt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := inodeHSets.Load(); got != 0 {
		t.Fatalf("Apply with no-op diff issued %d metadata publications, want 0 (free-rider skip)", got)
	}
}

type setAttrsCounter struct {
	client.NativeClient
	count *atomic.Int64
}

func (c *setAttrsCounter) SetAttrs(ctx context.Context, p string, upd client.AttrUpdate) error {
	c.count.Add(1)
	return c.NativeClient.SetAttrs(ctx, p, upd)
}

// A kernel directory handle keeps the directory's identity when a peer renames
// it. Resolving the handle before a CREATE is insufficient if the old path is
// reused while the request is in flight.
type beforeCreateClient struct {
	client.NativeClient
	before func()
}

func (c *beforeCreateClient) CreateFile(ctx context.Context, p string, mode uint32, exclusive bool) (*client.StatResult, bool, error) {
	if c.before != nil {
		before := c.before
		c.before = nil
		before()
	}
	return c.NativeClient.CreateFile(ctx, p, mode, exclusive)
}
func TestDirectoryHandleCreateRejectsReusedParent(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := nativeTestClient(t, rdb, "nfs-parent")
	if err := c.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stat(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	gated := &beforeCreateClient{NativeClient: c}
	fs := New(gated, false)
	bound, _, err := fs.FromStableHandle(st.Inode)
	if err != nil {
		t.Fatal(err)
	}
	gated.before = func() {
		if err := c.Rename(ctx, "/dir", "/moved", 0); err != nil {
			t.Fatal(err)
		}
		if err := c.Mkdir(ctx, "/dir"); err != nil {
			t.Fatal(err)
		}
	}
	file, createErr := bound.OpenFile("/dir/child", os.O_CREATE|os.O_RDWR, 0600)
	if file != nil {
		_ = file.Close()
	}
	replacement, err := c.Stat(ctx, "/dir/child")
	if err != nil {
		t.Fatal(err)
	}
	if replacement != nil {
		t.Fatalf("held directory handle created file inside replacement directory; create error=%v", createErr)
	}
	if createErr == nil {
		original, err := c.Stat(ctx, "/moved/child")
		if err != nil || original == nil {
			t.Fatalf("successful create lost: %v", err)
		}
	}
}
