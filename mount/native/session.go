// Package native runs the optional FUSE and NFS exposure layers. The ordinary
// afs command imports mountcontrol instead, keeping driver code out of its binary.
package native

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/mountcontrol"
	"github.com/rowantrollope/afs/mount/internal/afsfs"
	"github.com/rowantrollope/afs/mount/internal/client"
	"github.com/rowantrollope/afs/mount/internal/nfsfs"
	nfs "github.com/willscott/go-nfs"
)

type Config struct {
	Backend    string
	Mountpoint string
	RedisURL   string
	RedisKey   string
	Generation string
	ReadOnly   bool
	UID        *uint32
	GID        *uint32
	AllowOther bool
}

type Session struct {
	cfg       Config
	rdb       *redis.Client
	client    client.NativeClient
	cancel    context.CancelFunc
	fuse      *fuse.Server
	listener  *trackedListener
	endpoint  string
	created   bool
	mu        sync.Mutex
	closed    bool
	startErr  error
	closeOnce sync.Once
	done      chan struct{}
	flushGate chan struct{}
}

// Start mounts one workspace. StartExport exposes the same NFS adapter over a
// private loopback TCP listener for protocol tests, without an OS mount.
func Start(ctx context.Context, cfg Config) (*Session, error) { return start(ctx, cfg, false) }
func StartExport(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Backend != "nfs" {
		return nil, errors.New("an unmounted export requires the NFS backend")
	}
	if cfg.Mountpoint != "" {
		return nil, errors.New("an unmounted export must not set a mountpoint")
	}
	return start(ctx, cfg, true)
}

func start(ctx context.Context, cfg Config, exportOnly bool) (result *Session, err error) {
	if cfg.Backend != "fuse" && cfg.Backend != "nfs" {
		return nil, errors.New("native backend must be fuse or nfs")
	}
	if cfg.Backend != "fuse" && (cfg.UID != nil || cfg.GID != nil || cfg.AllowOther) {
		return nil, errors.New("uid, gid and allow-other require the FUSE backend")
	}
	if cfg.RedisKey == "" || cfg.Generation == "" {
		return nil, errors.New("native mount requires a workspace key and generation")
	}
	s := &Session{cfg: cfg, done: make(chan struct{}), flushGate: make(chan struct{}, 1)}
	mountAttempted := false

	defer func() {
		if err == nil {
			return
		}
		if mountAttempted {
			if mounted, checkErr := isMountpoint(cfg.Mountpoint); checkErr == nil && mounted {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				cleanupErr := unmountOS(cleanup, cfg.Mountpoint, false)
				cancel()
				if cleanupErr != nil {
					s.mu.Lock()
					s.startErr = err
					s.mu.Unlock()
					result = s // Keep authenticated control alive for an explicit detach.
					return
				}
			}
		}
		s.closeResources()
	}()
	if !exportOnly {
		if !filepath.IsAbs(cfg.Mountpoint) || filepath.Clean(cfg.Mountpoint) != cfg.Mountpoint || cfg.Mountpoint == "/" {
			return nil, errors.New("mountpoint must be a clean absolute directory other than root")
		}
		if err := prepareMountpoint(cfg.Mountpoint, &s.created); err != nil {
			return nil, err
		}
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL: %w", err)
	}
	opts.PoolSize = 16
	s.rdb = redis.NewClient(opts)
	lifetime, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	// A short metadata TTL is a fallback when a peer event is lost. Reconnect
	// also clears caches; normal invalidations keep the hot path warm.
	s.client, err = client.NewNativeWithCache(lifetime, s.rdb, cfg.RedisKey, cfg.Generation, time.Second)
	if err != nil {
		return nil, err
	}
	if _, err = s.client.Stat(ctx, "/"); err != nil {
		return nil, err
	}
	switch cfg.Backend {
	case "fuse":
		mountAttempted = true
		s.fuse, err = afsfs.Mount(lifetime, cfg.Mountpoint, s.client, fuseOptions(ctx, cfg))
		if err != nil {
			return nil, err
		}
		go func() { s.fuse.Wait(); s.closeResources() }()
	case "nfs":
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return nil, e
		}
		s.listener = newTrackedListener(listener)
		s.endpoint = listener.Addr().String()
		handler := newNFSHandler(nfsfs.New(s.client, cfg.ReadOnly), "/"+cfg.RedisKey)
		if err = s.client.SubscribeInvalidationsWithReconnect(lifetime, func(ev client.InvalidateEvent) {
			for _, p := range ev.Paths {
				handler.InvalidateVerifier(filepath.Dir(p))
			}
		}, func() { s.client.InvalidateCache(); handler.InvalidateVerifier("/") }); err != nil {
			return nil, err
		}
		server := &nfs.Server{Handler: handler, Context: lifetime}
		go func() { _ = server.Serve(s.listener) }()
		if !exportOnly {
			mountAttempted = true
			if err = mountNFS(ctx, cfg.Mountpoint, s.endpoint, "/"+cfg.RedisKey, cfg.ReadOnly); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func fuseOptions(ctx context.Context, cfg Config) *afsfs.Options {
	uid, gid := afsfs.GetOwnership()
	if cfg.UID != nil {
		uid = *cfg.UID
	}
	if cfg.GID != nil {
		gid = *cfg.GID
	}
	return &afsfs.Options{MountContext: ctx, AttrTimeout: time.Second, ReadOnly: cfg.ReadOnly, UID: uid, GID: gid, AllowOther: cfg.AllowOther}
}

func prepareMountpoint(path string, created *bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(path, 0o755); err != nil {
			return err
		}
		*created = true
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mountpoint must be a directory, not a symlink")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("native mountpoint must be empty")
	}
	if mounted, err := isMountpoint(path); err != nil {
		return err
	} else if mounted {
		return errors.New("directory is already a mountpoint")
	}
	return nil
}

func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Endpoint() string      { return s.endpoint }

func (s *Session) Status(ctx context.Context) mountcontrol.Result {
	s.mu.Lock()
	closed := s.closed
	startErr := s.startErr
	s.mu.Unlock()
	r := mountcontrol.Result{Success: true, Backend: s.cfg.Backend, Endpoint: s.endpoint}
	if startErr != nil {
		r.Error = startErr.Error()
		return r
	}
	if closed {
		r.Error = "native mount is stopped"
		return r
	}
	_, err := s.client.Stat(ctx, "/")
	r.Connected = err == nil
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// Flush first pushes kernel buffers through the adapter, then joins client
// operations. It certifies Redis visibility, not Redis disk persistence. The
// application must pause writes for a stable checkpoint boundary.
func (s *Session) Flush(ctx context.Context) error {
	select {
	case s.flushGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	finished := make(chan error, 1)
	go func() {
		defer func() { <-s.flushGate }()
		var err error
		if s.cfg.Mountpoint != "" {
			err = flushMount(ctx, s.cfg.Mountpoint)
		}
		if err == nil {
			err = s.client.Barrier(ctx)
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unmount leaves the live session intact if flush or normal unmount fails.
// Force is only used for an explicit detach request.
func (s *Session) Unmount(ctx context.Context, force bool) error {
	if !force {
		if err := s.Flush(ctx); err != nil {
			return err
		}
	}
	if s.cfg.Mountpoint != "" {
		var err error
		if s.fuse != nil && !force {
			err = s.fuse.Unmount()
		} else {
			err = unmountOS(ctx, s.cfg.Mountpoint, force)
		}
		if err != nil {
			return err
		}
	}
	s.closeResources()
	return nil
}

func (s *Session) closeResources() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.rdb != nil {
			_ = s.rdb.Close()
		}
		if s.created {
			_ = os.Remove(s.cfg.Mountpoint)
		}
		close(s.done)
	})
}
