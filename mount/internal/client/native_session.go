package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNativeSessionLost = errors.New("native mount session expired or closed; remount required")

type nativeLeaseKey struct{}
type nativeRequestCheckedKey struct{}

const nativeSessionTTL = 60 * time.Second

// Native sessions own expiring leases. A crashed gateway cannot leave advisory
// locks behind forever, and an expired session never silently reacquires them.
type nativeSession struct {
	*nativeClient
	generation string
	lease      string
	ctx        context.Context
	cancel     context.CancelFunc
	lost       atomic.Bool
	mu         sync.Mutex
	active     int
	paused     bool
	changed    chan struct{}
	barrier    chan struct{}
	closeOnce  sync.Once
	lockInodes sync.Map
}

func NewNative(ctx context.Context, rdb *redis.Client, key, generation string) (NativeClient, error) {
	return NewNativeWithCache(ctx, rdb, key, generation, 0)
}

func NewNativeWithCache(ctx context.Context, rdb *redis.Client, key, generation string, ttl time.Duration) (NativeClient, error) {
	if generation == "" || generation == "deleted" {
		return nil, ErrWorkspaceChanged
	}
	core := newNativeClient(rdb, key, nil).(*nativeClient)
	if ttl > 0 {
		core = newNativeClientWithCache(rdb, key, ttl, nil).(*nativeClient)
	}
	lifetime, cancel := context.WithCancel(ctx)
	s := &nativeSession{nativeClient: core, generation: generation, lease: core.keys.session(core.originID), ctx: lifetime, cancel: cancel, changed: make(chan struct{}), barrier: make(chan struct{}, 1)}
	bound := WithWorkspaceGeneration(ctx, generation)
	if err := core.ensureRoot(bound); err != nil {
		cancel()
		return nil, err
	}
	if err := rdb.Set(ctx, s.lease, "1", nativeSessionTTL).Err(); err != nil {
		cancel()
		return nil, err
	}
	if err := s.check(s.bind(ctx)); err != nil {
		_ = s.Close()
		return nil, err
	}
	go s.renew()
	return s, nil
}

func (s *nativeSession) bind(ctx context.Context) context.Context {
	return context.WithValue(WithWorkspaceGeneration(ctx, s.generation), nativeLeaseKey{}, s.lease)
}

func (s *nativeSession) check(ctx context.Context) error {
	if s.lost.Load() || s.ctx.Err() != nil {
		return ErrNativeSessionLost
	}
	err := s.nativeClient.checkGeneration(ctx)
	if errors.Is(err, ErrNativeSessionLost) || errors.Is(err, ErrWorkspaceChanged) {
		if errors.Is(err, ErrNativeSessionLost) {
			s.lost.Store(true)
		}
		s.InvalidateCache()
	}
	return err
}

func (s *nativeSession) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *nativeSession) begin(ctx context.Context) (context.Context, func(error) error, error) {
	for {
		s.mu.Lock()
		if !s.paused {
			s.active++
			s.mu.Unlock()
			break
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, nil, ErrNativeSessionLost
		case <-changed:
		}
	}
	bound := s.bind(ctx)
	finish := func(err error) error {
		if err == nil {
			err = s.check(bound)
		}
		s.mu.Lock()
		s.active--
		s.notifyLocked()
		s.mu.Unlock()
		return err
	}
	if err := s.check(bound); err != nil {
		return nil, nil, finish(err)
	}
	// The wrapper checks before and after this whole request. Internal path
	// resolution can skip duplicate checks; publication scripts and WATCH
	// transactions still enforce generation/session at the actual mutation.
	return context.WithValue(bound, nativeRequestCheckedKey{}, true), finish, nil
}

// Barrier briefly pauses admission, drains requests, and validates the same
// session. Cancellation always resumes admission; it does not unmount anything.
func (s *nativeSession) Barrier(ctx context.Context) error {
	select {
	case s.barrier <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.barrier }()
	s.mu.Lock()
	s.paused = true
	defer func() {
		s.paused = false
		s.notifyLocked()
		s.mu.Unlock()
	}()
	for s.active > 0 {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			return ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	return s.check(s.bind(ctx))
}

var renewNativeSession = redis.NewScript(`
if redis.call('GET',KEYS[1]) ~= ARGV[1] then return -2 end
if redis.call('EXISTS',KEYS[2]) == 0 then return -5 end
redis.call('PEXPIRE',KEYS[2],ARGV[2])
for i=3,#KEYS do redis.call('PEXPIRE',KEYS[i],tonumber(ARGV[2])*2) end
return 1
`)

func (s *nativeSession) renew() {
	ticker := time.NewTicker(nativeSessionTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, nativeSessionTTL/6)
			keys := []string{s.keys.generation(), s.lease}
			s.lockInodes.Range(func(key, _ interface{}) bool { keys = append(keys, key.(string)); return true })
			code, err := renewNativeSession.Run(ctx, s.rdb, keys, s.generation, nativeSessionTTL.Milliseconds()).Int()
			cancel()
			if err == nil && code < 0 {
				if code == -5 {
					s.lost.Store(true)
				}
				s.InvalidateCache()
				return
			}
		}
	}
}

func (s *nativeSession) lockOwner(owner string) string { return s.lease + "|" + owner }

func (s *nativeSession) Check(ctx context.Context) error { return s.check(s.bind(ctx)) }

func (s *nativeSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.lost.Store(true)
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = s.rdb.Del(ctx, s.lease).Err()
	})
	return err
}

func nativeLease(ctx context.Context) string {
	lease, _ := ctx.Value(nativeLeaseKey{}).(string)
	return lease
}

func (c *nativeClient) leaseGuardKey(ctx context.Context) string {
	if lease := nativeLease(ctx); lease != "" {
		return lease
	}
	return c.keys.generation()
}
