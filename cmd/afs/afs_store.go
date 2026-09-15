package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"time"
)

// This private adapter preserves the sync engine's original storage seam.
type afsStore struct {
	rdb *redis.Client
	cp  *controlplane.Store
}

func newAFSStore(rdb *redis.Client) *afsStore {
	return &afsStore{rdb: rdb, cp: controlplane.NewStore(rdb)}
}
func (s *afsStore) getWorkspaceMeta(ctx context.Context, w string) (workspaceMeta, error) {
	return s.cp.GetWorkspaceMeta(ctx, w)
}
func (s *afsStore) putWorkspaceMeta(ctx context.Context, m workspaceMeta) error {
	return s.cp.PutWorkspaceMeta(ctx, m)
}
func (s *afsStore) putSavepoint(ctx context.Context, m savepointMeta, tree manifest) error {
	return s.cp.PutSavepoint(ctx, m, tree)
}
func (s *afsStore) getManifest(ctx context.Context, w, c string) (manifest, error) {
	return s.cp.GetManifest(ctx, w, c)
}
func (s *afsStore) saveBlobs(ctx context.Context, w string, b map[string][]byte) error {
	return s.cp.SaveBlobs(ctx, w, b)
}
func (s *afsStore) getBlob(ctx context.Context, w, b string) ([]byte, error) {
	return s.cp.GetBlob(ctx, w, b)
}
func (s *afsStore) addBlobRefs(ctx context.Context, w string, m manifest, at time.Time) error {
	return s.cp.AddBlobRefs(ctx, w, m, at)
}
func (s *afsStore) syncWorkspaceRoot(ctx context.Context, w string, m manifest) error {
	return controlplane.SyncWorkspaceRoot(ctx, s.cp, w, m)
}
func workspaceRedisKey(w string) string { return controlplane.WorkspaceFSKey(w) }

func saveLiveWorkspaceCheckpoint(ctx context.Context, store *afsStore, workspace, name string, printResult bool) (bool, error) {
	_, err := controlplane.NewService(store.cp).SaveCheckpointFromLive(ctx, workspace, name)
	return err == nil, err
}
