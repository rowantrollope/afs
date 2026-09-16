package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

// This hook moves the held directory after its identity was read and WATCHed,
// but before the mutation's EXEC. It exercises the commit condition, rather
// than merely checking a stale handle before a request starts.
type parentCommitHook struct {
	once sync.Once
	run  func() error
	err  error
}

func (h *parentCommitHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *parentCommitHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *parentCommitHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		parents, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
		if len(parents) > 0 {
			for _, cmd := range cmds {
				args := cmd.Args()
				// File staging also uses a transaction, but has no namespace
				// writes. Only intercept the actual publication transaction.
				mutation := cmd.Name() == "eval" || (cmd.Name() == "hset" && len(args) > 1 && strings.Contains(fmt.Sprint(args[1]), ":dirents:"))
				if mutation {
					h.once.Do(func() { h.err = h.run() })
					if h.err != nil {
						return h.err
					}
					break
				}
			}
		}
		return next(ctx, cmds)
	}
}

func TestNativeExpectedParentRejectsReuseAtCommit(t *testing.T) {
	operations := map[string]func(context.Context, NativeClient) error{
		"create": func(ctx context.Context, c NativeClient) error {
			_, _, err := c.CreateFile(ctx, "/dir/new", 0644, false)
			return err
		},
		"mkdir":              func(ctx context.Context, c NativeClient) error { return c.Mkdir(ctx, "/dir/new") },
		"symlink":            func(ctx context.Context, c NativeClient) error { return c.Ln(ctx, "/target", "/dir/new") },
		"write":              func(ctx context.Context, c NativeClient) error { return c.Echo(ctx, "/dir/file", []byte("bad")) },
		"chmod":              func(ctx context.Context, c NativeClient) error { return c.Chmod(ctx, "/dir/file", 0600) },
		"remove":             func(ctx context.Context, c NativeClient) error { return c.Rm(ctx, "/dir/file") },
		"rename source":      func(ctx context.Context, c NativeClient) error { return c.Rename(ctx, "/dir/file", "/other/file", 0) },
		"rename destination": func(ctx context.Context, c NativeClient) error { return c.Rename(ctx, "/other/file", "/dir/file", 0) },
	}
	for name, op := range operations {
		t.Run(name, func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			peer := setupNativeSession(t, rdb, ctx, "parents")
			for _, p := range []string{"/dir/file", "/other/file"} {
				if err := peer.Echo(ctx, p, []byte("original")); err != nil {
					t.Fatal(err)
				}
			}
			dir, _ := peer.Stat(ctx, "/dir")
			other, _ := peer.Stat(ctx, "/other")
			wr := redis.NewClient(rdb.Options())
			t.Cleanup(func() { _ = wr.Close() })
			writer := setupNativeSession(t, wr, ctx, "parents")
			fired := false
			hook := &parentCommitHook{run: func() error {
				fired = true
				if err := peer.Rename(ctx, "/dir", "/moved", 0); err != nil {
					return err
				}
				return peer.Echo(ctx, "/dir/file", []byte("replacement"))
			}}
			wr.AddHook(hook)
			bound := WithExpectedParent(WithExpectedParent(ctx, "/dir", dir.Inode), "/other", other.Inode)
			if err := op(bound, writer); !errors.Is(err, ErrWriteConflict) {
				t.Fatalf("stale directory mutation: %v", err)
			}
			if !fired {
				t.Fatal("test did not reach publication window")
			}
			peer.InvalidateCache()
			for p, want := range map[string]string{"/moved/file": "original", "/dir/file": "replacement", "/other/file": "original"} {
				got, err := peer.Cat(ctx, p)
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q %v, want %q", p, got, err, want)
				}
			}
			for _, p := range []string{"/moved/new", "/dir/new"} {
				st, err := peer.Stat(ctx, p)
				if err != nil && !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				if st != nil {
					t.Fatalf("stale directory mutation created %s", p)
				}
			}
			st, _ := peer.Stat(ctx, "/moved/file")
			if st.Mode != 0644 {
				t.Fatalf("stale metadata mutation changed mode: %o", st.Mode)
			}
		})
	}
}

func TestNativeExpectedParentComposesAndRejectsImplicitParents(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := setupNativeSession(t, rdb, ctx, "parent-compose")
	if err := c.Echo(ctx, "/a/file", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, "/b"); err != nil {
		t.Fatal(err)
	}
	a, _ := c.Stat(ctx, "/a")
	b, _ := c.Stat(ctx, "/b")
	bound := WithExpectedParent(WithExpectedParent(ctx, "/a", a.Inode), "/b", b.Inode)
	if err := c.Rename(bound, "/a/file", "/b/file", 0); err != nil {
		t.Fatal(err)
	}
	conflicting := WithExpectedParent(bound, "/b", a.Inode)
	if _, _, err := c.CreateFile(conflicting, "/b/rejected", 0644, false); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("conflicting handle accepted: %v", err)
	}
	missing := WithExpectedParent(ctx, "/absent", a.Inode)
	if _, _, err := c.CreateFile(missing, "/absent/rejected", 0644, false); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("missing parent accepted: %v", err)
	}
	if st, err := c.Stat(ctx, "/absent"); st != nil || (err != nil && !errors.Is(err, ErrNotFound)) {
		t.Fatalf("implicit parent created: %+v %v", st, err)
	}
}

func TestNativeRenameRejectsDeletedCachedDestinationParent(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := setupNativeSession(t, rdb, ctx, "parent-deleted")
	peer := setupNativeSession(t, rdb, ctx, "parent-deleted")
	if err := c.Echo(ctx, "/source", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, "/destination"); err != nil {
		t.Fatal(err)
	}
	dst, _ := c.Stat(ctx, "/destination")
	if err := peer.Rm(ctx, "/destination"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename(ctx, "/source", "/destination/file", 0); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("deleted parent accepted: %v", err)
	}
	if exists, err := rdb.Exists(ctx, newKeyBuilder("parent-deleted").inode(fmt.Sprint(dst.Inode))).Result(); err != nil || exists != 0 {
		t.Fatalf("deleted parent recreated: %d %v", exists, err)
	}
	peer.InvalidateCache()
	if got, err := peer.Cat(ctx, "/source"); err != nil || string(got) != "original" {
		t.Fatalf("source changed: %q %v", got, err)
	}
}
