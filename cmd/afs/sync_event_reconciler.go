package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

// remoteEvent is the downloader-side counterpart of LocalEvent. It is
// produced by the subscription pump for each invalidation we receive from
// other clients touching the live workspace root.
type remoteEvent struct {
	Path         string // workspace-relative POSIX path; empty means "root, full reconciliation needed"
	NeedsContent bool   // true for InvalidateOpContent
	FullSweep    bool   // true for InvalidateOpPrefix "/"
	RootReplace  bool   // true when a checkpoint restore replaced the root
}

// reconciler is the single writer for SyncState. Watcher events, downloader
// subscription events, upload results, and download results all funnel
// through one goroutine so we never have to lock individual entries.
type reconciler struct {
	stopCh         <-chan struct{}
	asyncWG        sync.WaitGroup
	state          *stateWriter
	root           string
	rootIdentity   string
	workspace      string
	storageID      string
	headCheckpoint string
	sessionID      string
	store          *afsStore
	echo           *echoSuppressor
	conflict       *conflictNamer
	ignore         *syncIgnore

	uploadCh      chan uploadOp
	downloadCh    chan downloadOp
	uploadResCh   chan uploadResult
	downloadResCh chan downloadResult
	localCh       <-chan LocalEvent
	remoteCh      <-chan remoteEvent

	// Shared session History recording for explicit save mutations.

	fs           client.Client
	maxFileBytes int64
	readonly     bool
	log          *syncLogger

	// Chunk-level delta sync.
	chunkSize      int
	chunkThreshold int

	pendingFullSweep    bool
	fullSweepRequest    chan struct{}
	rootReplaceRequest  chan struct{}
	suppressLocalEvents atomic.Bool
	renameMu            sync.Mutex
	renameCandidates    map[string]renameCandidate
	pendingUploads      map[string]pendingSyncUpload // protected by state.mu
}

type renameCandidate struct {
	path       string
	entry      SyncEntry
	recordedAt time.Time
}

func newReconciler(
	state *stateWriter,
	root, workspace string,
	storageID string,
	headCheckpoint string,
	sessionID string,
	store *afsStore,
	fs client.Client,
	echo *echoSuppressor,
	conflict *conflictNamer,
	ignore *syncIgnore,
	maxFileBytes int64,
	readonly bool,
	log *syncLogger,
	chunkSize int,
	chunkThreshold int,
) *reconciler {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if chunkThreshold <= 0 {
		chunkThreshold = defaultChunkThreshold
	}
	return &reconciler{
		state:              state,
		root:               root,
		rootIdentity:       localFileIdentityFromPath(root),
		workspace:          workspace,
		storageID:          strings.TrimSpace(storageID),
		headCheckpoint:     strings.TrimSpace(headCheckpoint),
		sessionID:          strings.TrimSpace(sessionID),
		store:              store,
		fs:                 fs,
		echo:               echo,
		conflict:           conflict,
		ignore:             ignore,
		maxFileBytes:       maxFileBytes,
		readonly:           readonly,
		log:                log,
		chunkSize:          chunkSize,
		chunkThreshold:     chunkThreshold,
		uploadCh:           make(chan uploadOp, 256),
		downloadCh:         make(chan downloadOp, 256),
		uploadResCh:        make(chan uploadResult, 256),
		downloadResCh:      make(chan downloadResult, 256),
		fullSweepRequest:   make(chan struct{}, 1),
		rootReplaceRequest: make(chan struct{}, 1),
		renameCandidates:   make(map[string]renameCandidate),
	}
}

// uploadIn is the channel the uploader drains.
func (r *reconciler) uploadIn() <-chan uploadOp { return r.uploadCh }

// downloadIn is the channel the downloader drains.
func (r *reconciler) downloadIn() <-chan downloadOp { return r.downloadCh }

// uploadOut is where the uploader posts results.
func (r *reconciler) uploadOut() chan<- uploadResult { return r.uploadResCh }

// downloadOut is where the downloader posts results.
func (r *reconciler) downloadOut() chan<- downloadResult { return r.downloadResCh }

// fullSweepRequests is signalled by the daemon when a full reconciliation
// pass is required (subscription reconnect, prefix-of-root invalidation,
// startup completion).
func (r *reconciler) fullSweepRequests() <-chan struct{} { return r.fullSweepRequest }

// rootReplaceRequests is signalled when the control plane replaces the live
// root as one operation, for example after checkpoint restore.
func (r *reconciler) rootReplaceRequests() <-chan struct{} { return r.rootReplaceRequest }

// requestFullSweep posts a full-sweep request unless one is already queued.
func (r *reconciler) requestFullSweep() {
	select {
	case r.fullSweepRequest <- struct{}{}:
	default:
	}
}

func (r *reconciler) requestRootReplace() {
	select {
	case r.rootReplaceRequest <- struct{}{}:
	default:
	}
}

func (r *reconciler) suppressLocalEventsDuringRestore(suppress bool) {
	r.suppressLocalEvents.Store(suppress)
}

// run drains all event sources until ctx is cancelled. The reconciler is the
// single writer for r.state.
func (r *reconciler) run(ctx context.Context, local <-chan LocalEvent, remote <-chan remoteEvent) {
	r.localCh = local
	r.remoteCh = remote
	symlinkSweep := time.NewTicker(500 * time.Millisecond)
	defer symlinkSweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-symlinkSweep.C:
			r.sweepMissingLocalSymlinks(ctx)
		case ev, ok := <-r.localCh:
			if !ok {
				r.localCh = nil
				continue
			}
			r.handleLocalEvent(ctx, ev)
		case ev, ok := <-r.remoteCh:
			if !ok {
				r.remoteCh = nil
				continue
			}
			r.handleRemoteEvent(ctx, ev)
		case res := <-r.uploadResCh:
			r.handleUploadResult(ctx, res)
		case res := <-r.downloadResCh:
			r.handleDownloadResult(ctx, res)
		}
	}
}

// handleLocalEvent processes a watcher event. The reconciler reads the local
// file from disk, computes the hash, looks up the stored entry, and decides
// whether to enqueue an upload, drop as echo, or trigger conflict resolution.
func (r *reconciler) handleLocalEvent(ctx context.Context, ev LocalEvent) {
	if ctx.Err() != nil || r.readonly {
		return
	}
	if err := r.checkLocalRoot(); err != nil {
		r.log.Err("local event", err.Error())
		return
	}
	if ev.Path == "" {
		return
	}
	if ev.Path == "." {
		info, err := os.Lstat(r.root)
		if err != nil || !info.IsDir() {
			return
		}
		r.handleLocalDir(ctx, ev.Path, r.root, info)
		return
	}
	if r.suppressLocalEvents.Load() {
		return
	}
	if isSyncControlPath(ev.Path) {
		return
	}
	if r.ignore.shouldIgnore(ev.Path, false) {
		return
	}
	abs := filepath.Join(r.root, filepath.FromSlash(ev.Path))
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		if exp, ok := r.echo.consume(ev.Path); ok && exp.kind == "delete" {
			return
		}
		fmt.Fprintf(os.Stderr, "afs sync: handleLocalEvent %s: ErrNotExist → handleLocalDelete (kindHint=%s)\n", ev.Path, ev.KindHint)
		r.log.LocalChange(ev.Path, "deleted")
		r.handleLocalDelete(ctx, ev.Path, ev.KindHint)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "afs sync: lstat %s: %v\n", abs, err)
		return
	}
	if r.echo.matchesTemporaryDirectory(ev.Path, info) {
		return
	}

	// Echo suppression: if the downloader marked an expectation for this
	// path and the disk content matches, drop the event silently.
	if exp, ok := r.echo.consume(ev.Path); ok {
		if r.echoMatches(abs, info, exp) {
			return
		}
		// Echo expectation existed but disk does not match — fall through
		// and treat as a normal local event. The expectation has already
		// been consumed.
	}

	// Log only events that survived ignore + echo filtering, and skip
	// chmod-only events (permission changes are synced but not interesting
	// to the user in the interactive log).
	if ev.KindHint != "chmod" {
		r.log.LocalChange(ev.Path, ev.KindHint)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		r.handleLocalSymlink(ev.Path, abs)
	case info.IsDir():
		r.handleLocalDir(ctx, ev.Path, abs, info)
	case info.Mode().IsRegular():
		r.handleLocalFile(ev.Path, abs, info)
	default:
		// Sockets, fifos, devices: ignored.
	}
}

func (r *reconciler) echoMatches(abs string, info fs.FileInfo, exp echoExpectation) bool {
	switch exp.kind {
	case "file":
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			return false
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return false
		}
		return sha256Hex(data) == exp.hash
	case "symlink":
		target, err := os.Readlink(abs)
		if err != nil {
			return false
		}
		return target == exp.hash
	case "dir":
		return info.IsDir() && uint32(info.Mode().Perm()) == exp.mode && exp.identity == localFileIdentity(info)
	case "delete":
		return false // shouldn't happen — file present means no delete echo
	}
	return false
}

func renameCandidateKey(entry SyncEntry) string {
	if strings.TrimSpace(entry.LocalIdentity) != "" {
		return entry.Type + ":inode:" + entry.LocalIdentity
	}
	switch entry.Type {
	case "file":
		if entry.LocalHash != "" {
			return fmt.Sprintf("file:hash:%s:%d", entry.LocalHash, entry.Size)
		}
	case "symlink":
		if entry.Target != "" {
			return "symlink:target:" + entry.Target
		}
	}
	return ""
}

func renameCandidateKeyForLocalFile(identity, hash string, size int64) string {
	if strings.TrimSpace(identity) != "" {
		return "file:inode:" + identity
	}
	if hash != "" {
		return fmt.Sprintf("file:hash:%s:%d", hash, size)
	}
	return ""
}

func renameCandidateKeyForLocalSymlink(identity, target string) string {
	if strings.TrimSpace(identity) != "" {
		return "symlink:inode:" + identity
	}
	if target != "" {
		return "symlink:target:" + target
	}
	return ""
}

func (r *reconciler) rememberRenameCandidate(path string, entry SyncEntry) {
	key := renameCandidateKey(entry)
	if key == "" {
		return
	}
	r.renameMu.Lock()
	defer r.renameMu.Unlock()
	r.renameCandidates[key] = renameCandidate{
		path:       path,
		entry:      entry,
		recordedAt: time.Now().UTC(),
	}
}

func (r *reconciler) takeRenameCandidate(key string) (renameCandidate, bool) {
	if key == "" {
		return renameCandidate{}, false
	}
	r.renameMu.Lock()
	defer r.renameMu.Unlock()
	candidate, ok := r.renameCandidates[key]
	if !ok {
		return renameCandidate{}, false
	}
	delete(r.renameCandidates, key)
	if time.Since(candidate.recordedAt) > 5*time.Second {
		return renameCandidate{}, false
	}
	return candidate, true
}

func (r *reconciler) takeFallbackRenameCandidate(rel, wantType string) (renameCandidate, bool) {
	if strings.TrimSpace(rel) == "" || strings.TrimSpace(wantType) == "" {
		return renameCandidate{}, false
	}
	dir := path.Dir(rel)
	now := time.Now().UTC()

	r.renameMu.Lock()
	defer r.renameMu.Unlock()

	var (
		matchKey string
		match    renameCandidate
		count    int
	)
	for key, candidate := range r.renameCandidates {
		if now.Sub(candidate.recordedAt) > 5*time.Second {
			delete(r.renameCandidates, key)
			continue
		}
		if candidate.entry.Type != wantType {
			continue
		}
		if candidate.path == rel || path.Dir(candidate.path) != dir {
			continue
		}
		matchKey = key
		match = candidate
		count++
		if count > 1 {
			return renameCandidate{}, false
		}
	}
	if count != 1 {
		return renameCandidate{}, false
	}
	delete(r.renameCandidates, matchKey)
	return match, true
}

func (r *reconciler) takeRenameCandidateForLocalFile(rel, identity, hash string, size int64, hasStored bool) (renameCandidate, bool) {
	// An atomic replacement of a tracked path keeps that path's baseline.
	// Its new inode may reuse a recently deleted file's identity; treating
	// that as a rename would substitute the unrelated source's baseline.
	if hasStored {
		return renameCandidate{}, false
	}
	if candidate, ok := r.takeRenameCandidate(renameCandidateKeyForLocalFile(identity, hash, size)); ok {
		return candidate, true
	}
	return r.takeFallbackRenameCandidate(rel, "file")
}

func (r *reconciler) takeRenameCandidateForLocalSymlink(rel, identity, target string, hasStored bool) (renameCandidate, bool) {
	if hasStored {
		return renameCandidate{}, false
	}
	if candidate, ok := r.takeRenameCandidate(renameCandidateKeyForLocalSymlink(identity, target)); ok {
		return candidate, true
	}
	return r.takeFallbackRenameCandidate(rel, "symlink")
}

func (r *reconciler) renameCandidateStillPending(key, path string) bool {
	if key == "" {
		return false
	}
	r.renameMu.Lock()
	defer r.renameMu.Unlock()
	candidate, ok := r.renameCandidates[key]
	if !ok {
		return false
	}
	return candidate.path == path
}

func (r *reconciler) forgetRenameCandidate(entry SyncEntry) {
	key := renameCandidateKey(entry)
	if key == "" {
		return
	}
	r.renameMu.Lock()
	defer r.renameMu.Unlock()
	delete(r.renameCandidates, key)
}

func (r *reconciler) enqueueUploadOpAsync(op uploadOp, delay time.Duration, shouldSend func() bool) {
	r.asyncWG.Add(1)
	go func() {
		defer r.asyncWG.Done()
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-r.stopCh:
				return
			case <-timer.C:
			}
		}
		if shouldSend != nil && !shouldSend() {
			return
		}
		r.queueUpload(op)
	}()
}

func (r *reconciler) queueUpload(op uploadOp) {
	select {
	case <-r.stopCh:
		return
	default:
	}
	select {
	case <-r.stopCh:
	case r.uploadCh <- op:
	}
}

func (r *reconciler) queueDownload(op downloadOp) {
	select {
	case <-r.stopCh:
		return
	default:
	}
	select {
	case <-r.stopCh:
	case r.downloadCh <- op:
	}
}

func (r *reconciler) stageSyncEntry(path string, entry SyncEntry) uint64 {
	r.state.mu.Lock()
	entryCopy := entry
	entryCopy.LastSyncedAt = time.Now().UTC()
	entryCopy.Version = r.state.nextVersion()
	r.state.state.Entries[path] = entryCopy
	r.state.dirty = true
	r.state.mu.Unlock()
	r.state.markDirty()
	return entryCopy.Version
}

func (r *reconciler) installPendingRename(path, localIdentity string, entry SyncEntry) uint64 {
	entryCopy := entry
	entryCopy.Deleted = false
	entryCopy.LocalIdentity = defaultString(strings.TrimSpace(localIdentity), entryCopy.LocalIdentity)
	return r.stageSyncEntry(path, entryCopy)
}

func (r *reconciler) handleLocalFile(rel, abs string, info fs.FileInfo) {
	fileSize := info.Size()
	if fileSize > r.maxFileBytes {
		fmt.Fprintf(os.Stderr, "afs sync: skipping %s — %d bytes exceeds %d byte cap\n", rel, fileSize, r.maxFileBytes)
		return
	}

	r.state.mu.Lock()
	stored, hasStored := r.state.state.Entries[rel]
	r.state.mu.Unlock()
	localIdentity := localFileIdentity(info)

	// Chunked path: files above the threshold use streaming chunk hashes
	// so we never load the full file into memory.
	if fileSize > int64(r.chunkThreshold) {
		hashes, actualSize, err := streamChunkHashes(abs, r.chunkSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "afs sync: chunk hash %s: %v\n", abs, err)
			return
		}
		hash := compositeHash(hashes)
		if candidate, ok := r.takeRenameCandidateForLocalFile(rel, localIdentity, hash, actualSize, hasStored); ok {
			renameVersion := r.installPendingRename(rel, localIdentity, candidate.entry)
			changed := hash != candidate.entry.LocalHash || uint32(info.Mode()&fs.ModePerm) != candidate.entry.Mode
			if changed {
				renameVersion = r.stageSyncEntry(rel, SyncEntry{
					Type:          "file",
					Mode:          uint32(info.Mode() & fs.ModePerm),
					Size:          actualSize,
					LocalHash:     hash,
					LocalIdentity: localIdentity,
					RemoteHash:    candidate.entry.RemoteHash,
					LocalMtimeMs:  info.ModTime().UnixMilli(),
					RemoteMtimeMs: candidate.entry.RemoteMtimeMs,
					ChunkSize:     r.chunkSize,
					ChunkHashes:   append([]string(nil), hashes...),
				})
			}
			r.enqueueTrackedUpload(uploadOp{
				Kind:          opUploadRename,
				Path:          rel,
				PrevPath:      candidate.path,
				RenameVersion: renameVersion,
				AbsPath:       abs,
				Mode:          uint32(info.Mode() & fs.ModePerm),
				LocalHash:     candidate.entry.LocalHash,
				LocalIdentity: localIdentity,
				LocalMtimeMs:  info.ModTime().UnixMilli(),
				StoredEntry:   candidate.entry,
				HasStored:     true,
			})
			if changed {
				dirty, _ := diffChunkManifests(candidate.entry.ChunkHashes, hashes)
				r.enqueueTrackedUpload(uploadOp{
					Kind:          opUploadFile,
					Path:          rel,
					AbsPath:       abs,
					Mode:          uint32(info.Mode() & fs.ModePerm),
					LocalHash:     hash,
					LocalIdentity: localIdentity,
					LocalMtimeMs:  info.ModTime().UnixMilli(),
					StoredEntry:   candidate.entry,
					HasStored:     true,
					Chunked:       true,
					FileSize:      actualSize,
					ChunkSize:     r.chunkSize,
					ChunkHashes:   hashes,
					DirtyChunks:   dirty,
				})
			}
			return
		}
		if hasStored && stored.LocalHash == hash && stored.Type == "file" && stored.Mode == uint32(info.Mode()&fs.ModePerm) {
			return
		}
		dirty, _ := diffChunkManifests(stored.ChunkHashes, hashes)
		r.enqueueTrackedUpload(uploadOp{
			Kind:          opUploadFile,
			Path:          rel,
			AbsPath:       abs,
			Mode:          uint32(info.Mode() & fs.ModePerm),
			LocalHash:     hash,
			LocalIdentity: localIdentity,
			LocalMtimeMs:  info.ModTime().UnixMilli(),
			StoredEntry:   stored,
			HasStored:     hasStored,
			Chunked:       true,
			FileSize:      actualSize,
			ChunkSize:     r.chunkSize,
			ChunkHashes:   hashes,
			DirtyChunks:   dirty,
		})
		return
	}

	// Inline path: small files use full-file read (existing behavior).
	data, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afs sync: read %s: %v\n", abs, err)
		return
	}
	hash := sha256Hex(data)
	if candidate, ok := r.takeRenameCandidateForLocalFile(rel, localIdentity, hash, fileSize, hasStored); ok {
		renameVersion := r.installPendingRename(rel, localIdentity, candidate.entry)
		changed := hash != candidate.entry.LocalHash || uint32(info.Mode()&fs.ModePerm) != candidate.entry.Mode
		if changed {
			renameVersion = r.stageSyncEntry(rel, SyncEntry{
				Type:          "file",
				Mode:          uint32(info.Mode() & fs.ModePerm),
				Size:          fileSize,
				LocalHash:     hash,
				LocalIdentity: localIdentity,
				RemoteHash:    candidate.entry.RemoteHash,
				LocalMtimeMs:  info.ModTime().UnixMilli(),
				RemoteMtimeMs: candidate.entry.RemoteMtimeMs,
			})
		}
		r.enqueueTrackedUpload(uploadOp{
			Kind:          opUploadRename,
			Path:          rel,
			PrevPath:      candidate.path,
			RenameVersion: renameVersion,
			AbsPath:       abs,
			Mode:          uint32(info.Mode() & fs.ModePerm),
			LocalHash:     candidate.entry.LocalHash,
			LocalIdentity: localIdentity,
			LocalMtimeMs:  info.ModTime().UnixMilli(),
			StoredEntry:   candidate.entry,
			HasStored:     true,
		})
		if changed {
			r.enqueueTrackedUpload(uploadOp{
				Kind:          opUploadFile,
				Path:          rel,
				AbsPath:       abs,
				Content:       data,
				Mode:          uint32(info.Mode() & fs.ModePerm),
				LocalHash:     hash,
				LocalIdentity: localIdentity,
				LocalMtimeMs:  info.ModTime().UnixMilli(),
				StoredEntry:   candidate.entry,
				HasStored:     true,
			})
		}
		return
	}
	if hasStored && stored.LocalHash == hash && stored.Type == "file" && stored.Mode == uint32(info.Mode()&fs.ModePerm) {
		return
	}
	r.enqueueTrackedUpload(uploadOp{
		Kind:          opUploadFile,
		Path:          rel,
		AbsPath:       abs,
		Content:       data,
		Mode:          uint32(info.Mode() & fs.ModePerm),
		LocalHash:     hash,
		LocalIdentity: localIdentity,
		LocalMtimeMs:  info.ModTime().UnixMilli(),
		StoredEntry:   stored,
		HasStored:     hasStored,
	})
}

func (r *reconciler) handleLocalSymlink(rel, abs string) {
	target, err := os.Readlink(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afs sync: readlink %s: %v\n", abs, err)
		return
	}
	info, err := os.Lstat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afs sync: lstat symlink %s: %v\n", abs, err)
		return
	}
	localIdentity := localFileIdentity(info)
	r.state.mu.Lock()
	stored, hasStored := r.state.state.Entries[rel]
	r.state.mu.Unlock()
	if candidate, ok := r.takeRenameCandidateForLocalSymlink(rel, localIdentity, target, hasStored); ok {
		renameVersion := r.installPendingRename(rel, localIdentity, candidate.entry)
		r.enqueueTrackedUpload(uploadOp{
			Kind:          opUploadRename,
			Path:          rel,
			PrevPath:      candidate.path,
			RenameVersion: renameVersion,
			AbsPath:       abs,
			Symlink:       target,
			LocalIdentity: localIdentity,
			StoredEntry:   candidate.entry,
			HasStored:     true,
		})
		return
	}
	if hasStored && stored.Type == "symlink" && stored.Target == target {
		return
	}
	r.enqueueTrackedUpload(uploadOp{
		Kind:          opUploadSymlink,
		Path:          rel,
		AbsPath:       abs,
		Symlink:       target,
		LocalIdentity: localIdentity,
		StoredEntry:   stored,
		HasStored:     hasStored,
	})
}

func (r *reconciler) handleLocalDir(ctx context.Context, rel, abs string, info fs.FileInfo) {
	r.state.mu.Lock()
	stored, hasStored := r.state.state.Entries[rel]
	r.state.mu.Unlock()
	r.reconcileLocalDirDeletes(ctx, rel)
	mode := uint32(info.Mode().Perm())
	kind := opUploadMkdir
	if hasStored && stored.Type == "dir" && !stored.Deleted {
		if stored.Mode == mode {
			return
		}
		kind = opUploadChmod
	}
	if r.readonly {
		return
	}
	r.enqueueTrackedUpload(uploadOp{
		Kind:        kind,
		Path:        rel,
		AbsPath:     abs,
		Mode:        uint32(info.Mode() & fs.ModePerm),
		StoredEntry: stored,
		HasStored:   hasStored,
	})
}

func (r *reconciler) reconcileLocalDirDeletes(ctx context.Context, rel string) {
	prefix := ""
	if rel != "." {
		prefix = strings.TrimSuffix(rel, "/") + "/"
	}
	missing := make([]string, 0)
	r.state.mu.Lock()
	for path, entry := range r.state.state.Entries {
		if entry.Deleted || entry.Type == "dir" {
			continue
		}
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(path))); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, path)
		}
	}
	r.state.mu.Unlock()
	for _, missingPath := range missing {
		r.handleLocalDelete(ctx, missingPath, "remove")
	}
}

func (r *reconciler) sweepMissingLocalSymlinks(ctx context.Context) {
	if r.suppressLocalEvents.Load() {
		return
	}
	missing := make([]string, 0)
	r.state.mu.Lock()
	for path, entry := range r.state.state.Entries {
		if entry.Deleted || entry.Type != "symlink" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(path))); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, path)
		}
	}
	r.state.mu.Unlock()
	for _, missingPath := range missing {
		r.handleLocalDelete(ctx, missingPath, "remove")
	}
}

func (r *reconciler) handleLocalDelete(ctx context.Context, rel, kindHint string) {
	if r.readonly {
		return
	}
	if err := r.checkLocalRoot(); err != nil {
		r.log.Err("local deletion", err.Error())
		return
	}
	r.state.mu.Lock()
	stored, hasStored := r.state.state.Entries[rel]
	if !hasStored {
		r.state.mu.Unlock()
		fmt.Fprintf(os.Stderr, "afs sync: handleLocalDelete %s: not in state, skipping\n", rel)
		return
	}
	if stored.Deleted {
		// Watcher and sweep notifications may describe the same removal.
		// Keep its version so the pending acknowledgment can clear it.
		r.state.mu.Unlock()
		return
	}
	fmt.Fprintf(os.Stderr, "afs sync: handleLocalDelete %s: setting tombstone, queuing upload delete\n", rel)
	prior := stored
	// Tombstone immediately — buildPlan sees this before upload completes.
	stored.Deleted = true
	stored.Version = r.state.nextVersion()
	stored.LastSyncedAt = time.Now().UTC()
	r.state.state.Entries[rel] = stored
	r.state.dirty = true
	r.state.mu.Unlock()
	r.state.markDirty()
	deleteOp := uploadOp{
		Kind:          opUploadDelete,
		Path:          rel,
		AbsPath:       filepath.Join(r.root, filepath.FromSlash(rel)),
		StoredEntry:   prior,
		HasStored:     true,
		DeleteVersion: stored.Version,
	}
	candidateKey := renameCandidateKey(prior)
	if candidateKey == "" {
		r.queueUpload(deleteOp)
		return
	}
	r.rememberRenameCandidate(rel, prior)
	if kindHint == "rename" {
		r.scanForRenameTargets(ctx, rel)
	}
	r.enqueueUploadOpAsync(deleteOp, 150*time.Millisecond, func() bool {
		return r.renameCandidateStillPending(candidateKey, rel)
	})
}

func (r *reconciler) scanForRenameTargets(ctx context.Context, deletedPath string) {
	root := r.root
	_ = filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if abs == root || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == deletedPath || r.ignore.shouldIgnore(rel, false) {
			return nil
		}
		r.handleLocalEvent(ctx, LocalEvent{Path: rel, KindHint: "rename"})
		return nil
	})
}

func (r *reconciler) handleRemoteEvent(ctx context.Context, ev remoteEvent) {
	if ev.RootReplace {
		r.requestRootReplace()
		return
	}
	if ev.FullSweep {
		r.requestFullSweep()
		return
	}
	if ev.Path == "" {
		return
	}
	rel := strings.TrimPrefix(ev.Path, "/")
	if rel == "" {
		r.requestFullSweep()
		return
	}
	if r.ignore.shouldIgnore(rel, false) {
		return
	}

	// Skip remote events for paths with a local tombstone — the delete
	// upload will propagate the removal to Redis shortly.
	r.state.mu.Lock()
	stored, hasStored := r.state.state.Entries[rel]
	r.state.mu.Unlock()
	if hasStored && stored.Deleted {
		return
	}

	abs := filepath.Join(r.root, filepath.FromSlash(rel))

	stat, err := r.fs.Stat(ctx, absoluteRemotePath(rel))
	if err != nil && !isSyncRemoteMissingError(err) {
		fmt.Fprintf(os.Stderr, "afs sync: stat remote %s: %v\n", rel, err)
		return
	}
	if stat == nil {
		if hasStored && !stored.Deleted {
			// The invalidation event might have arrived while the file was
			// being created (pre-write invalidation race) or the cache was
			// transiently stale. Flush the cache and retry once before
			// concluding the file was deleted.
			r.fs.InvalidateCache()
			stat2, err2 := r.fs.Stat(ctx, absoluteRemotePath(rel))
			if err2 != nil && !isSyncRemoteMissingError(err2) {
				r.log.Err("confirm remote deletion "+rel, err2.Error())
				r.requestFullSweep()
				return
			}
			if err2 == nil && stat2 != nil {
				// File exists after cache flush — not actually deleted.
				// Treat as an update instead.
				fmt.Fprintf(os.Stderr, "afs sync: handleRemoteEvent %s: stat nil → retry found it (cache was stale)\n", rel)
				stat = stat2
				// Fall through to the download/update logic below.
			} else {
				// Confirmed: file truly gone from remote. Invalidate plans made
				// before this observation, but retain the live local baseline
				// until the downloader removes it. A tombstone while that file
				// still exists looks like a local recreation to a full sweep.
				r.state.mu.Lock()
				entry, ok := r.state.state.Entries[rel]
				if !ok || entry.Deleted || entry.Version != stored.Version {
					// The missing reads belong to the original baseline, never
					// to a newer local upload or recovery result.
					r.state.mu.Unlock()
					r.requestFullSweep()
					return
				}
				entry.Version = r.state.nextVersion()
				entry.LastSyncedAt = time.Now().UTC()
				r.state.state.Entries[rel] = entry
				r.state.dirty = true
				stored = entry
				r.state.mu.Unlock()

				fmt.Fprintf(os.Stderr, "afs sync: handleRemoteEvent %s: stat nil confirmed after retry → downloadDelete\n", rel)
				r.log.RemoteChange(rel, "deleted")
				r.queueDownload(downloadOp{
					Kind:        opDownloadDelete,
					Path:        rel,
					AbsPath:     abs,
					StoredEntry: stored,
					HasStored:   true,
				})
				return
			}
		} else {
			return
		}
	}

	// Detect potential conflict: local file diverged from stored hash AND
	// remote also moved off stored hash.
	conflict := r.detectConflict(rel, abs, stored, hasStored)
	r.log.RemoteChange(rel, stat.Type+" changed")

	switch stat.Type {
	case "dir":
		r.queueDownload(downloadOp{
			Kind:        opDownloadMkdir,
			Path:        rel,
			AbsPath:     abs,
			Mode:        stat.Mode,
			StoredEntry: stored,
			HasStored:   hasStored,
		})
	case "symlink":
		target, err := r.fs.Readlink(ctx, absoluteRemotePath(rel))
		if err != nil {
			fmt.Fprintf(os.Stderr, "afs sync: readlink remote %s: %v\n", rel, err)
			return
		}
		r.queueDownload(downloadOp{
			Kind:        opDownloadSymlink,
			Path:        rel,
			AbsPath:     abs,
			Symlink:     target,
			StoredEntry: stored,
			HasStored:   hasStored,
			Conflict:    conflict,
		})
	case "file":
		// Check if the remote file has chunk metadata for delta download.
		chunkSize, remoteHashes, chunkErr := r.fs.ChunkMeta(ctx, absoluteRemotePath(rel))
		if chunkErr == nil && chunkSize > 0 && len(remoteHashes) > 0 {
			dirty, _ := diffChunkManifests(stored.ChunkHashes, remoteHashes)
			r.queueDownload(downloadOp{
				Kind:        opDownloadFile,
				Path:        rel,
				AbsPath:     abs,
				Mode:        stat.Mode,
				StoredEntry: stored,
				HasStored:   hasStored,
				Conflict:    conflict,
				Chunked:     true,
				FileSize:    stat.Size,
				ChunkSize:   chunkSize,
				ChunkHashes: remoteHashes,
				DirtyChunks: dirty,
			})
		} else {
			r.queueDownload(downloadOp{
				Kind:        opDownloadFile,
				Path:        rel,
				AbsPath:     abs,
				Mode:        stat.Mode,
				StoredEntry: stored,
				HasStored:   hasStored,
				Conflict:    conflict,
			})
		}
	}
}

// detectConflict returns true when both sides moved off the stored hash. If
// the local file is missing or the stored entry is empty, no conflict is
// possible (it's just a download).
func (r *reconciler) detectConflict(rel, abs string, stored SyncEntry, hasStored bool) bool {
	if !hasStored {
		return false
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return false
		}
		return stored.Type == "symlink" && stored.Target != target
	}
	if !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	hash := sha256Hex(data)
	return stored.LocalHash != "" && hash != stored.LocalHash
}

func (r *reconciler) handleUploadResult(ctx context.Context, res uploadResult) {
	if res.Op.Tracked {
		defer r.finishPendingUpload(res.Op.Path)
		if res.Op.Kind == opUploadRename && res.Op.PrevPath != "" {
			defer r.finishPendingUpload(res.Op.PrevPath)
		}
	}
	if res.Skipped {
		if res.Op.Kind == opUploadRename {
			// installPendingRename copied the source baseline to a
			// destination that the failed rename never created remotely.
			// Forget that provisional entry so recovery uploads the local
			// destination instead of treating it as a remote deletion.
			r.state.mu.Lock()
			if entry, exists := r.state.state.Entries[res.Op.Path]; exists &&
				!entry.Deleted && res.Op.RenameVersion != 0 && entry.Version == res.Op.RenameVersion {
				delete(r.state.state.Entries, res.Op.Path)
				r.state.dirty = true
			}
			r.state.mu.Unlock()
			r.state.markDirty()
		}
		r.requestFullSweep()
		return
	}
	if res.Op.Kind == opUploadFile && r.uploadResultObsolete(res) {
		r.requestFullSweep()
		return
	}
	if res.Err != nil {
		r.log.Err("upload "+res.Op.Path, res.Err.Error())
		if !errors.Is(res.Err, context.Canceled) {
			// The watcher may never emit another event for these bytes, and
			// reconnect stream replay covers remote changes only. Keep local
			// work pending through the existing recovery loop, whose failed
			// scans retry with a one-second delay while Redis is unavailable.
			r.requestFullSweep()
		}
		return
	}
	if res.Conflict {
		if res.Op.Kind == opUploadDelete {
			// A local delete cannot discard a newer published edit. Remove
			// the provisional tombstone so normal inbound reconciliation
			// can retain the remote candidate at the original path.
			r.state.mu.Lock()
			if entry, ok := r.state.state.Entries[res.Op.Path]; ok && entry.Deleted {
				delete(r.state.state.Entries, res.Op.Path)
				r.state.dirty = true
			}
			r.state.mu.Unlock()
			r.handleRemoteEvent(ctx, remoteEvent{Path: absoluteRemotePath(res.Op.Path)})
			return
		}
		// Remote diverged. The remote-wins resolution is to download the
		// remote version, push the local copy aside, and create an
		// auto-checkpoint.
		kind := opDownloadFile
		if res.Op.Kind == opUploadSymlink {
			stat, err := r.fs.Stat(ctx, absoluteRemotePath(res.Op.Path))
			if err != nil || stat == nil || stat.Type == "dir" {
				r.requestFullSweep()
				return
			}
			if stat.Type == "symlink" {
				kind = opDownloadSymlink
			}
		}
		r.queueDownload(downloadOp{
			Kind:        kind,
			Path:        res.Op.Path,
			AbsPath:     res.Op.AbsPath,
			StoredEntry: res.Op.StoredEntry,
			HasStored:   res.Op.HasStored,
			Conflict:    true,
		})
		return
	}
	now := time.Now().UTC()
	r.state.mu.Lock()
	switch res.Op.Kind {
	case opUploadFile:
		size := int64(len(res.Op.Content))
		if res.Op.Chunked {
			size = res.Op.FileSize
		}
		localIdentity := defaultString(strings.TrimSpace(res.Op.LocalIdentity), localFileIdentityFromPath(res.Op.AbsPath))
		localIdentity = defaultString(localIdentity, storedLocalIdentity(res.Op.StoredEntry))
		entry := SyncEntry{
			Type:          "file",
			Mode:          res.Op.Mode,
			Size:          size,
			LocalHash:     res.Op.LocalHash,
			LocalIdentity: localIdentity,
			RemoteHash:    res.Op.LocalHash,
			LastSyncedAt:  now,
			ChunkSize:     res.Op.ChunkSize,
			ChunkHashes:   res.Op.ChunkHashes,
			Version:       r.state.nextVersion(),
		}
		if res.RemoteStat != nil {
			entry.RemoteMtimeMs = res.RemoteStat.Mtime
			entry.LocalMtimeMs = res.Op.LocalMtimeMs
		}
		r.state.state.Entries[res.Op.Path] = entry
	case opUploadSymlink:
		localIdentity := defaultString(strings.TrimSpace(res.Op.LocalIdentity), localFileIdentityFromPath(res.Op.AbsPath))
		localIdentity = defaultString(localIdentity, storedLocalIdentity(res.Op.StoredEntry))
		entry := SyncEntry{
			Type:          "symlink",
			Target:        res.Op.Symlink,
			LocalIdentity: localIdentity,
			LastSyncedAt:  now,
			Version:       r.state.nextVersion(),
		}
		if res.RemoteStat != nil {
			entry.RemoteMtimeMs = res.RemoteStat.Mtime
			entry.LocalMtimeMs = res.RemoteStat.Mtime
		}
		r.state.state.Entries[res.Op.Path] = entry
	case opUploadMkdir:
		r.state.state.Entries[res.Op.Path] = SyncEntry{
			Type:         "dir",
			Mode:         res.Op.Mode,
			LastSyncedAt: now,
			Version:      r.state.nextVersion(),
		}
	case opUploadDelete:
		// Only outstanding deletions need a tombstone. Retaining it after
		// acknowledgment would ignore or erase a later remote recreation.
		r.forgetRenameCandidate(res.Op.StoredEntry)
		if entry, ok := r.state.state.Entries[res.Op.Path]; ok && entry.Deleted &&
			res.Op.DeleteVersion != 0 && entry.Version == res.Op.DeleteVersion {
			delete(r.state.state.Entries, res.Op.Path)
		}
		// A recreation event may have arrived while this acknowledgment
		// was queued and been suppressed by the outstanding tombstone.
		r.requestFullSweep()
	case opUploadRename:
		delete(r.state.state.Entries, res.Op.PrevPath)
		entry := res.Op.StoredEntry
		entry.Deleted = false
		entry.LastSyncedAt = now
		localIdentity := defaultString(strings.TrimSpace(res.Op.LocalIdentity), localFileIdentityFromPath(res.Op.AbsPath))
		entry.LocalIdentity = defaultString(localIdentity, entry.LocalIdentity)
		entry.Version = r.state.nextVersion()
		if res.RemoteStat != nil {
			entry.RemoteMtimeMs = res.RemoteStat.Mtime
			entry.LocalMtimeMs = res.RemoteStat.Mtime
		}
		r.state.state.Entries[res.Op.Path] = entry
	case opUploadChmod:
		if entry, ok := r.state.state.Entries[res.Op.Path]; ok {
			entry.Mode = res.Op.Mode
			entry.Version = r.state.nextVersion()
			entry.LastSyncedAt = now
			r.state.state.Entries[res.Op.Path] = entry
		}
	}
	r.state.dirty = true
	r.state.mu.Unlock()
	r.state.markDirty()

	switch res.Op.Kind {
	case opUploadFile:
		r.log.Upload(res.Op.Path)
	case opUploadSymlink:
		r.log.Symlink(res.Op.Path, res.Op.Symlink, "upload")
	case opUploadMkdir:
		r.log.Mkdir(res.Op.Path, "upload")
		if res.RemoteStat != nil && res.RemoteStat.Mode != res.Op.Mode {
			r.handleRemoteEvent(ctx, remoteEvent{Path: absoluteRemotePath(res.Op.Path)})
		}
	case opUploadDelete:
		r.log.Delete(res.Op.Path, "upload")
	case opUploadRename:
		r.log.Upload(res.Op.Path)
	}
}

func (r *reconciler) handleDownloadResult(ctx context.Context, res downloadResult) {
	if res.ConflictPath != "" {
		// The preserved copy is new local work independently of whether the
		// canonical download result can still be adopted. Its rename may not
		// produce a separate watcher event, so keep publication scheduled even
		// when a newer local deletion causes the result below to be discarded.
		r.requestFullSweep()
	}
	if res.Skipped {
		r.requestFullSweep()
		return
	}
	if res.Err != nil {
		r.log.Err("download "+res.Op.Path, res.Err.Error())
		return
	}
	if err := r.checkLocalRoot(); err != nil {
		r.log.Err("download "+res.Op.Path, err.Error())
		return
	}
	if res.Op.Kind == opDownloadDelete {
		// The worker removed the completed inbound baseline atomically with
		// the local entry. A delayed result must not mutate a newer local
		// write or convert this remote deletion into an outbound tombstone.
		if res.ConflictPath != "" {
			r.log.Conflict(res.Op.Path, res.ConflictPath)
			r.requestFullSweep()
			if !r.readonly {
				go triggerConflictCheckpoint(ctx, r.store, r.workspace)
			}
		}
		r.log.Delete(res.Op.Path, "download")
		return
	}
	// A pending local deletion can discard the worker's own candidate. An
	// application may already have edited or recreated that path while this
	// acknowledgment waited, so match its identity and content before removal.
	r.state.mu.Lock()
	if entry, ok := r.state.state.Entries[res.Op.Path]; ok && entry.Deleted {
		r.state.mu.Unlock()
		abs := filepath.Join(r.root, filepath.FromSlash(res.Op.Path))
		if res.localCandidateUnchanged(abs) {
			_ = os.Remove(abs)
		}
		r.requestFullSweep()
		return
	}
	r.state.mu.Unlock()
	if res.ConflictPath != "" {
		r.log.Conflict(res.Op.Path, res.ConflictPath)
		if !r.readonly {
			go triggerConflictCheckpoint(ctx, r.store, r.workspace)
		}
	}
	now := time.Now().UTC()
	r.state.mu.Lock()
	switch res.Op.Kind {
	case opDownloadFile:
		entry := SyncEntry{
			Type:          "file",
			Mode:          res.Mode,
			Size:          res.Size,
			LocalHash:     res.RemoteHash,
			LocalIdentity: localFileIdentityFromPath(filepath.Join(r.root, filepath.FromSlash(res.Op.Path))),
			RemoteHash:    res.RemoteHash,
			LocalMtimeMs:  res.LocalMtimeMs,
			RemoteMtimeMs: res.MtimeMs,
			LastSyncedAt:  now,
			ChunkSize:     res.Op.ChunkSize,
			ChunkHashes:   res.Op.ChunkHashes,
			Version:       r.state.nextVersion(),
		}
		r.state.state.Entries[res.Op.Path] = entry
	case opDownloadSymlink:
		r.state.state.Entries[res.Op.Path] = SyncEntry{
			Type:          "symlink",
			Target:        res.Target,
			LocalIdentity: localFileIdentityFromPath(filepath.Join(r.root, filepath.FromSlash(res.Op.Path))),
			LastSyncedAt:  now,
			Version:       r.state.nextVersion(),
		}
	case opDownloadMkdir:
		entry := SyncEntry{
			Type:         "dir",
			Mode:         res.Op.Mode,
			LastSyncedAt: now,
			Version:      r.state.nextVersion(),
		}
		r.state.state.Entries[res.Op.Path] = entry
	case opDownloadChmod:
		if entry, ok := r.state.state.Entries[res.Op.Path]; ok {
			entry.Mode = res.Mode
			entry.Version = r.state.nextVersion()
			entry.LastSyncedAt = now
			r.state.state.Entries[res.Op.Path] = entry
		}
	}
	r.state.dirty = true
	r.state.mu.Unlock()
	r.state.markDirty()

	switch res.Op.Kind {
	case opDownloadFile:
		r.log.Download(res.Op.Path)
	case opDownloadSymlink:
		r.log.Symlink(res.Op.Path, res.Target, "download")
	case opDownloadMkdir:
		r.log.Mkdir(res.Op.Path, "download")
	case opDownloadDelete:
		r.log.Delete(res.Op.Path, "download")
	}
}
