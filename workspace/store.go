// Package workspace provides request-scoped access to the retained AFS engine.
// It needs Redis, but no local folder, mount, or synchronization daemon.
package workspace

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

var (
	ErrAlreadyExists    = client.ErrAlreadyExists
	ErrWorkspaceChanged = client.ErrWorkspaceChanged
	ErrNotFound         = os.ErrNotExist
)

// Store owns one Redis connection pool. All file data and workspace metadata
// remain in the ordinary AFS namespace, compatible with the existing CLI.
type Store struct {
	rdb     *redis.Client
	store   *controlplane.Store
	service *controlplane.Service
}

// Workspace is a handle to one immutable workspace identity and generation.
// Reopen it after a checkpoint restore; old handles fail closed.
type Workspace struct {
	Name       string
	StorageID  string
	FSKey      string
	Generation string
	fs         client.Client
}

func New(redisURL string) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, errors.New("invalid Redis connection URL")
	}
	rdb := redis.NewClient(opts)
	store := controlplane.NewStore(rdb)
	return &Store{rdb: rdb, store: store, service: controlplane.NewService(store)}, nil
}

// Open resolves an existing workspace without creating or rematerializing it.
// An incomplete or replaced root is rejected rather than rebuilt over live data.
func (s *Store) Open(ctx context.Context, name string) (*Workspace, error) {
	if err := controlplane.ValidateName("workspace", name); err != nil {
		return nil, err
	}
	meta, err := s.service.GetWorkspace(ctx, name)
	if err != nil {
		return nil, err
	}
	id := controlplane.WorkspaceStorageID(meta)
	generation, err := s.service.WorkspaceGeneration(ctx, id)
	if err != nil {
		return nil, err
	}
	fsKey := controlplane.WorkspaceFSKey(id)
	fs := &scopedClient{Client: client.New(s.rdb, fsKey), generation: generation}
	root, err := fs.Stat(ctx, "/")
	if err != nil {
		return nil, err
	}
	if root == nil || root.Type != "dir" {
		return nil, ErrWorkspaceChanged
	}
	return &Workspace{Name: meta.Name, StorageID: id, FSKey: fsKey, Generation: generation, fs: fs}, nil
}

// Ensure creates a missing workspace through the existing AFS bootstrap. It
// reuses an existing tree untouched, including its checkpoints and generation.
// Concurrent first callers wait on the engine's existing import/name lock.
func (s *Store) Ensure(ctx context.Context, name string) (*Workspace, error) {
	for {
		workspace, err := s.Open(ctx, name)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return workspace, err
		}
		_, createErr := s.service.CreateWorkspace(ctx, name)
		if createErr == nil {
			return s.Open(ctx, name)
		}
		// Another creator can publish between Open and CreateWorkspace. An
		// uncertain create result can also have published before its reply.
		if workspace, openErr := s.Open(ctx, name); openErr == nil {
			return workspace, nil
		} else if !errors.Is(openErr, os.ErrNotExist) {
			return nil, openErr
		}
		if !errors.Is(createErr, controlplane.ErrImportInProgress) {
			return nil, createErr
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Client delegates directly to the AFS filesystem and binds every request to
// this handle's generation. It starts no lease or subscription goroutine.
func (w *Workspace) Client() client.Client { return w.fs }

func (s *Store) Close() error { return s.rdb.Close() }
