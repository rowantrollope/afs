package client

import (
	"errors"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestNativeMkdirModePreservesNewAndExistingPermissions(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	c := setupNativeSession(t, rdb, ctx, "mkdir-mode")
	peer := setupNativeSession(t, rdb, ctx, "mkdir-mode")
	for _, mode := range []uint32{0, 0700, 0750, 02750} {
		path := fmt.Sprintf("/dir-%o", mode)
		if err := c.MkdirMode(ctx, path, mode); err != nil {
			t.Fatal(err)
		}
		st, err := peer.Stat(ctx, path)
		if err != nil || st == nil || st.Mode != mode {
			t.Fatalf("new directory mode: %+v %v, want %o", st, err, mode)
		}
		if err := peer.MkdirMode(ctx, path, 0777); err != nil {
			t.Fatal(err)
		}
		if err := c.Mkdir(ctx, path); err != nil {
			t.Fatal(err)
		}
		fresh, err := c.StatInode(ctx, st.Inode)
		if err != nil || fresh.Mode != mode {
			t.Fatalf("existing directory mode changed: %+v %v", fresh, err)
		}
	}
}

func TestNativeMkdirModeConcurrentCreateDoesNotChmodWinner(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	a := setupNativeSession(t, rdb, ctx, "mkdir-concurrent")
	b := setupNativeSession(t, rdb, ctx, "mkdir-concurrent")
	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("/dir-%d", i)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for n, c := range []NativeClient{a, b} {
			go func(n int, c NativeClient) { <-start; errs <- c.MkdirMode(ctx, path, []uint32{0700, 0750}[n]) }(n, c)
		}
		close(start)
		for n := 0; n < 2; n++ {
			if err := <-errs; err != nil && !errors.Is(err, ErrAlreadyExists) {
				t.Fatal(err)
			}
		}
		a.InvalidateCache()
		st, err := a.Stat(ctx, path)
		if err != nil || st == nil || (st.Mode != 0700 && st.Mode != 0750) {
			t.Fatalf("concurrent directory permissions: %+v %v", st, err)
		}
		winnerMode := st.Mode
		if err := b.MkdirMode(ctx, path, 0000); err != nil {
			t.Fatal(err)
		}
		st, err = a.StatInode(ctx, st.Inode)
		if err != nil || st.Mode != winnerMode {
			t.Fatalf("later create changed winner mode: %+v %v", st, err)
		}
	}
}

func TestNativeMkdirModeFencesGenerationDuringCommit(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	seed := setupNativeSession(t, rdb, ctx, "mkdir-generation")
	root, err := seed.Stat(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	wr := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = wr.Close() })
	c := setupNativeSession(t, wr, ctx, "mkdir-generation")
	fired := false
	wr.AddHook(&parentCommitHook{run: func() error {
		fired = true
		return rdb.Set(ctx, newKeyBuilder("mkdir-generation").generation(), "replaced", 0).Err()
	}})
	if err := c.MkdirMode(WithExpectedParent(ctx, "/", root.Inode), "/stale", 0700); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("stale directory create: %v", err)
	}
	if !fired {
		t.Fatal("did not reach namespace commit window")
	}
	if st, err := New(rdb, "mkdir-generation").Stat(ctx, "/stale"); err != nil || st != nil {
		t.Fatalf("stale directory published: %+v %v", st, err)
	}
}
