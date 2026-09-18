package controlplane

// This package retains the original Redis manifest/blob checkpoint engine. The
// catalog, tenancy, search, and volume-composition services were removed.
// The optional HTTP server invokes this same engine.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrWorkspaceConflict = errors.New("workspace conflict")

type Service struct{ store *Store }

func NewService(store *Store) *Service { return &Service{store: store} }
func (s *Service) GetWorkspace(ctx context.Context, name string) (WorkspaceMeta, error) {
	return s.store.GetWorkspaceMeta(ctx, name)
}
func (s *Service) ListWorkspaces(ctx context.Context) ([]WorkspaceMeta, error) {
	return s.store.ListWorkspaces(ctx)
}

type RestoreCheckpointResult struct {
	Restored                bool   `json:"restored"`
	CheckpointID            string `json:"checkpoint_id"`
	WorkspaceID             string `json:"workspace_id"`
	WorkspaceName           string `json:"workspace_name"`
	SafetyCheckpointID      string `json:"safety_checkpoint_id,omitempty"`
	SafetyCheckpointCreated bool   `json:"safety_checkpoint_created"`
}

func newOpaqueWorkspaceID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "ws_" + hex.EncodeToString(raw[:]), nil
}
func generatedCheckpointName() string {
	return "cp-" + time.Now().UTC().Format("20060102T150405.000000000")
}

func (s *Service) CreateWorkspace(ctx context.Context, name string) (WorkspaceMeta, error) {
	now := time.Now().UTC()
	return s.CreateWorkspaceFromManifest(ctx, name, Manifest{Version: formatVersion, Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0o755, MtimeMs: now.UnixMilli()}}}, nil)
}

// CreateWorkspaceFromManifest uses the original import's manifest/blob and
// inode materialization path. It publishes the name after content is ready.
func (s *Service) CreateWorkspaceFromManifest(ctx context.Context, name string, m Manifest, blobs map[string][]byte) (WorkspaceMeta, error) {
	return s.CreateWorkspaceStreaming(ctx, name, func(id string, writer *BlobWriter) (Manifest, error) {
		for blobID, size := range manifestBlobRefs(m) {
			data, ok := blobs[blobID]
			if !ok {
				return Manifest{}, fmt.Errorf("manifest blob %q is missing", blobID)
			}
			if err := writer.Submit(ctx, blobID, data, size); err != nil {
				return Manifest{}, err
			}
		}
		return m, nil
	})
}

// CreateWorkspaceStreaming keeps the original bounded, pipelined BlobWriter
// import path. The builder can hand blobs to the writer as files are hashed.
func (s *Service) CreateWorkspaceStreaming(ctx context.Context, name string, build func(string, *BlobWriter) (Manifest, error)) (WorkspaceMeta, error) {
	return s.createWorkspaceStreaming(ctx, name, build, nil)
}

// prepare runs against the fully materialized private workspace before its
// name becomes visible. Fork history and policy must be present at publication.
func (s *Service) createWorkspaceStreaming(ctx context.Context, name string, build func(string, *BlobWriter) (Manifest, error), prepare func(WorkspaceMeta) error) (WorkspaceMeta, error) {
	if build == nil {
		return WorkspaceMeta{}, fmt.Errorf("manifest builder is required")
	}

	if err := ValidateName("workspace", name); err != nil {
		return WorkspaceMeta{}, err
	}
	lock, err := AcquireImportLock(ctx, s.store, name)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	defer lock.Release(context.Background())
	exists, err := s.store.WorkspaceExists(ctx, name)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	if exists {
		return WorkspaceMeta{}, fmt.Errorf("workspace %q already exists", name)
	}
	id, err := newOpaqueWorkspaceID()
	if err != nil {
		return WorkspaceMeta{}, err
	}
	now := time.Now().UTC()
	writer := NewBlobWriter(s.store.rdb, id, now)
	writer.enableImportCache(BlobWriterMaxBytes)
	defer writer.discardImportCache()
	m, err := build(id, writer)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	if err = writer.Flush(ctx); err != nil {
		return WorkspaceMeta{}, err
	}
	m = cloneManifest(m)
	m.Version = formatVersion
	m.Workspace = id
	m.Savepoint = initialCheckpointName
	hash, err := HashManifest(m)
	if err != nil {
		return WorkspaceMeta{}, err
	}
	totals := manifestStats(m)
	meta := WorkspaceMeta{Version: formatVersion, ID: id, Name: name, CreatedAt: now, UpdatedAt: now, HeadSavepoint: initialCheckpointName, DefaultSavepoint: initialCheckpointName}
	cp := SavepointMeta{Version: formatVersion, ID: initialCheckpointName, Name: initialCheckpointName, Workspace: id, ManifestHash: hash, CreatedAt: now, Kind: CheckpointKindSystem, Source: CheckpointSourceCLI, Author: "afs", FileCount: totals.FileCount, DirCount: totals.DirCount, TotalBytes: totals.TotalBytes}
	if err = s.store.PutSavepoint(ctx, cp, m); err != nil {
		return WorkspaceMeta{}, err
	}
	if err = SyncWorkspaceRootWithOptions(ctx, s.store, id, m, SyncOptions{
		BlobProvider:       writer.cachedBlob,
		SkipNamespaceReset: true, // id was freshly allocated and has no live tree.
	}); err != nil {
		return WorkspaceMeta{}, err
	}
	writer.discardImportCache()
	if prepare != nil {
		if err = prepare(meta); err != nil {
			return WorkspaceMeta{}, err
		}
	}
	if err = lock.Lost(); err != nil {
		return WorkspaceMeta{}, err
	}
	generation, err := newWorkspaceGeneration()
	if err != nil {
		return WorkspaceMeta{}, err
	}
	if err = s.store.rdb.Set(ctx, WorkspaceGenerationKey(id), generation, 0).Err(); err != nil {
		return WorkspaceMeta{}, err
	}
	if err = s.store.PutWorkspaceMeta(ctx, meta); err != nil {
		return WorkspaceMeta{}, err
	}
	return meta, s.recordLifecycle(ctx, meta, "workspace", "create", nil)
}

func (s *Service) ListCheckpoints(ctx context.Context, workspace string) ([]SavepointMeta, error) {
	if _, err := s.store.GetWorkspaceMeta(ctx, workspace); err != nil {
		return nil, err
	}
	return s.store.ListSavepoints(ctx, workspace, 0)
}

func (s *Service) GetCheckpoint(ctx context.Context, workspace, ref string) (SavepointMeta, Manifest, error) {
	meta, err := s.store.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return SavepointMeta{}, Manifest{}, err
	}
	if ref == "" || ref == "latest" {
		checkpoints, err := s.store.ListSavepoints(ctx, workspace, 1)
		if err != nil {
			return SavepointMeta{}, Manifest{}, err
		}
		if len(checkpoints) == 0 {
			return SavepointMeta{}, Manifest{}, fmt.Errorf("workspace %q has no checkpoints; create one with afs cp create %s", meta.Name, meta.Name)
		}
		ref = checkpoints[0].ID
	}
	if err = ValidateName("checkpoint", ref); err != nil {
		return SavepointMeta{}, Manifest{}, err
	}
	cp, err := s.store.GetSavepointMeta(ctx, workspace, ref)
	if errors.Is(err, os.ErrNotExist) {
		all, listErr := s.store.ListSavepoints(ctx, workspace, 0)
		if listErr != nil {
			return SavepointMeta{}, Manifest{}, listErr
		}
		for _, candidate := range all {
			if candidate.Name == ref {
				cp = candidate
				err = nil
				break
			}
		}
	}
	if err != nil {
		return SavepointMeta{}, Manifest{}, err
	}
	m, err := s.store.GetManifest(ctx, workspace, cp.ID)
	return cp, m, err
}

// SaveCheckpointFromLive snapshots published remote state. The caller must
// flush its relevant local daemons first; other machines' pending writes are
// outside this method's scope. A concurrent application is not quiesced.
func (s *Service) SaveCheckpointFromLive(ctx context.Context, workspace, name string) (SavepointMeta, error) {
	generation, err := s.WorkspaceGeneration(ctx, workspace)
	if err != nil {
		return SavepointMeta{}, err
	}
	cp, _, err := s.captureCheckpoint(ctx, workspace, name, generation, "", true)
	return cp, err
}

func (s *Service) captureCheckpoint(ctx context.Context, workspace, name, generation, lockToken string, allowUnchanged bool) (SavepointMeta, bool, error) {
	if name == "" {
		name = generatedCheckpointName()
	}
	if err := ValidateName("checkpoint", name); err != nil {
		return SavepointMeta{}, false, err
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return SavepointMeta{}, false, err
	}
	id := workspaceStorageID(meta)
	if err = serverRequireLiveRoot(ctx, s, id); err != nil {
		return SavepointMeta{}, false, err
	}
	changes, err := latestServerChange(ctx, s.store.rdb, id)
	if err != nil {
		return SavepointMeta{}, false, err
	}
	m, blobs, files, dirs, size, err := BuildManifestFromWorkspaceRoot(ctx, s.store.rdb, id, name)
	if err != nil {
		return SavepointMeta{}, false, err
	}
	latest, err := latestServerChange(ctx, s.store.rdb, id)
	if err != nil {
		return SavepointMeta{}, false, err
	}
	if latest != changes {
		return SavepointMeta{}, false, ErrWorkspaceConflict
	}
	options, _ := ctx.Value(checkpointOptionsKey{}).(CheckpointOptions)
	if options.Source == "" {
		options.Source = CheckpointSourceCLI
	}
	if options.Author == "" {
		options.Author = "afs"
	}
	kind := CheckpointKindManual
	if lockToken != "" {
		kind = CheckpointKindSafety
	}
	saved, err := s.saveCheckpoint(ctx, SaveCheckpointRequest{ExpectedChangesID: changes, Description: options.Description, ExpectedGeneration: generation, ImportLockToken: lockToken, Workspace: id, ExpectedHead: meta.HeadSavepoint, CheckpointID: name, Manifest: m, Blobs: blobs, FileCount: files, DirCount: dirs, TotalBytes: size, SkipWorkspaceRootSync: true, AllowUnchanged: allowUnchanged, Kind: kind, Source: options.Source, Author: options.Author})
	if err != nil {
		return SavepointMeta{}, false, err
	}
	if !saved {
		return SavepointMeta{}, false, nil
	}
	cp, err := s.store.GetSavepointMeta(ctx, id, name)
	return cp, true, err
}

func (s *Service) ForkWorkspace(ctx context.Context, source, newName, ref string) error {
	sourceMeta, err := s.store.GetWorkspaceMeta(ctx, source)
	if err != nil {
		return err
	}
	sourceID := workspaceStorageID(sourceMeta)
	cp, m, err := s.GetCheckpoint(ctx, sourceID, ref)
	if err != nil {
		return err
	}
	blobs := map[string][]byte{}
	for blobID := range manifestBlobRefs(m) {
		b, err := s.store.GetBlob(ctx, sourceID, blobID)
		if err != nil {
			return err
		}
		blobs[blobID] = b
	}
	_, err = s.createWorkspaceStreaming(ctx, newName, func(_ string, writer *BlobWriter) (Manifest, error) {
		for blobID, size := range manifestBlobRefs(m) {
			if err := writer.Submit(ctx, blobID, blobs[blobID], size); err != nil {
				return Manifest{}, err
			}
		}
		return m, nil
	}, func(meta WorkspaceMeta) error {
		if err := s.forkFileHistory(ctx, workspaceStorageID(sourceMeta), meta.ID); err != nil {
			return err
		}
		initial, err := getJSON[SavepointMeta](ctx, s.store.rdb, savepointMetaKey(meta.ID, initialCheckpointName))
		if err != nil {
			return err
		}
		initial.Kind = CheckpointKindFork
		initial.ParentSavepoint = cp.ID
		initial.Description = "Forked from " + source + "."
		forkManifest, err := getJSON[Manifest](ctx, s.store.rdb, savepointManifestKey(meta.ID, initialCheckpointName))
		if err != nil {
			return err
		}
		return s.store.PutSavepoint(ctx, initial, forkManifest)
	})
	if err != nil {
		return err
	}
	forked, err := s.GetWorkspace(ctx, newName)
	if err != nil {
		return err
	}
	return s.recordLifecycle(ctx, forked, "workspace", "fork", map[string]string{"source_workspace": sourceID, "checkpoint_id": cp.ID})
}

func (s *Service) RestoreCheckpoint(ctx context.Context, workspace, ref string) (RestoreCheckpointResult, error) {
	cp, m, err := s.GetCheckpoint(ctx, workspace, ref)
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	id := workspaceStorageID(meta)
	lock, err := AcquireImportLock(ctx, s.store, id)
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	defer lock.Release(context.Background())
	previousGeneration, err := s.store.rdb.Get(ctx, WorkspaceGenerationKey(id)).Result()
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	generation, err := newWorkspaceGeneration()
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	result := RestoreCheckpointResult{Restored: true, CheckpointID: cp.ID, WorkspaceID: id, WorkspaceName: meta.Name}
	// Fence writers before capturing the safety checkpoint. A failed safety
	// capture leaves the intact root fenced; retry captures it again. Once root
	// replacement starts, retries do not capture partially materialized content.
	recovering := strings.HasPrefix(previousGeneration, "restoring:")
	fencing := "fencing:" + generation
	if recovering {
		// Keep the partial-root state durable even if recovery preparation is
		// interrupted before the next materialization begins.
		fencing = "restoring:" + generation
	}
	if err = s.store.rdb.Set(ctx, WorkspaceGenerationKey(id), fencing, 0).Err(); err != nil {
		return RestoreCheckpointResult{}, err
	}
	liveRoot, err := s.store.rdb.Exists(ctx, workspaceFSInodeKey(id, workspaceFSRootInodeID)).Result()
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	// Explicit restore can recover a missing root. There is no live tree to
	// capture in that case; keep its existing checkpoints and file lineages.
	if !recovering && liveRoot != 0 {
		safety, saved, err := s.captureCheckpoint(ctx, id, "before-restore-"+time.Now().UTC().Format("20060102T150405.000000000"), fencing, lock.Token(), false)
		if err != nil {
			return RestoreCheckpointResult{}, err
		}
		result.SafetyCheckpointID = safety.ID
		result.SafetyCheckpointCreated = saved
	}
	if err = lock.Lost(); err != nil {
		return RestoreCheckpointResult{}, err
	}
	activityBefore, err := s.prepareRestoreActivity(ctx, id, recovering)
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	history, err := s.prepareRestoreFileHistory(ctx, id, m, "restore-"+generation, recovering)
	if err != nil {
		return RestoreCheckpointResult{}, err
	}
	if err = s.store.rdb.Set(ctx, WorkspaceGenerationKey(id), "restoring:"+generation, 0).Err(); err != nil {
		return RestoreCheckpointResult{}, err
	}
	if err = s.store.MoveWorkspaceHead(ctx, id, cp.ID, time.Now().UTC()); err != nil {
		return RestoreCheckpointResult{}, err
	}
	if err = SyncWorkspaceRootWithOptions(ctx, s.store, id, m, SyncOptions{History: history}); err != nil {
		return RestoreCheckpointResult{}, err
	}
	if err = lock.Lost(); err != nil {
		return RestoreCheckpointResult{}, err
	}
	if err = s.completeRestoreWithActivity(ctx, id, generation, lock.Token(), cp.ID, activityBefore, m, recovering); err != nil {
		return RestoreCheckpointResult{}, err
	}
	return result, s.recordLifecycle(ctx, meta, "checkpoint", "restore", map[string]string{"checkpoint_id": cp.ID})
}

// DeleteCheckpoint removes the immutable checkpoint and its accounting refs.
// Blob bodies are retained: published live files may still reference the same
// content, and this derivative deliberately does not add a garbage collector.
func (s *Service) DeleteCheckpoint(ctx context.Context, workspace, ref string) error {
	cp, m, err := s.GetCheckpoint(ctx, workspace, ref)
	if err != nil {
		return err
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return err
	}
	id := workspaceStorageID(meta)
	keys := []string{workspaceMetaKey(id), savepointMetaKey(id, cp.ID), restoreActivityBeforeKey(id)}
	for blobID := range manifestBlobRefs(m) {
		keys = append(keys, blobRefKey(id, blobID))
	}
	err = s.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(id))
		if err != nil {
			return err
		}
		if current.HeadSavepoint == cp.ID || current.DefaultSavepoint == cp.ID {
			return fmt.Errorf("cannot delete current or default checkpoint %q", cp.ID)
		}
		baseline, err := tx.Get(ctx, restoreActivityBeforeKey(id)).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if baseline == cp.ID {
			return fmt.Errorf("cannot delete restore baseline checkpoint %q until the interrupted restore completes", cp.ID)
		}
		if n, err := tx.Exists(ctx, savepointMetaKey(id, cp.ID)).Result(); err != nil {
			return err
		} else if n == 0 {
			return os.ErrNotExist
		}
		refs := map[string]blobRef{}
		for blobID := range manifestBlobRefs(m) {
			r, err := getJSON[blobRef](ctx, tx, blobRefKey(id, blobID))
			if err != nil {
				return err
			}
			if r.RefCount > 0 {
				r.RefCount--
			}
			refs[blobID] = r
		}
		_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.Del(ctx, savepointMetaKey(id, cp.ID), savepointManifestKey(id, cp.ID))
			p.ZRem(ctx, workspaceSavepointsKey(id), cp.ID)
			for blobID, r := range refs {
				if err := setJSON(ctx, p, blobRefKey(id, blobID), r); err != nil {
					return err
				}
			}
			return nil
		})
		return err
	}, keys...)
	if err != nil {
		return err
	}
	return s.recordLifecycle(ctx, meta, "checkpoint", "delete", map[string]string{"checkpoint_id": cp.ID})
}

func (s *Service) DeleteWorkspace(ctx context.Context, workspace string) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("workspace required")
	}
	lock, err := AcquireImportLock(ctx, s.store, workspace)
	if err != nil {
		return err
	}
	defer lock.Release(context.Background())
	meta, err := s.GetWorkspace(ctx, workspace)
	if err != nil {
		return err
	}
	if err := s.store.DeleteWorkspace(ctx, workspace); err != nil {
		return err
	}
	return s.recordLifecycle(ctx, meta, "workspace", "delete", nil)
}
