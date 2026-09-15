package client

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type nativeLockWaitHook struct {
	ready chan struct{}
	once  sync.Once
}

func (h *nativeLockWaitHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *nativeLockWaitHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *nativeLockWaitHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "hgetall" && strings.Contains(cmd.Args()[1].(string), ":locks:") {
			h.once.Do(func() { close(h.ready) })
		}
		return err
	}
}

func startBlockedNativeLock(t *testing.T) (*nativeSession, *redis.Client, context.Context, uint64, context.CancelFunc, <-chan error) {
	t.Helper()
	rdb, ctx := setupTestRedis(t)
	c := setupNativeSession(t, rdb, ctx, "lock-wait").(*nativeSession)
	st, _, err := c.CreateFile(ctx, "/file", 0644, false)
	if err != nil {
		t.Fatal(err)
	}
	lock := &FileLock{Start: 0, End: 99, Type: syscall.F_WRLCK}
	if err := c.Setlk(ctx, st.Inode, "holder", lock, false); err != nil {
		t.Fatal(err)
	}
	hook := &nativeLockWaitHook{ready: make(chan struct{})}
	rdb.AddHook(hook)
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- c.Setlk(waitCtx, st.Inode, "waiter", lock, true) }()
	select {
	case <-hook.ready:
	case <-waitCtx.Done():
		t.Fatal("waiter never attempted lock")
	}
	return c, rdb, ctx, st.Inode, cancel, result
}

func TestNativeWaitingLockDoesNotBlockBarrier(t *testing.T) {
	c, _, ctx, _, cancel, result := startBlockedNativeLock(t)
	barrierCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if err := c.Barrier(barrierCtx); err != nil {
		t.Fatalf("sleeping lock waiter blocked data barrier: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
}

func TestNativeWaitingLockRetainsLifetimeFences(t *testing.T) {
	for _, action := range []string{"close", "generation"} {
		t.Run(action, func(t *testing.T) {
			c, rdb, ctx, _, _, result := startBlockedNativeLock(t)
			want := ErrNativeSessionLost
			if action == "close" {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				want = ErrWorkspaceChanged
				if err := rdb.Set(ctx, c.keys.generation(), "replaced", 0).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-result; !errors.Is(err, want) {
				t.Fatalf("%s waiter: %v", action, err)
			}
		})
	}
}

func TestNativeWaitingLockRespectsPausedAdmission(t *testing.T) {
	c, rdb, ctx, inode, _, result := startBlockedNativeLock(t)
	_, finish, err := c.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = finish(nil)
		}
	}()
	barrierCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	barrier := make(chan error, 1)
	go func() { barrier <- c.Barrier(barrierCtx) }()
	for {
		c.mu.Lock()
		paused := c.paused
		c.mu.Unlock()
		if paused {
			break
		}
		select {
		case <-barrierCtx.Done():
			t.Fatal("barrier never paused admission")
		case <-time.After(time.Millisecond):
		}
	}
	// The lock becomes available while a separate admitted request keeps the
	// barrier paused. A waiting acquisition must enter through the same gate.
	if err := rdb.HDel(ctx, c.keys.locks(strconv.FormatUint(inode, 10)), c.lockOwner("holder")).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		t.Fatalf("lock acquisition bypassed paused admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := finish(nil); err != nil {
		t.Fatal(err)
	}
	finished = true
	if err := <-barrier; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("lock did not resume after barrier: %v", err)
	}
}
