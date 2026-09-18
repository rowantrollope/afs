package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

// fullReconciler walks the local tree and the live workspace root, diffs
// observed state against the persisted SyncState, and applies changes
// directly (download files to disk, upload files to Redis) without going
// through the reconciler's channels. This avoids the deadlock that occurred
// when the old implementation dispatched ops into unbuffered channels before
// the consumer goroutines were running.
type fullReconciler struct {
	r *reconciler
	// Managed workspace restore fences active mounts; it must never replace
	// their local trees, which may still contain unpublished edits.
	requireRemountOnRootReplace bool
}

func newFullReconciler(r *reconciler) *fullReconciler {
	return &fullReconciler{r: r}
}

// Save keeps the current outbound operation's context alive while stopping
// generation work. Check both before starting another action or applying an
// inbound read to the local tree.
func (f *fullReconciler) checkRunning(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-f.r.stopCh:
		return context.Canceled
	default:
		return f.r.checkLocalRoot()
	}
}

// observedMeta is what the metadata-only scan collects per path. No file
// content or hashes — those are deferred to the execution phase where they're
// actually needed (and can be parallelized).
type observedMeta struct {
	kind    string // "file" | "dir" | "symlink"
	mode    uint32
	size    int64
	mtimeMs int64
	mtimeNs int64  // local observation, used to reject changes during a scan
	target  string // symlink target (local) or readlink result (remote)
}

// syncAction is one entry in the plan the reconciler builds during the diff
// phase, then executes in parallel during the apply phase.
type syncAction struct {
	kind        string // "download" | "upload" | "mkdir-local" | "mkdir-remote" | "delete-local" | "delete-remote" | "symlink-download" | "symlink-upload"
	path        string // workspace-relative POSIX, no leading slash
	absPath     string // absolute local path
	mode        uint32
	target      string // for symlinks
	conflict    bool
	localMeta   *observedMeta
	remoteMeta  *observedMeta // carried from scan phase so exec can record mtime in state
	checkState  bool
	storedEntry SyncEntry
	hasStored   bool
}

const defaultParallelWorkers = 8

// ProgressFunc is called periodically during a full reconcile with
// (completed, total) counts. Used by the CLI to update the startup spinner.
type ProgressFunc func(done, total int64)

// run executes a single full reconciliation pass. On cold start (empty local
// folder, no persisted state) it uses the bulk materialize path, which reads
// the entire workspace in a handful of pipelined Redis calls instead of one
// LsLong per directory. Warm restarts use the metadata-diff approach to
// detect changes.
func (f *fullReconciler) run(ctx context.Context, onProgress ProgressFunc) error {
	if f.isColdStart() {
		return f.coldStart(ctx, onProgress)
	}
	return f.warmStart(ctx, onProgress)
}

// replaceFromRemote treats the live Redis root as authoritative and
// rematerializes the local sync folder from scratch. This is used for
// checkpoint restore, where the user explicitly chose to replace active state
// instead of merging local edits back up.
func (f *fullReconciler) replaceFromRemote(ctx context.Context, onProgress ProgressFunc) error {
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if f.requireRemountOnRootReplace {
		return fmt.Errorf("%w: workspace root changed; preserve local files and mount a new directory", client.ErrWorkspaceChanged)
	}
	// The filesystem client's root lookup validates managed mount generations.
	// The raw manifest bridge below intentionally bypasses that client, so do
	// this before a restore can replace any unsynchronized local contents.
	if _, err := f.r.fs.Stat(ctx, "/"); err != nil {
		return err
	}
	if f.r.store == nil || f.r.store.rdb == nil {
		return fmt.Errorf("root replace requires a store with Redis connection")
	}
	if err := ensureNoOpenHandlesUnderPath(f.r.root, os.Getpid()); err != nil {
		return err
	}

	fsKey, headSavepoint, err := f.workspaceRootRef(ctx)
	if err != nil {
		return err
	}

	m, blobs, stats, err := buildManifestFromWorkspaceRootWithProgress(ctx, f.r.store.rdb, fsKey, f.r.workspace, headSavepoint, onProgress)
	if err != nil {
		return fmt.Errorf("build manifest: %w", err)
	}
	if onProgress != nil {
		onProgress(0, int64(stats.FileCount+stats.DirCount))
	}

	var done int64
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	// A restore can fence this mount while the raw manifest snapshot loads.
	// Revalidate at the destructive boundary before touching local contents.
	if _, err := f.r.fs.Stat(ctx, "/"); err != nil {
		return err
	}
	if _, err := materializeManifestToDirectory(f.r.root, m, func(blobID string) ([]byte, error) {
		data, ok := blobs[blobID]
		if !ok {
			return nil, fmt.Errorf("blob %q missing during root replace", blobID)
		}
		return data, nil
	}, manifestMaterializeOptions{
		preserveMetadata: true,
		readonlyFiles:    f.r.readonly,
		onProgress: func(p importStats) {
			done = int64(p.Files + p.Dirs + p.Symlinks)
			if onProgress != nil {
				onProgress(done, int64(stats.FileCount+stats.DirCount))
			}
		},
	}); err != nil {
		return fmt.Errorf("materialize: %w", err)
	}

	if err := f.replaceStateFromManifest(m, blobs); err != nil {
		return err
	}
	f.r.state.markDirty()
	return nil
}

func (f *fullReconciler) replaceStateFromManifest(m manifest, blobs map[string][]byte) error {
	now := time.Now().UTC()
	next := make(map[string]SyncEntry, len(m.Entries))
	for manifestPath, entry := range m.Entries {
		rel := strings.TrimPrefix(manifestPath, "/")
		if rel == "" {
			continue
		}
		syncEntry := SyncEntry{
			Type:          entry.Type,
			Mode:          entry.Mode,
			Size:          entry.Size,
			RemoteMtimeMs: entry.MtimeMs,
			LastSyncedAt:  now,
		}
		switch entry.Type {
		case "file":
			data, err := manifestEntryData(entry, func(blobID string) ([]byte, error) {
				data, ok := blobs[blobID]
				if !ok {
					return nil, fmt.Errorf("blob %q missing while rebuilding sync state", blobID)
				}
				return data, nil
			})
			if err != nil {
				return err
			}
			hash := sha256Hex(data)
			syncEntry.LocalHash = hash
			syncEntry.RemoteHash = hash
			if fi, statErr := os.Stat(filepath.Join(f.r.root, filepath.FromSlash(rel))); statErr == nil {
				syncEntry.LocalMtimeMs = fi.ModTime().UnixMilli()
			}
		case "symlink":
			syncEntry.Target = entry.Target
		}
		next[rel] = syncEntry
	}

	f.r.state.mu.Lock()
	f.r.state.state.Entries = make(map[string]SyncEntry, len(next))
	for rel, entry := range next {
		entry.Version = f.r.state.nextVersion()
		f.r.state.state.Entries[rel] = entry
	}
	f.r.state.dirty = true
	f.r.state.mu.Unlock()
	return nil
}

func (f *fullReconciler) workspaceRootRef(ctx context.Context) (string, string, error) {
	storageID := strings.TrimSpace(f.r.storageID)
	if storageID != "" {
		head, err := f.workspaceRootHead(ctx, storageID, f.r.headCheckpoint)
		if err != nil {
			return "", "", fmt.Errorf("get workspace root head: %w", err)
		}
		return storageID, head, nil
	}

	meta, err := f.r.store.getWorkspaceMeta(ctx, f.r.workspace)
	if err != nil {
		return "", "", fmt.Errorf("get workspace meta: %w", err)
	}
	return controlplane.WorkspaceStorageID(meta), strings.TrimSpace(meta.HeadSavepoint), nil
}

func (f *fullReconciler) workspaceRootHead(ctx context.Context, storageID, fallback string) (string, error) {
	fallback = strings.TrimSpace(fallback)
	if f.r.store == nil || f.r.store.rdb == nil {
		return fallback, nil
	}
	head, err := f.r.store.rdb.Get(ctx, workspaceRootHeadSavepointKey(storageID)).Result()
	if err == nil {
		if head = strings.TrimSpace(head); head != "" {
			return head, nil
		}
		return fallback, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return "", err
}

func workspaceRootHeadSavepointKey(storageID string) string {
	return "afs:{" + controlplane.WorkspaceFSKey(strings.TrimSpace(storageID)) + "}:root_head_savepoint"
}

// isColdStart returns true when the local folder is empty (or missing) and
// there is no persisted SyncState. This means we can skip the diff entirely
// and just pull everything from Redis in bulk.
func (f *fullReconciler) isColdStart() bool {
	f.r.state.mu.Lock()
	entryCount := len(f.r.state.state.Entries)
	f.r.state.mu.Unlock()
	if entryCount > 0 {
		return false
	}
	populated, err := f.hasNonControlLocalEntries()
	return err == nil && !populated
}

// Bulk materialization replaces local contents, including ignored files. Only
// the control directory is preserved by that path; any other local entry must
// use ordinary warm reconciliation, which leaves ignored contents intact.
func (f *fullReconciler) hasNonControlLocalEntries() (bool, error) {
	entries, err := os.ReadDir(f.r.root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != syncControlDirName {
			return true, nil
		}
	}
	return false, nil
}

// coldStart pulls the entire workspace from Redis using the bulk manifest
// path (buildManifestFromWorkspaceRoot + materializeManifestToDirectory).
// It reads the full tree in a handful of pipelined HMGet/HGetAll calls —
// dramatically faster than one LsLong per directory over WAN.
func (f *fullReconciler) coldStart(ctx context.Context, onProgress ProgressFunc) error {
	if _, err := f.r.fs.Stat(ctx, "/"); err != nil {
		return err
	}
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if f.r.store == nil || f.r.store.rdb == nil {
		return fmt.Errorf("cold start requires a store with Redis connection")
	}

	fsKey, headSavepoint, err := f.workspaceRootRef(ctx)
	if err != nil {
		return err
	}
	// Every Redis key for this workspace is hash-tagged by the workspace's
	// storage ID, which may differ from the user-facing name in cloud /
	// multi-tenant deployments (e.g. name "getting-started" → storage ID
	// "ws_f6214eecf58fe5e6"). Using the resolved ID here ensures we read the
	// actual tree instead of an empty phantom key derived from the name.

	m, blobs, stats, err := buildManifestFromWorkspaceRootWithProgress(ctx, f.r.store.rdb, fsKey, f.r.workspace, headSavepoint, onProgress)
	if err != nil {
		return fmt.Errorf("build manifest: %w", err)
	}

	if onProgress != nil {
		onProgress(0, int64(stats.FileCount+stats.DirCount))
	}

	var done int64
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if populated, err := f.hasNonControlLocalEntries(); err != nil {
		return err
	} else if populated {
		return errors.New("local files appeared during initial download; preserve them and retry mounting")
	}
	if _, err := f.r.fs.Stat(ctx, "/"); err != nil {
		return err
	}
	matStats, err := materializeManifestToDirectory(f.r.root, m, func(blobID string) ([]byte, error) {
		data, ok := blobs[blobID]
		if !ok {
			return nil, fmt.Errorf("blob %q missing during cold start materialize", blobID)
		}
		return data, nil
	}, manifestMaterializeOptions{
		preserveMetadata: true,
		readonlyFiles:    f.r.readonly,
		onProgress: func(p importStats) {
			done = int64(p.Files + p.Dirs + p.Symlinks)
			if onProgress != nil {
				onProgress(done, int64(stats.FileCount+stats.DirCount))
			}
		},
	})
	if err != nil {
		return fmt.Errorf("materialize: %w", err)
	}

	// Build SyncState from the materialized manifest so warm restarts can
	// diff against it without re-reading content.
	now := time.Now().UTC()
	f.r.state.mu.Lock()
	for path, entry := range m.Entries {
		rel := strings.TrimPrefix(path, "/")
		if rel == "" {
			continue // skip root dir entry
		}
		se := SyncEntry{
			Type:          entry.Type,
			Mode:          entry.Mode,
			Size:          entry.Size,
			RemoteMtimeMs: entry.MtimeMs,
			LastSyncedAt:  now,
		}
		switch entry.Type {
		case "file":
			// Compute hash from the inline/blob content so warm restart can compare.
			data, _ := manifestEntryData(entry, func(blobID string) ([]byte, error) {
				d, ok := blobs[blobID]
				if !ok {
					return nil, fmt.Errorf("blob %q missing", blobID)
				}
				return d, nil
			})
			if data != nil {
				se.LocalHash = sha256Hex(data)
				se.RemoteHash = se.LocalHash
			}
			// Get local mtime from the file we just wrote.
			abs := filepath.Join(f.r.root, filepath.FromSlash(rel))
			if fi, statErr := os.Stat(abs); statErr == nil {
				se.LocalMtimeMs = fi.ModTime().UnixMilli()
				se.Mode = uint32(fi.Mode().Perm())
			}
		case "symlink":
			se.Target = entry.Target
		}
		f.r.state.state.Entries[rel] = se
	}
	f.r.state.dirty = true
	f.r.state.mu.Unlock()
	f.r.state.markDirty()

	_ = matStats // used via the progress callback
	return nil
}

// warmStart diffs local vs remote metadata and syncs only what changed.
func (f *fullReconciler) warmStart(ctx context.Context, onProgress ProgressFunc) error {
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if _, err := f.r.fs.Stat(ctx, "/"); err != nil {
		return err
	}
	if err := f.r.echo.retryDirectoryModes(f.r.root); err != nil {
		return err
	}
	local, err := f.scanLocalMeta()
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	// Detect files that were deleted locally while the daemon was offline.
	// These didn't produce tombstones (daemon wasn't running), so we stamp
	// them now so buildPlan's tombstone logic handles them correctly.
	f.detectOfflineDeletes(local)

	// Flush the client's attribute/listing cache so the remote scan always
	// hits Redis. Without this, recently-created files that haven't been
	// reflected in the cached directory listing would be missed, causing
	// buildPlan to falsely classify them as remote-deleted.
	f.r.fs.InvalidateCache()
	remote, err := f.scanRemoteMeta(ctx, onProgress)
	if err != nil {
		return fmt.Errorf("scan remote: %w", err)
	}
	if err := f.checkRunning(ctx); err != nil {
		return err
	}

	plan := f.buildPlan(ctx, local, remote)
	if len(plan) == 0 {
		f.cleanupTombstones()
		return nil
	}
	err = f.executePlan(ctx, plan, onProgress)
	f.cleanupTombstones()
	return err
}

// detectOfflineDeletes stamps tombstones on state entries whose files are
// missing locally. These are files deleted while the daemon was offline
// (no watcher → no tombstone written at delete time). Must run before
// buildPlan so the tombstone logic propagates offline deletes to remote.
func (f *fullReconciler) detectOfflineDeletes(local map[string]observedMeta) {
	f.r.state.mu.Lock()
	defer f.r.state.mu.Unlock()
	now := time.Now().UTC()
	for path, entry := range f.r.state.state.Entries {
		if f.r.readonly {
			if entry.Deleted {
				entry.Deleted = false
				entry.Version = f.r.state.nextVersion()
				f.r.state.state.Entries[path] = entry
				f.r.state.dirty = true
			}
			continue
		}
		if entry.Deleted {
			continue
		}
		if _, exists := local[path]; !exists {
			// A background download or application may have created the path
			// after the local walk passed its parent.
			if _, err := os.Lstat(filepath.Join(f.r.root, filepath.FromSlash(path))); !os.IsNotExist(err) {
				continue
			}
			f.r.log.Info(fmt.Sprintf("detectOfflineDeletes %s: in state but missing locally -> tombstone", path))
			entry.Deleted = true
			entry.Version = f.r.state.nextVersion()
			entry.LastSyncedAt = now
			f.r.state.state.Entries[path] = entry
			f.r.state.dirty = true
		}
	}
}

// scanLocalMeta walks the local tree collecting only stat information —
// no ReadFile, no hashing. This is O(syscalls) not O(bytes).
func (f *fullReconciler) scanLocalMeta() (map[string]observedMeta, error) {
	if err := f.r.checkLocalRoot(); err != nil {
		return nil, err
	}
	out := make(map[string]observedMeta)
	root := f.r.root
	before, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("local sync root: %w", err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("local sync root %s is not a directory", root)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p != root && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if f.r.ignore.shouldIgnoreEntry(p, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return nil
			}
			out[rel] = observedMeta{kind: "symlink", target: target, mtimeMs: info.ModTime().UnixMilli()}
			return nil
		}
		if d.IsDir() {
			out[rel] = observedMeta{kind: "dir", mode: uint32(info.Mode() & fs.ModePerm), mtimeMs: info.ModTime().UnixMilli()}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		out[rel] = observedMeta{
			kind:    "file",
			mode:    uint32(info.Mode() & fs.ModePerm),
			size:    info.Size(),
			mtimeMs: info.ModTime().UnixMilli(),
			mtimeNs: info.ModTime().UnixNano(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A root detached or replaced while walking is not evidence that every
	// tracked descendant was intentionally deleted. Retain the baseline.
	after, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("local sync root after scan: %w", err)
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("local sync root changed during scan")
	}
	return out, err
}

// scanRemoteMeta walks the live workspace root using LsLong only — no Cat().
// This is one LsLong RPC per directory, proportional to directory count not
// file count. For symlinks we also call Readlink (one extra RPC per symlink).
// No timeout — large workspaces (45 GB+) can have thousands of directories
// and the walk legitimately takes minutes on WAN. The parent context handles
// cancellation (Ctrl-C).
func (f *fullReconciler) scanRemoteMeta(ctx context.Context, onProgress ProgressFunc) (map[string]observedMeta, error) {
	out := make(map[string]observedMeta)
	var scanned int64
	report := func() {
		scanned++
		if onProgress != nil {
			onProgress(scanned, -1) // -1 = total unknown during scan
		}
	}
	if err := f.scanRemoteDirMeta(ctx, "/", out, report); err != nil {
		if isClientNotFound(err) {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

func (f *fullReconciler) scanRemoteDirMeta(ctx context.Context, dir string, out map[string]observedMeta, onEntry func()) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	entries, err := f.r.fs.LsLong(ctx, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := joinRemote(dir, e.Name)
		rel := strings.TrimPrefix(full, "/")
		if f.r.ignore.shouldIgnore(rel, e.Type == "dir") {
			continue
		}
		if onEntry != nil {
			onEntry()
		}
		switch e.Type {
		case "dir":
			out[rel] = observedMeta{kind: "dir", mode: e.Mode, mtimeMs: e.Mtime}
			if err := f.scanRemoteDirMeta(ctx, full, out, onEntry); err != nil {
				return err
			}
		case "symlink":
			target, err := f.r.fs.Readlink(ctx, full)
			if err != nil {
				continue
			}
			out[rel] = observedMeta{kind: "symlink", mode: e.Mode, size: e.Size, target: target, mtimeMs: e.Mtime}
		case "file":
			out[rel] = observedMeta{kind: "file", mode: e.Mode, size: e.Size, mtimeMs: e.Mtime}
		}
	}
	return nil
}

// buildPlan diffs local vs remote vs persisted state and produces a list of
// actions. The ctx is used for remote Stat calls in the lok && !rok case.
func (f *fullReconciler) buildPlan(ctx context.Context, local, remote map[string]observedMeta) []syncAction {
	baseline := f.r.state.snapshot().Entries
	all := make(map[string]struct{}, len(local)+len(remote))
	for k := range local {
		all[k] = struct{}{}
	}
	for k := range remote {
		all[k] = struct{}{}
	}

	// Sort directories before files so mkdir actions run first in the
	// parallel phase (the worker pool creates parents before writing children).
	var plan []syncAction
	for path := range all {
		if f.r.deferScanForPendingUpload(path) {
			continue
		}
		l, lok := local[path]
		r, rok := remote[path]

		stored, hasStored := baseline[path]

		abs := filepath.Join(f.r.root, filepath.FromSlash(path))

		switch {
		case lok && !rok:
			if hasStored && stored.Deleted {
				f.r.log.Info(fmt.Sprintf("buildPlan %s: lok && !rok, tombstone -> re-upload", path))
				plan = append(plan, f.planUpload(path, abs, l, stored, hasStored)...)
			} else if hasStored && !stored.Deleted {
				// File was synced, now missing from remote. Verify with fresh Stat.
				stat, statErr := f.r.fs.Stat(ctx, absoluteRemotePath(path))
				if statErr == nil && stat != nil {
					f.r.log.Info(fmt.Sprintf("buildPlan %s: lok && !rok, hasStored, Stat found -> skip scan race", path))
					continue // Remote still has it — scan race, skip
				}
				if statErr != nil && !isClientNotFound(statErr) {
					f.r.requestFullSweep()
					continue
				}
				f.r.log.Info(fmt.Sprintf("buildPlan %s: lok && !rok, hasStored, Stat nil (err=%v) -> delete-local", path, statErr))
				plan = append(plan, syncAction{kind: "delete-local", path: path, absPath: abs})
			} else {
				f.r.log.Info(fmt.Sprintf("buildPlan %s: lok && !rok, !hasStored -> upload", path))
				plan = append(plan, f.planUpload(path, abs, l, stored, hasStored)...)
			}
		case !lok && rok:
			if hasStored && stored.Deleted {
				hasLiveRemoteDescendants := false
				if r.kind == "dir" {
					f.r.state.mu.Lock()
					hasLiveRemoteDescendants = mountRemoteDirHasLiveDescendants(path, remote, f.r.state.state.Entries)
					f.r.state.mu.Unlock()
				}
				if hasLiveRemoteDescendants {
					f.r.log.Info(fmt.Sprintf("buildPlan %s: !lok && rok, tombstoned dir has live remote descendants -> download", path))
					plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, false))
					continue
				}
				// Tombstone: user intentionally deleted → propagate to remote.
				f.r.log.Info(fmt.Sprintf("buildPlan %s: !lok && rok, tombstone -> delete-remote", path))
				plan = append(plan, syncAction{kind: "delete-remote", path: path, absPath: abs})
			} else {
				// File missing locally (download in-flight, restart, crash, etc.)
				// but present in remote. Re-download. Only tombstones propagate
				// deletes — absence without a tombstone is never treated as an
				// intentional delete.
				f.r.log.Info(fmt.Sprintf("buildPlan %s: !lok && rok, no tombstone -> download", path))
				plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, false))
			}
		case lok && rok:
			comparisonRemote := r
			if f.r.readonly && comparisonRemote.kind == "file" {
				comparisonRemote.mode &^= 0o222
			}
			// Both present. Check if they match using metadata (size+mtime
			// for files, target for symlinks). Only go deeper if they differ.
			if metaMatch(l, comparisonRemote, stored, hasStored) {
				f.refreshStateMeta(path, l, r, stored, hasStored)
				continue
			}
			if hasStored && !stored.Deleted {
				localChanged := observedChangedFromStored(l, stored, true) || l.mode != stored.Mode
				remoteChanged := observedChangedFromStored(comparisonRemote, stored, false) || comparisonRemote.mode != stored.Mode
				switch {
				case localChanged && !remoteChanged:
					if f.r.readonly {
						plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, true))
					} else {
						plan = append(plan, f.planUpload(path, abs, l, stored, hasStored)...)
					}
				case !localChanged && remoteChanged:
					plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, false))
				case localChanged && remoteChanged:
					plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, true))
				default:
					f.refreshStateMeta(path, l, r, stored, hasStored)
				}
				continue
			}
			plan = append(plan, f.planDownload(path, abs, r, stored, hasStored, true))
		}
	}
	for i := range plan {
		a := &plan[i]
		a.checkState = true
		a.storedEntry, a.hasStored = baseline[a.path]
		if l, exists := local[a.path]; exists {
			a.localMeta = &l
		}
		if r, exists := remote[a.path]; exists {
			a.remoteMeta = &r
		}
	}
	return plan
}

func (f *fullReconciler) planUpload(path, abs string, l observedMeta, stored SyncEntry, hasStored bool) []syncAction {
	if f.r.readonly {
		return nil
	}
	switch l.kind {
	case "dir":
		return []syncAction{{kind: "mkdir-remote", path: path, absPath: abs, mode: l.mode}}
	case "symlink":
		return []syncAction{{kind: "symlink-upload", path: path, absPath: abs, target: l.target}}
	case "file":
		return []syncAction{{kind: "upload", path: path, absPath: abs, mode: l.mode, localMeta: &l}}
	}
	return nil
}

func (f *fullReconciler) planDownload(path, abs string, r observedMeta, stored SyncEntry, hasStored bool, conflict bool) syncAction {
	rm := r // copy so we can take address
	switch r.kind {
	case "dir":
		return syncAction{kind: "mkdir-local", path: path, absPath: abs, mode: r.mode, remoteMeta: &rm}
	case "symlink":
		return syncAction{kind: "symlink-download", path: path, absPath: abs, target: r.target, conflict: conflict, remoteMeta: &rm}
	default: // file
		return syncAction{kind: "download", path: path, absPath: abs, mode: r.mode, conflict: conflict, remoteMeta: &rm}
	}
}

// metaMatch decides whether local and remote are equivalent using only
// metadata (no content read). For files we compare size and check if both
// sides match the stored state. Equal local and remote timestamps alone do not
// establish a shared version: an old download and a newer same-size remote
// write can land in the same millisecond.
func metaMatch(l, r observedMeta, stored SyncEntry, hasStored bool) bool {
	if l.kind != r.kind {
		return false
	}
	switch l.kind {
	case "dir":
		return l.mode == r.mode
	case "symlink":
		return l.target == r.target
	case "file":
		if l.size != r.size || l.mode != r.mode {
			return false
		}
		// If we have stored state and both sides match it, they're in sync.
		if hasStored && stored.Size == l.size {
			if l.mtimeMs == stored.LocalMtimeMs && r.mtimeMs == stored.RemoteMtimeMs {
				return true
			}
		}
		return false
	}
	return false
}

// executePlan runs the planned actions with a bounded worker pool.
func (f *fullReconciler) executePlan(ctx context.Context, plan []syncAction, onProgress ProgressFunc) (result error) {
	directoryModes := newSyncDirectoryModes(f.r)
	defer func() {
		result = errors.Join(result, directoryModes.restore())
	}()
	// Separate actions whose ordering matters from the parallel file ops.
	// Dirs must happen first so parent directories exist before child writes.
	// Deletes run deepest-path-first so non-empty remote directories are
	// emptied before their parent directory is removed.
	var dirActions, deleteActions, fileActions []syncAction
	for _, a := range plan {
		switch a.kind {
		case "mkdir-local", "mkdir-remote":
			dirActions = append(dirActions, a)
		case "delete-local", "delete-remote":
			deleteActions = append(deleteActions, a)
		default:
			fileActions = append(fileActions, a)
		}
	}
	sort.SliceStable(dirActions, func(i, j int) bool {
		leftDepth := strings.Count(dirActions[i].path, "/")
		rightDepth := strings.Count(dirActions[j].path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return dirActions[i].path < dirActions[j].path
	})
	sort.SliceStable(deleteActions, func(i, j int) bool {
		leftDepth := strings.Count(deleteActions[i].path, "/")
		rightDepth := strings.Count(deleteActions[j].path, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return deleteActions[i].path > deleteActions[j].path
	})

	total := int64(len(plan))
	var done atomic.Int64

	report := func() {
		if onProgress != nil {
			onProgress(done.Load(), total)
		}
	}

	// Phase 1: directories (serial, fast).
	for _, a := range dirActions {
		if err := f.checkRunning(ctx); err != nil {
			return err
		}
		if a.kind == "mkdir-local" {
			if err := directoryModes.prepare(filepath.Dir(a.absPath)); err != nil {
				return err
			}
		}
		if err := f.executeAction(ctx, a); err != nil {
			return err
		}
		if a.kind == "mkdir-local" {
			if err := directoryModes.prepare(a.absPath); err != nil {
				return err
			}
		}
		done.Add(1)
		report()
	}

	// Existing directories may already have their final restrictive modes
	// and therefore have no mkdir action. Reopen parents before local writes
	// or deletes; restore only after all parallel workers have joined.
	for _, a := range plan {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch a.kind {
		case "download", "symlink-download", "delete-local":
			if err := directoryModes.prepare(filepath.Dir(a.absPath)); err != nil {
				return err
			}
		}
	}

	// Phase 2: ordered deletes (serial, dependency-sensitive).
	for _, a := range deleteActions {
		if err := f.checkRunning(ctx); err != nil {
			return err
		}
		if err := f.executeAction(ctx, a); err != nil {
			return err
		}
		done.Add(1)
		report()
	}

	// Phase 3: files + symlinks (parallel).
	sem := make(chan struct{}, defaultParallelWorkers)
	var mu sync.Mutex
	var firstErr error

	var wg sync.WaitGroup
dispatch:
	for _, a := range fileActions {
		if f.checkRunning(ctx) != nil {
			break
		}
		mu.Lock()
		if firstErr != nil {
			mu.Unlock()
			break
		}
		mu.Unlock()

		select {
		case <-ctx.Done():
			break dispatch
		case <-f.r.stopCh:
			break dispatch
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(action syncAction) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := f.executeAction(ctx, action); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
			done.Add(1)
			report()
		}(a)
	}
	wg.Wait()
	f.r.state.markDirty()
	if firstErr == nil {
		return f.checkRunning(ctx)
	}
	return firstErr
}

// executeAction applies one planned action directly (no channel dispatch).
func (f *fullReconciler) executeAction(ctx context.Context, a syncAction) error {
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if !f.actionStillCurrent(a) {
		return nil
	}
	if f.r.readonly {
		switch a.kind {
		case "mkdir-remote", "upload", "symlink-upload", "delete-remote":
			return errors.New("sync mount is read-only")
		}
	}
	switch a.kind {
	case "mkdir-local":
		return f.execMkdirLocal(a)
	case "mkdir-remote":
		return f.execMkdirRemote(ctx, a)
	case "download":
		return f.execDownload(ctx, a)
	case "upload":
		return f.execUpload(ctx, a)
	case "symlink-download":
		return f.execSymlinkDownload(ctx, a)
	case "symlink-upload":
		return f.execSymlinkUpload(ctx, a)
	case "delete-remote":
		return f.execDeleteRemote(ctx, a)
	case "delete-local":
		return f.execDeleteLocal(ctx, a)
	default:
		return fmt.Errorf("unknown action kind: %s", a.kind)
	}
}

func (f *fullReconciler) execMkdirLocal(a syncAction) error {
	if err := os.MkdirAll(a.absPath, 0o755); err != nil {
		return err
	}
	if a.mode != 0 {
		if err := os.Chmod(a.absPath, fs.FileMode(a.mode)); err != nil {
			return err
		}
	}
	info, err := os.Lstat(a.absPath)
	if err != nil {
		return err
	}
	f.r.echo.markDir(a.path, a.mode, info)
	f.updateActionState(a, SyncEntry{
		Type:         "dir",
		Mode:         a.mode,
		LastSyncedAt: time.Now().UTC(),
	})
	return nil
}

func (f *fullReconciler) execMkdirRemote(ctx context.Context, a syncAction) error {
	remotePath := absoluteRemotePath(a.path)
	stat, current, err := f.remoteStatStillCurrent(ctx, a)
	if err != nil || !current {
		return err
	}
	if stat == nil {
		// Publish the requested mode with creation. A peer that creates the
		// directory after our observation keeps its own permissions.
		err = f.r.fs.MkdirMode(ctx, remotePath, a.mode)
		if isClientAlreadyExists(err) {
			err = nil
		}
	} else {
		// An intentional local chmod may only change the observed revision.
		err = f.r.fs.Chmod(client.WithExpectedStat(ctx, stat), remotePath, a.mode)
	}
	if err != nil {
		if errors.Is(err, client.ErrWriteConflict) {
			f.r.requestFullSweep()
		}
		return fmt.Errorf("publish remote directory %s: %w", a.path, err)
	}
	f.updateActionState(a, SyncEntry{
		Type:         "dir",
		Mode:         a.mode,
		LastSyncedAt: time.Now().UTC(),
	})
	return nil
}

func (f *fullReconciler) execDownload(ctx context.Context, a syncAction) error {
	// Check for tombstone right before downloading — if the user deleted
	// this file locally and the upload hasn't completed yet, skip the
	// download so we don't reverse their delete.
	f.r.state.mu.Lock()
	if entry, ok := f.r.state.state.Entries[a.path]; ok && entry.Deleted {
		f.r.state.mu.Unlock()
		return nil
	}
	f.r.state.mu.Unlock()

	remotePath := absoluteRemotePath(a.path)
	// Use a per-file timeout to prevent a single slow Redis call from
	// blocking the entire cold start. 30s is generous for any individual
	// file (even multi-MB on WAN).
	catCtx, catCancel := context.WithTimeout(ctx, 30*time.Second)
	defer catCancel()
	data, err := f.r.fs.Cat(catCtx, remotePath)
	if err != nil {
		if isClientNotFound(err) {
			return nil // vanished between scan and download
		}
		return fmt.Errorf("download %s: %w", a.path, err)
	}
	hash := sha256Hex(data)
	if err := f.checkRunning(ctx); err != nil {
		return err
	}

	if !f.actionStillCurrent(a) {
		return nil
	}
	mode := a.mode
	if mode == 0 && !f.r.readonly {
		mode = 0o644
	}
	if f.r.readonly {
		mode &^= 0o222
	}
	localData, localErr := os.ReadFile(a.absPath)
	localInfo, statErr := os.Lstat(a.absPath)
	identicalContent := localErr == nil && statErr == nil && localInfo.Mode().IsRegular() && sha256Hex(localData) == hash
	identical := identicalContent && uint32(localInfo.Mode().Perm()) == mode
	// Removing write bits for a read-only mount does not create a local
	// conflict. Preserve other mode differences, including equal-byte edits.
	if identical || (identicalContent && f.r.readonly && uint32(localInfo.Mode().Perm()) == a.mode) {
		a.conflict = false
	}
	if !identical {
		conflictPath, err := f.stageDownload(ctx, a, mode, func(file *os.File) error {
			_, err := file.Write(data)
			return err
		})
		if conflictPath != "" || errors.Is(err, errSyncDownloadLocalChanged) {
			// Startup can run before the watcher. Preserve both staged edits
			// and newly created conflict copies through another reconciliation.
			f.r.requestFullSweep()
		}
		if errors.Is(err, errSyncDownloadLocalChanged) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", a.path, err)
		}
		f.r.echo.markFile(a.path, hash)
	}

	// Record both mtimes so the next startup's metaMatch can skip unchanged
	// files without re-reading content. Local mtime comes from the file we
	// just wrote; remote mtime comes from the scan phase.
	var localMtimeMs, remoteMtimeMs int64
	if fi, err := os.Stat(a.absPath); err == nil {
		localMtimeMs = fi.ModTime().UnixMilli()
	}
	if a.remoteMeta != nil {
		remoteMtimeMs = a.remoteMeta.mtimeMs
	}
	f.updateActionState(a, SyncEntry{
		Type:          "file",
		Mode:          mode,
		Size:          int64(len(data)),
		LocalHash:     hash,
		RemoteHash:    hash,
		LocalMtimeMs:  localMtimeMs,
		RemoteMtimeMs: remoteMtimeMs,
		LastSyncedAt:  time.Now().UTC(),
	})
	return nil
}

// Use the event downloader's staging and destination-identity checks during
// full recovery as well. Its final guard must also validate this scan's action:
// a state update during disk I/O can supersede an otherwise unchanged file.
func (f *fullReconciler) stageDownload(ctx context.Context, a syncAction, mode uint32, write func(*os.File) error) (string, error) {
	d := newDownloader(f.r.fs, nil, f.r.root, f.r.conflict, f.r.echo, f.r.readonly, f.r.log)
	d.rootIdentity = f.r.rootIdentity
	return d.writeLocalFile(ctx, downloadOp{Path: a.path, AbsPath: a.absPath, Conflict: a.conflict}, mode, write, func() error {
		if err := f.checkRunning(ctx); err != nil {
			return err
		}
		if !f.actionStillCurrent(a) {
			return errSyncDownloadLocalChanged
		}
		return nil
	})
}

func (f *fullReconciler) execDeleteLocal(ctx context.Context, a syncAction) error {
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	f.r.log.Info(fmt.Sprintf("execDeleteLocal %s", a.path))
	conflictPath, skipped, err := applyConfirmedSyncRemoteDelete(ctx, f.r.fs, f.r.root, a.absPath, a.storedEntry, a.hasStored, f.r.state, f.r.conflict, f.r.echo, func() error { return f.checkRunning(ctx) })
	if conflictPath != "" {
		f.r.log.Conflict(a.path, conflictPath)
	}
	if skipped || conflictPath != "" {
		f.r.requestFullSweep()
	}
	return err
}

func (f *fullReconciler) execDeleteRemote(ctx context.Context, a syncAction) error {
	if err := f.r.checkLocalRoot(); err != nil {
		return err
	}
	f.r.log.Info(fmt.Sprintf("execDeleteRemote %s", a.path))
	remotePath := absoluteRemotePath(a.path)
	stat, current, err := f.remoteStatStillCurrent(ctx, a)
	if err != nil || !current {
		return err
	}
	matches, err := syncRemoteMatchesStored(ctx, f.r.fs, remotePath, stat, a.storedEntry, a.hasStored)
	if err != nil {
		return err
	}
	if !matches {
		f.r.state.mu.Lock()
		entry, exists := f.r.state.state.Entries[a.path]
		if exists && entry.Deleted {
			entry.Deleted = false
			entry.Version = f.r.state.nextVersion()
			f.r.state.state.Entries[a.path] = entry
			f.r.state.dirty = true
		}
		f.r.state.mu.Unlock()
		// Download immediately: another warm scan would interpret the same
		// local absence as a fresh offline deletion before this candidate lands.
		remote := observedMeta{kind: stat.Type, mode: stat.Mode, size: stat.Size, mtimeMs: stat.Mtime}
		download := f.planDownload(a.path, a.absPath, remote, entry, exists, false)
		download.checkState, download.storedEntry, download.hasStored = true, entry, exists
		return f.executeAction(ctx, download)
	}
	if err := f.r.fs.Rm(client.WithExpectedStat(ctx, stat), remotePath); err != nil && !isClientNotFound(err) && !errors.Is(err, redis.Nil) {
		if errors.Is(err, client.ErrWriteConflict) {
			f.r.requestFullSweep()
		}
		return fmt.Errorf("rm remote %s: %w", a.path, err)
	}
	f.r.state.mu.Lock()
	if entry, ok := f.r.state.state.Entries[a.path]; ok && entry.Deleted &&
		(!a.checkState || entry.Version == a.storedEntry.Version) {
		delete(f.r.state.state.Entries, a.path)
		f.r.state.dirty = true
	}
	f.r.state.mu.Unlock()
	f.r.requestFullSweep()
	return nil
}

func (f *fullReconciler) execUpload(ctx context.Context, a syncAction) error {
	if f.r.readonly {
		// Read-only mounts must never push local changes back to the workspace.
		// The mount reconcile planner already downgrades imports/uploads to
		// "skipped" so users see this in the plan; this is the runtime guard
		// for any path that schedules an upload directly.
		fmt.Fprintf(os.Stderr, "afs sync: skipping upload of %s — mount is read-only\n", a.path)
		return nil
	}
	localInfo, err := os.Lstat(a.absPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(a.absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if int64(len(data)) > f.r.maxFileBytes {
		fmt.Fprintf(os.Stderr, "afs sync: skipping %s — %d bytes exceeds %d byte cap\n", a.path, len(data), f.r.maxFileBytes)
		return nil
	}
	hash := sha256Hex(data)
	if !f.actionStillCurrent(a) {
		return nil
	}
	remotePath := absoluteRemotePath(a.path)
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	expectedStat, current, err := f.remoteStatStillCurrent(ctx, a)
	if err != nil || !current {
		return err
	}
	var chunkSize int
	var chunkHashes []string
	publishCtx := client.WithExpectedStat(ctx, expectedStat)
	if len(data) > f.r.chunkThreshold {
		// Recovery can discover a new file before the watcher does. Publish
		// the same chunk manifest as the event uploader so later edits retain
		// delta uploads regardless of which path won that race.
		chunkSize = f.r.chunkSize
		chunkHashes = uploadChunkHashes(data, chunkSize)
		chunks := make(map[int][]byte, len(chunkHashes))
		for i := range chunkHashes {
			start := i * chunkSize
			chunks[i] = data[start:min(start+chunkSize, len(data))]
		}
		err = f.r.fs.WriteChunks(publishCtx, remotePath, chunks, chunkSize, int64(len(data)), chunkHashes)
		hash = compositeHash(chunkHashes)
	} else {
		err = f.r.fs.Echo(publishCtx, remotePath, data)
	}
	if err != nil {
		if errors.Is(err, client.ErrWriteConflict) {
			f.r.requestFullSweep()
		}
		return fmt.Errorf("upload %s: %w", a.path, err)
	}
	mode := a.mode
	if mode == 0 {
		mode = 0o644
	}
	_ = f.r.fs.Chmod(ctx, remotePath, mode)
	remoteStat, err := f.r.fs.Stat(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("stat uploaded %s: %w", a.path, err)
	}
	if remoteStat == nil {
		return fmt.Errorf("uploaded file %s is missing remotely", a.path)
	}

	f.updateActionState(a, SyncEntry{
		Type:          "file",
		Mode:          mode,
		Size:          int64(len(data)),
		LocalHash:     hash,
		RemoteHash:    hash,
		LocalMtimeMs:  localInfo.ModTime().UnixMilli(),
		RemoteMtimeMs: remoteStat.Mtime,
		LastSyncedAt:  time.Now().UTC(),
		ChunkSize:     chunkSize,
		ChunkHashes:   chunkHashes,
	})
	return nil
}

func (f *fullReconciler) execSymlinkDownload(ctx context.Context, a syncAction) error {
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	// Reuse the event downloader's staged replacement and conflict copy. A
	// warm restart must preserve the competing local target just as live sync
	// does, including edits made after the remote scan.
	results := make(chan downloadResult, 1)
	d := newDownloader(f.r.fs, results, f.r.root, f.r.conflict, f.r.echo, f.r.readonly, f.r.log)
	d.rootIdentity = f.r.rootIdentity
	downloadHasStored := a.hasStored
	if a.checkState && a.localMeta == nil {
		// The checked recovery plan already expects this path to be absent,
		// including after rejecting a local deletion of a changed remote
		// target. Keep the original baseline only for state publication.
		downloadHasStored = false
	}
	d.processSymlink(ctx, downloadOp{Kind: opDownloadSymlink, Path: a.path, AbsPath: a.absPath,
		Symlink: a.target, StoredEntry: a.storedEntry, HasStored: downloadHasStored, Conflict: a.conflict})
	res := <-results
	if res.Err != nil {
		return res.Err
	}
	if res.Skipped {
		f.r.requestFullSweep()
		return nil
	}
	if res.ConflictPath != "" {
		f.r.log.Conflict(a.path, res.ConflictPath)
		f.r.requestFullSweep()
	}
	f.updateActionState(a, SyncEntry{
		Type:          "symlink",
		Target:        res.Target,
		LocalIdentity: localFileIdentityFromPath(a.absPath),
		LastSyncedAt:  time.Now().UTC(),
	})
	return nil
}

func (f *fullReconciler) execSymlinkUpload(ctx context.Context, a syncAction) error {
	remotePath := absoluteRemotePath(a.path)
	if err := f.checkRunning(ctx); err != nil {
		return err
	}
	if _, err := syncUploadSymlink(ctx, f.r.fs, remotePath, a.target, a.storedEntry, a.hasStored); err != nil {
		if errors.Is(err, client.ErrWriteConflict) {
			f.r.requestFullSweep()
		}
		return fmt.Errorf("symlink upload %s: %w", a.path, err)
	}
	f.updateActionState(a, SyncEntry{
		Type:         "symlink",
		Target:       a.target,
		LastSyncedAt: time.Now().UTC(),
	})
	return nil
}

// cleanupTombstones removes tombstone entries older than 5 minutes to
// prevent unbounded state growth.
func (f *fullReconciler) cleanupTombstones() {
	const maxAge = 5 * time.Minute
	now := time.Now()
	f.r.state.mu.Lock()
	for path, entry := range f.r.state.state.Entries {
		if entry.Deleted && now.Sub(entry.LastSyncedAt) > maxAge {
			delete(f.r.state.state.Entries, path)
			f.r.state.dirty = true
		}
	}
	f.r.state.mu.Unlock()
}

func (f *fullReconciler) updateState(path string, entry SyncEntry) {
	f.r.state.mu.Lock()
	entry.Version = f.r.state.nextVersion()
	f.r.state.state.Entries[path] = entry
	f.r.state.dirty = true
	f.r.state.mu.Unlock()
}

func (f *fullReconciler) refreshStateMeta(rel string, l, r observedMeta, stored SyncEntry, hasStored bool) {
	now := time.Now().UTC()
	f.r.state.mu.Lock()
	defer f.r.state.mu.Unlock()
	entry, exists := f.r.state.state.Entries[rel]
	if exists != hasStored || exists && entry.Version != stored.Version {
		f.r.requestFullSweep()
		return
	}
	entry.Deleted = false
	entry.Type = l.kind
	entry.Mode = l.mode
	entry.Size = l.size
	entry.LocalMtimeMs = l.mtimeMs
	entry.RemoteMtimeMs = r.mtimeMs
	entry.Target = targetFromMeta(l, r)
	entry.LastSyncedAt = now
	entry.Version = f.r.state.nextVersion()
	f.r.state.state.Entries[rel] = entry
	f.r.state.dirty = true
}

func targetFromMeta(l, r observedMeta) string {
	if l.kind == "symlink" {
		return l.target
	}
	if r.kind == "symlink" {
		return r.target
	}
	return ""
}

func joinRemote(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// remoteSubscriptionPump runs in its own goroutine, translating client
// invalidation events into remoteEvents the reconciler understands.
// It also handles durable stream catch-up on startup and after each
// pub/sub reconnection.
type remoteSubscriptionPump struct {
	fs          client.Client
	log         *syncLogger
	stateWriter *stateWriter
	out         chan remoteEvent
	onOverflow  func(remoteEvent)
}

func newRemoteSubscriptionPump(fs client.Client, log *syncLogger, sw *stateWriter, onOverflow func(remoteEvent)) *remoteSubscriptionPump {
	return &remoteSubscriptionPump{fs: fs, log: log, stateWriter: sw, out: make(chan remoteEvent, 256), onOverflow: onOverflow}
}

func (p *remoteSubscriptionPump) events() <-chan remoteEvent { return p.out }

func (p *remoteSubscriptionPump) run(ctx context.Context, onReconnect func()) error {
	p.log.Info("subscription pump started, listening for remote changes")

	handler := func(ev client.InvalidateEvent) {
		p.dispatchInvalidateEvent(ev)
	}

	// Recover only after Redis confirms the live subscription. Catching up
	// first leaves a gap where a final remote publication can be lost forever.
	return p.fs.SubscribeInvalidationsWithReconnect(ctx, handler, func() {
		p.log.Info("pub/sub connected, replaying change stream")
		p.fs.InvalidateCache()
		if !p.catchUpFromStream(ctx) {
			if onReconnect != nil {
				onReconnect()
			}
		}
	})
}

// catchUpFromStream reads the durable change stream from the last persisted
// cursor and dispatches any missed events. Returns true if fully caught up,
// false if the cursor was missing/trimmed (caller should fall back to full
// reconcile).
func (p *remoteSubscriptionPump) catchUpFromStream(ctx context.Context) bool {
	lastID := p.stateWriter.lastStreamID()
	if lastID == "" {
		// The initial scan can predate subscription confirmation, so a missing
		// cursor requires a fresh scan even on the first connection.
		return false
	}
	const batchSize int64 = 500
	total := 0
	for {
		entries, err := p.fs.ReadChangeStream(ctx, lastID, batchSize)
		if err != nil {
			if errors.Is(err, client.ErrStreamTrimmed) {
				p.log.Info("change stream cursor trimmed, falling back to full reconcile")
			} else {
				p.log.Err("stream catch-up", err.Error())
			}
			return false
		}
		if len(entries) == 0 {
			if total > 0 {
				p.log.Info(fmt.Sprintf("stream catch-up complete: replayed %d entries", total))
			}
			return true
		}
		for _, e := range entries {
			if e.Event.Origin == p.fs.OriginID() {
				lastID = e.ID
				continue
			}
			fmt.Fprintf(os.Stderr, "afs sync: stream catch-up id=%s origin=%s op=%s paths=%v\n", e.ID, e.Event.Origin, e.Event.Op, e.Event.Paths)
			p.dispatchInvalidateEvent(client.InvalidateEvent(e.Event))
			lastID = e.ID
			total++
		}
		p.stateWriter.updateStreamID(lastID)
		if int64(len(entries)) < batchSize {
			if total > 0 {
				p.log.Info(fmt.Sprintf("stream catch-up complete: replayed %d entries", total))
			}
			return true
		}
	}
}

// dispatchInvalidateEvent translates one InvalidateEvent into remoteEvent(s)
// on the output channel. Shared by the live pub/sub handler and the stream
// catch-up path.
func (p *remoteSubscriptionPump) dispatchInvalidateEvent(ev client.InvalidateEvent) {
	fmt.Fprintf(os.Stderr, "afs sync: dispatch origin=%s op=%s paths=%v (self=%s)\n", ev.Origin, ev.Op, ev.Paths, p.fs.OriginID())
	switch ev.Op {
	case client.InvalidateOpContent:
		for _, path := range ev.Paths {
			p.log.RemoteChange(path, "content")
			p.send(remoteEvent{Path: path, NeedsContent: true})
		}
	case client.InvalidateOpInode:
		for _, path := range ev.Paths {
			p.log.RemoteChange(path, "inode")
			p.send(remoteEvent{Path: path})
		}
	case client.InvalidateOpDir:
		for _, path := range ev.Paths {
			p.log.RemoteChange(path, "dir")
			p.send(remoteEvent{Path: path})
		}
	case client.InvalidateOpPrefix:
		for _, path := range ev.Paths {
			if path == "/" || path == "" {
				p.log.Info("full sweep requested (prefix /)")
				p.send(remoteEvent{FullSweep: true})
				return
			}
			p.log.RemoteChange(path, "prefix")
			p.send(remoteEvent{Path: path})
		}
	case client.InvalidateOpRootReplace:
		p.log.Info("root replacement requested")
		p.send(remoteEvent{RootReplace: true})
	}
}

func (p *remoteSubscriptionPump) send(ev remoteEvent) {
	select {
	case p.out <- ev:
	default:
		// Recovery must bypass the saturated event queue. Otherwise the
		// last publication and its fallback can both disappear forever.
		p.onOverflow(ev)
	}
}
