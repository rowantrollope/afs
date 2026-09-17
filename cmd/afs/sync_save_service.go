package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The supervisor outlives individual worker generations. Requests are polled
// independently of fsnotify so an overflowing event queue cannot hide a save.
// Only this goroutine owns the active generation and writes save responses.
type syncSaveService struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	active *syncDaemon
	// The mount remains addressable if a worker generation cannot restart.
	workspace string
	localRoot string
	token     string
}

func startSyncSaveService(ctx context.Context, daemon *syncDaemon) *syncSaveService {
	return startSyncSaveServiceWithToken(ctx, daemon, "")
}

func startSyncSaveServiceWithToken(ctx context.Context, daemon *syncDaemon, token string) *syncSaveService {
	ctx = daemon.mutationContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	service := &syncSaveService{ctx: ctx, cancel: cancel, done: make(chan struct{}), active: daemon,
		workspace: daemon.cfg.Workspace, localRoot: daemon.cfg.LocalRoot, token: token}
	go service.run()
	return service
}

func (s *syncSaveService) Done() <-chan struct{} { return s.done }

func (s *syncSaveService) Stop() {
	s.cancel()
	<-s.done
}

func (s *syncSaveService) run() {
	defer close(s.done)
	defer func() {
		if s.active != nil {
			s.active.Stop()
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

func (s *syncSaveService) poll() {
	s.rememberMountIdentity()
	if s.localRoot == "" {
		return
	}
	root := s.localRoot
	entries, err := os.ReadDir(filepath.Join(root, syncControlRequestsDirName))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if s.ctx.Err() != nil {
			return
		}
		rel := filepath.Join(syncControlRequestsDirName, entry.Name())
		id, ok := syncControlRequestID(rel)
		if !ok || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
			continue
		}
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var request syncControlRequest
		if json.Unmarshal(raw, &request) != nil {
			continue
		}
		result, stop := s.handleRequest(request)
		// Remove the request first so a failed response write cannot repeat the save.
		_ = os.Remove(path)
		if err := writeSyncControlJSON(syncControlResultPath(root, id), result, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "afs sync: write save response: %v\n", err)
		}
		if stop {
			s.cancel()
			return
		}
	}
}

func (s *syncSaveService) handleRequest(request syncControlRequest) (syncControlResult, bool) {
	s.rememberMountIdentity()
	result := syncControlResult{Version: syncControlVersion, Operation: request.Operation,
		Workspace: s.workspace, LocalRoot: s.localRoot}
	if s.token != "" && subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.token)) != 1 {
		result.Error = "sync daemon identity token does not match"
		return result, false
	}
	result.Token = request.Token
	if request.Version != syncControlVersion || request.Workspace != s.workspace || filepath.Clean(request.LocalRoot) != s.localRoot {
		result.Error = "control request does not match the mounted workspace and local root"
		return result, false
	}
	switch request.Operation {
	case syncControlOpSave, syncControlOpShutdown:
		result = s.saveWithResume(request, request.Operation != syncControlOpShutdown)
		return result, result.Success && request.Operation == syncControlOpShutdown
	case syncControlOpDetach:
		if s.active != nil {
			s.active.Stop()
			s.active = nil
		}
		result.Success = true
		return result, true
	case syncControlOpStatus:
		status := &syncStatus{}
		if d := s.active; d != nil {
			ctx, cancel := context.WithTimeout(s.ctx, time.Second)
			_, err := d.cfg.FS.Stat(ctx, "/")
			cancel()
			status.Connected = err == nil
			if err != nil {
				status.LastError = err.Error()
			}
			status.Queued = len(d.reconciler.uploadCh) + len(d.reconciler.downloadCh) + len(d.reconciler.uploadResCh) + len(d.reconciler.downloadResCh)
			if d.watcher != nil {
				status.Queued += len(d.watcher.out)
			}
			d.stateWriter.mu.Lock()
			status.TrackedUploads = len(d.reconciler.pendingUploads)
			status.Entries, _ = syncStateEntryCounts(d.stateWriter.state)
			d.stateWriter.mu.Unlock()
			d.log.mu.Lock()
			status.Conflicts = d.log.conflicts
			if status.LastError == "" {
				status.LastError = d.log.lastError
			}
			d.log.mu.Unlock()
		} else {
			status.LastError = "sync daemon is unavailable"
		}
		result.Status, result.Success = status, true
		return result, false
	default:
		result.Error = "unsupported sync control operation"
		return result, false
	}
}

func (s *syncSaveService) rememberMountIdentity() {
	if s.localRoot == "" && s.active != nil {
		s.workspace = s.active.cfg.Workspace
		s.localRoot = s.active.cfg.LocalRoot
	}
}

func (s *syncSaveService) save(request syncControlRequest) syncControlResult {
	return s.saveWithResume(request, true)
}

func (s *syncSaveService) saveWithResume(request syncControlRequest, resume bool) syncControlResult {
	s.rememberMountIdentity()
	result := syncControlResult{Version: syncControlVersion, Operation: request.Operation,
		Workspace: request.Workspace, LocalRoot: request.LocalRoot, Token: request.Token}
	fail := func(err error) syncControlResult { result.Error = err.Error(); return result }
	if s.token != "" && subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.token)) != 1 {
		return fail(errors.New("sync daemon identity token does not match"))
	}
	if request.Version != syncControlVersion {
		return fail(errors.New("unsupported sync save version"))
	}
	if request.Workspace != s.workspace || filepath.Clean(request.LocalRoot) != s.localRoot {
		return fail(errors.New("save request does not match the mounted workspace and local root"))
	}
	daemon := s.active
	if daemon == nil {
		return fail(errors.New("sync daemon is unavailable"))
	}
	if request.Path != "" || request.Content != "" || request.VersionID != "" || request.FileID != "" || request.Ordinal != 0 {
		return fail(errors.New("save operates on the entire mounted sync workspace"))
	}
	if daemon.cfg.Readonly && resume {
		return fail(errors.New("cannot save a read-only sync mount"))
	}
	if request.DeadlineUnixMilli <= 0 {
		return fail(errors.New("save requires a deadline"))
	}
	deadline := time.UnixMilli(request.DeadlineUnixMilli)
	if !time.Now().Before(deadline) {
		return fail(context.DeadlineExceeded)
	}
	ctx, cancel := context.WithDeadline(daemon.mutationContext(s.ctx), deadline)
	defer cancel()
	if daemon.cfg.Readonly {
		// Readers have no writes to flush. Join the retained worker drain and
		// return an explicit reader acknowledgement, never a save receipt.
		daemon.Stop()
		s.active = nil
		result.Success, result.ReadOnly = true, true
		return result
	}

	// Preserve the caller's local tree across the drain. An old inbound write
	// must never become the tree that this save silently acknowledges.
	var beforeDrain syncSaveTree
	finishMoves, err := daemon.reconciler.conflict.captureMoves(func() error {
		var scanErr error
		beforeDrain, scanErr = scanSyncSaveLocal(ctx, daemon.reconciler)
		return scanErr
	})
	if finishMoves != nil {
		defer finishMoves()
	}
	if err != nil {
		return fail(fmt.Errorf("scan local tree before save: %w", err))
	}

	// Cancelling the old generation stops producers as well as workers. Join
	// delayed delete senders and in-flight writes before inspecting the tree.
	// If a client has already timed out, no later success or new save is started.
	daemon.StopForSave(ctx)
	moves := finishMoves()
	s.active = nil
	s.applyStoppedUploadResults(daemon)
	var receipt syncSaveReceipt
	afterDrain, saveErr := scanSyncSaveLocal(ctx, daemon.reconciler)
	if saveErr == nil {
		saveErr = compareSyncSaveDrainTrees(daemon.reconciler.root, beforeDrain, afterDrain, moves)
		if saveErr != nil {
			saveErr = fmt.Errorf("local tree changed while pausing sync: %w", saveErr)
		}
	}
	if saveErr == nil {
		receipt, saveErr = saveSyncTree(ctx, daemon.reconciler, afterDrain)
	}
	if ctx.Err() != nil {
		saveErr = ctx.Err()
	}

	// Resume with new channels, watcher, subscription and workers. Old queued
	// operations must never run after a successful save receipt.
	if s.ctx.Err() == nil && (resume || saveErr != nil) {
		err := daemon.reconciler.checkLocalRoot()
		var fresh *syncDaemon
		if err == nil {
			fresh, err = newSyncDaemon(daemon.cfg)
		}
		if err == nil {
			fresh.full.requireRemountOnRootReplace = daemon.full.requireRemountOnRootReplace
			// Restart workers for the same directory, never adopt a replacement
			// that appeared while the old generation was being drained.
			fresh.reconciler.rootIdentity = daemon.reconciler.rootIdentity
			fresh.uploader.rootIdentity = daemon.reconciler.rootIdentity
			fresh.downloader.rootIdentity = daemon.reconciler.rootIdentity
			fresh.stateWriter.state = daemon.Snapshot()
			fresh.stateWriter.dirty = true
			err = fresh.reconciler.checkLocalRoot()
			if err == nil {
				err = fresh.StartSteadyStateOnly(s.ctx)
			}
		}
		if err != nil {
			saveErr = errors.Join(saveErr, fmt.Errorf("could not resume sync: %w", err))
		} else {
			s.active = fresh
			if saveErr != nil {
				// Shutdown discarded watcher timers and queued operations. Recover
				// from the retained baseline and actual tree, with ordinary conflict
				// handling and retries, even when no new watcher event arrives.
				fresh.watcher.requestRescan()
			}
		}
	}
	if saveErr != nil {
		return fail(saveErr)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	result.Success = true
	result.Save = &receipt
	return result
}

// Successful uploads whose result was waiting when shutdown began still
// establish ownership of remote paths. In particular, a renamed temporary file
// may otherwise look like an unrelated remote-only file to the strict save.
func (s *syncSaveService) applyStoppedUploadResults(d *syncDaemon) {
	apply := func(result uploadResult) {
		if result.Err != nil || result.Conflict {
			return
		}
		if result.RemoteStat != nil {
			d.stateWriter.mu.Lock()
			existing, ok := d.stateWriter.state.Entries[result.Op.Path]
			d.stateWriter.mu.Unlock()
			if ok && existing.RemoteMtimeMs > result.RemoteStat.Mtime {
				return
			}
		}
		d.reconciler.handleUploadResult(context.Background(), result)
	}
	for {
		select {
		case result := <-d.reconciler.uploadResCh:
			apply(result)
		default:
			goto retained
		}
	}
retained:
	for _, result := range d.uploader.stoppedResults {
		apply(result)
	}
	// Cancelled rename candidates cannot enqueue again; their tombstones remain
	// available to the save planner for detecting offline deletes.
	d.reconciler.renameMu.Lock()
	d.reconciler.renameCandidates = make(map[string]renameCandidate)
	d.reconciler.renameMu.Unlock()
}
