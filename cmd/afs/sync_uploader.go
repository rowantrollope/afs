package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/client"
)

// uploadOpKind enumerates the mutations the uploader can apply to the live
// workspace root over the client.Client API.
type uploadOpKind int

const (
	opUploadFile uploadOpKind = iota + 1
	opUploadSymlink
	opUploadMkdir
	opUploadDelete
	opUploadChmod
	opUploadRename
)

// uploadOp is the work item the reconciler hands to the uploader. The
// reconciler stages content reads on the local filesystem and includes the
// hash so the uploader can detect drift between "what we read" and "what's
// remote right now" without rehashing.
type uploadOp struct {
	Kind          uploadOpKind
	Path          string // workspace-relative POSIX, no leading slash
	PrevPath      string // previous workspace-relative POSIX path for renames
	AbsPath       string // absolute local path (for diagnostic logging)
	Content       []byte // file body, only for non-chunked opUploadFile
	Mode          uint32
	Symlink       string // target, only for opUploadSymlink
	LocalHash     string // sha256 of Content or compositeHash for chunked
	LocalIdentity string
	LocalMtimeMs  int64 // local metadata captured when the content was staged
	StoredEntry   SyncEntry
	HasStored     bool
	Tracked       bool   // reconciler keeps this operation pending through result application
	RenameVersion uint64 // provisional destination baseline installed when a rename was staged
	// Chunked upload fields (set when file > chunkThreshold).
	Chunked     bool
	FileSize    int64
	ChunkSize   int
	ChunkHashes []string // complete new manifest
	DirtyChunks []int    // indices of changed chunks
}

// uploadResult tells the reconciler how the upload landed so it can mark the
// SyncEntry up to date or trigger a conflict resolution loop.
type uploadResult struct {
	Op             uploadOp
	Err            error
	Conflict       bool
	Skipped        bool // queued local content changed; reconcile again without updating the baseline
	RemoteHashSeen string
	RemoteStat     *client.StatResult
}

// uploader runs in its own goroutine, draining ops from the reconciler.
type uploader struct {
	stopCh         <-chan struct{}
	stoppedResults []uploadResult
	runContext     context.Context
	fs             client.Client
	results        chan<- uploadResult
	maxFileBytes   int64
	readonly       bool
	log            *syncLogger
	localRoot      string
	rootIdentity   string
}

func newUploader(fs client.Client, results chan<- uploadResult, maxFileBytes int64, readonly bool, log *syncLogger) *uploader {
	if maxFileBytes <= 0 {
		maxFileBytes = 64 * 1024 * 1024
	}
	return &uploader{fs: fs, results: results, maxFileBytes: maxFileBytes, readonly: readonly, log: log}
}

// attachChangelog wires the uploader to emit one controlplane ChangeEntry per
// successful upload op. Session + workspace storage identity is baked in at
// start so each entry is attributable without extra plumbing per-op.

// emitChange writes one changelog row for op `result`. Called only when the
// upload landed successfully (no error, no conflict). Safe to call with the
// changelog unmounted — it no-ops.

// run drains in until ctx is cancelled. Each op is processed serially so the
// reconciler can rely on op-completion ordering when applying state updates.
func (u *uploader) run(ctx context.Context, in <-chan uploadOp) {
	if u.runContext == nil {
		u.runContext = ctx
	}
	for {
		select {
		case <-ctx.Done():
			return
		case op, ok := <-in:
			if !ok || ctx.Err() != nil {
				return
			}
			if u.readonly {
				u.send(uploadResult{Op: op, Err: errors.New("uploader is read-only")})
				continue
			}
			u.process(u.runContext, op)
		}
	}
}

func (u *uploader) process(ctx context.Context, op uploadOp) {
	if u.localRoot != "" {
		if err := checkSyncLocalRoot(u.localRoot, u.rootIdentity); err != nil {
			u.send(uploadResult{Op: op, Err: err})
			return
		}
	}
	switch op.Kind {
	case opUploadFile:
		u.processFile(ctx, op)
	case opUploadSymlink:
		u.processSymlink(ctx, op)
	case opUploadMkdir:
		u.processMkdir(ctx, op)
	case opUploadDelete:
		u.processDelete(ctx, op)
	case opUploadChmod:
		u.processChmod(ctx, op)
	case opUploadRename:
		u.processRename(ctx, op)
	default:
		u.send(uploadResult{Op: op, Err: fmt.Errorf("unknown upload op kind: %d", op.Kind)})
	}
}

func (u *uploader) processFile(ctx context.Context, op uploadOp) {
	if op.Chunked {
		u.processChunkedFile(ctx, op)
		return
	}
	if !u.queuedFileCurrent(op) {
		return
	}
	if int64(len(op.Content)) > u.maxFileBytes {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("file %s is %d bytes, exceeds sync size cap of %d bytes", op.Path, len(op.Content), u.maxFileBytes)})
		return
	}

	mode := op.Mode
	if mode == 0 {
		mode = 0o644
	}
	// Recovery may have uploaded this content while the operation waited.
	// Only divergent remote content is a conflict with the stored baseline.
	remotePath := absoluteRemotePath(op.Path)
	stat, statErr := u.fs.Stat(ctx, remotePath)
	if statErr != nil && !isClientNotFound(statErr) {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("stat remote %s: %w", op.Path, statErr)})
		return
	}
	if stat != nil {
		remoteData, err := u.fs.Cat(ctx, remotePath)
		if err != nil {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("read remote %s: %w", op.Path, err)})
			return
		}
		if !u.queuedFileCurrent(op) {
			return
		}
		remoteHash := sha256Hex(remoteData)
		if remoteHash == op.LocalHash {
			u.finishMatchingFileUpload(ctx, op, remotePath, mode)
			return
		}
		matchesBaseline := remoteHash == op.StoredEntry.RemoteHash
		if !matchesBaseline && op.StoredEntry.ChunkSize > 0 {
			matchesBaseline = compositeHash(uploadChunkHashes(remoteData, op.StoredEntry.ChunkSize)) == op.StoredEntry.RemoteHash
		}
		if !op.HasStored || (op.StoredEntry.RemoteHash != "" && !matchesBaseline) {
			u.send(uploadResult{Op: op, Conflict: true, RemoteHashSeen: remoteHash, RemoteStat: stat})
			return
		}
	}

	if !u.queuedFileCurrent(op) {
		return
	}
	// Echo handles both create-and-write and write-existing in a single
	// round trip; the native client falls back to createFile when the
	// path is missing. We don't pre-create with CreateFile because that
	// leaves the inode briefly empty and other watchers can race against
	// the empty state.
	if err := u.fs.Echo(client.WithExpectedStat(ctx, stat), remotePath, op.Content); err != nil {
		if errors.Is(err, client.ErrWriteConflict) {
			u.send(uploadResult{Op: op, Conflict: true})
			return
		}
		u.send(uploadResult{Op: op, Err: fmt.Errorf("write remote %s: %w", op.Path, err)})
		return
	}
	u.finishFileUpload(ctx, op, remotePath, mode)
}

func (u *uploader) processChunkedFile(ctx context.Context, op uploadOp) {
	if !u.queuedFileCurrent(op) {
		return
	}
	remotePath := absoluteRemotePath(op.Path)
	stat, statErr := u.fs.Stat(ctx, remotePath)
	if statErr != nil && !isClientNotFound(statErr) {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("stat remote %s: %w", op.Path, statErr)})
		return
	}

	// Drift check: compare remote chunk manifest against what we stored.
	_, remoteHashes, err := u.fs.ChunkMeta(ctx, remotePath)
	remoteMissing := errors.Is(err, redis.Nil) || isClientNotFound(err)
	if err != nil && !remoteMissing {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("chunk meta %s: %w", op.Path, err)})
		return
	}
	// Echo uploads can leave absent or stale chunk metadata. Derive hashes
	// from current bytes and accept either representation of the baseline.
	if err == nil {
		remoteData, readErr := u.fs.Cat(ctx, remotePath)
		if readErr != nil {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("read remote %s: %w", op.Path, readErr)})
			return
		}
		if !u.queuedFileCurrent(op) {
			return
		}
		remoteHash := sha256Hex(remoteData)
		remoteHashes = uploadChunkHashes(remoteData, op.ChunkSize)
		remoteComposite := compositeHash(remoteHashes)
		if remoteComposite == op.LocalHash {
			u.finishMatchingFileUpload(ctx, op, remotePath, op.Mode)
			return
		}
		baselineComposite := remoteComposite
		if op.StoredEntry.ChunkSize > 0 && op.StoredEntry.ChunkSize != op.ChunkSize {
			baselineComposite = compositeHash(uploadChunkHashes(remoteData, op.StoredEntry.ChunkSize))
		}
		if !op.HasStored || (op.StoredEntry.RemoteHash != "" && remoteHash != op.StoredEntry.RemoteHash && baselineComposite != op.StoredEntry.RemoteHash) {
			u.send(uploadResult{Op: op, Conflict: true, RemoteHashSeen: remoteComposite, RemoteStat: stat})
			return
		}
	}

	// A missing remote file has no unchanged chunks to preserve. Recreate
	// its complete contents even if this operation was planned as a delta.
	dirtyChunks := op.DirtyChunks
	if remoteMissing {
		dirtyChunks = make([]int, len(op.ChunkHashes))
		for i := range dirtyChunks {
			dirtyChunks[i] = i
		}
	}

	// Upload dirty chunks in batches.
	chunks := make(map[int][]byte, len(dirtyChunks))
	for _, idx := range dirtyChunks {
		data, err := readChunkFromDisk(op.AbsPath, idx, op.ChunkSize)
		if err != nil {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("read chunk %d of %s: %w", idx, op.Path, err)})
			return
		}
		if idx >= len(op.ChunkHashes) || sha256Hex(data) != op.ChunkHashes[idx] {
			u.send(uploadResult{Op: op, Skipped: true})
			return
		}
		chunks[idx] = data
	}
	if !u.queuedFileCurrent(op) {
		return
	}

	// The client publishes a complete new file or complete delta atomically.
	// Precreating an empty inode would expose an incomplete publication.
	if err := u.fs.WriteChunks(client.WithExpectedStat(ctx, stat), remotePath, chunks, op.ChunkSize, op.FileSize, op.ChunkHashes); err != nil {
		if errors.Is(err, client.ErrWriteConflict) {
			u.send(uploadResult{Op: op, Conflict: true})
			return
		}
		u.send(uploadResult{Op: op, Err: fmt.Errorf("write chunks %s: %w", op.Path, err)})
		return
	}

	u.finishFileUpload(ctx, op, remotePath, op.Mode)
}

func (u *uploader) finishFileUpload(ctx context.Context, op uploadOp, remotePath string, mode uint32) {
	// Retain mode propagation even when recovery already uploaded the bytes.
	_ = u.fs.Chmod(ctx, remotePath, mode)
	newStat, err := u.fs.Stat(ctx, remotePath)
	if err != nil {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("post-write stat %s: %w", op.Path, err)})
		return
	}
	u.send(uploadResult{Op: op, RemoteHashSeen: op.LocalHash, RemoteStat: newStat})
}

func (u *uploader) finishMatchingFileUpload(ctx context.Context, op uploadOp, remotePath string, mode uint32) {
	stat, err := u.fs.Stat(ctx, remotePath)
	if err != nil {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("stat matching remote %s: %w", op.Path, err)})
		return
	}
	if !u.queuedFileCurrent(op) {
		return
	}
	if stat == nil || stat.Type != "file" {
		u.send(uploadResult{Op: op, Skipped: true})
		return
	}
	if op.HasStored && op.StoredEntry.Mode != 0 && stat.Mode != op.StoredEntry.Mode && stat.Mode != mode {
		if mode == op.StoredEntry.Mode {
			// Only the remote mode changed. A rescan can apply that change.
			u.send(uploadResult{Op: op, Skipped: true})
		} else {
			u.send(uploadResult{Op: op, Conflict: true, RemoteHashSeen: op.LocalHash, RemoteStat: stat})
		}
		return
	}
	u.finishFileUpload(ctx, op, remotePath, mode)
}

func (u *uploader) queuedFileCurrent(op uploadOp) bool {
	// Older callers did not capture metadata with the staged content.
	if op.LocalMtimeMs == 0 {
		return true
	}
	info, err := os.Lstat(op.AbsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("stat queued file %s: %w", op.Path, err)})
		return false
	}
	size := int64(len(op.Content))
	if op.Chunked {
		size = op.FileSize
	}
	if err != nil || !info.Mode().IsRegular() || info.ModTime().UnixMilli() != op.LocalMtimeMs || info.Size() != size || uint32(info.Mode().Perm()) != op.Mode || op.LocalIdentity != "" && localFileIdentity(info) != op.LocalIdentity {
		u.send(uploadResult{Op: op, Skipped: true})
		return false
	}
	if !op.Chunked {
		data, readErr := os.ReadFile(op.AbsPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("read queued file %s: %w", op.Path, readErr)})
			return false
		}
		if readErr != nil || !bytes.Equal(data, op.Content) {
			u.send(uploadResult{Op: op, Skipped: true})
			return false
		}
	}
	return true
}

func uploadChunkHashes(data []byte, chunkSize int) []string {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	var hashes []string
	for offset := 0; offset < len(data); offset += chunkSize {
		end := min(offset+chunkSize, len(data))
		hashes = append(hashes, sha256Hex(data[offset:end]))
	}
	return hashes
}

func (u *uploader) processSymlink(ctx context.Context, op uploadOp) {
	remotePath := absoluteRemotePath(op.Path)
	// Best-effort delete first; Ln on existing path returns an error.
	if existing, err := u.fs.Stat(ctx, remotePath); err == nil && existing != nil {
		if rmErr := u.fs.Rm(ctx, remotePath); rmErr != nil {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("replace symlink %s: %w", op.Path, rmErr)})
			return
		}
	}
	if err := u.fs.Ln(ctx, op.Symlink, remotePath); err != nil {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("create symlink %s: %w", op.Path, err)})
		return
	}
	stat, _ := u.fs.Stat(ctx, remotePath)
	u.send(uploadResult{Op: op, RemoteStat: stat})
}

func (u *uploader) processMkdir(ctx context.Context, op uploadOp) {
	remotePath := absoluteRemotePath(op.Path)
	if err := u.fs.Mkdir(ctx, remotePath); err != nil {
		// If it already exists we treat as success (the live root may have
		// the dir from a prior run).
		if !isClientAlreadyExists(err) {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("mkdir remote %s: %w", op.Path, err)})
			return
		}
	}
	stat, _ := u.fs.Stat(ctx, remotePath)
	u.send(uploadResult{Op: op, RemoteStat: stat})
}

func (u *uploader) processDelete(ctx context.Context, op uploadOp) {
	if u.localRoot != "" {
		if err := checkSyncLocalRoot(u.localRoot, u.rootIdentity); err != nil {
			u.send(uploadResult{Op: op, Err: err})
			return
		}
	}
	if op.AbsPath != "" {
		_, err := os.Lstat(op.AbsPath)
		if err == nil {
			u.send(uploadResult{Op: op, Skipped: true})
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			u.send(uploadResult{Op: op, Err: fmt.Errorf("stat queued delete %s: %w", op.Path, err)})
			return
		}
	}
	remotePath := absoluteRemotePath(op.Path)
	stat, err := u.fs.Stat(ctx, remotePath)
	if err != nil && !errors.Is(err, redis.Nil) && !isClientNotFound(err) {
		u.send(uploadResult{Op: op, Err: err})
		return
	}
	if stat != nil {
		matches, err := syncRemoteMatchesStored(ctx, u.fs, remotePath, stat, op.StoredEntry, op.HasStored)
		if err != nil {
			u.send(uploadResult{Op: op, Err: err})
			return
		}
		if !matches {
			u.send(uploadResult{Op: op, Conflict: true, RemoteStat: stat})
			return
		}
	}
	if err := u.fs.Rm(client.WithExpectedStat(ctx, stat), remotePath); err != nil && !errors.Is(err, redis.Nil) && !isClientNotFound(err) {
		if errors.Is(err, client.ErrWriteConflict) {
			u.send(uploadResult{Op: op, Conflict: true})
			return
		}
		u.send(uploadResult{Op: op, Err: fmt.Errorf("rm remote %s: %w", op.Path, err)})
		return
	}
	u.send(uploadResult{Op: op})
}

func syncRemoteMatchesStored(ctx context.Context, fs client.Client, remotePath string, stat *client.StatResult, stored SyncEntry, hasStored bool) (bool, error) {
	if stat == nil {
		return true, nil
	}
	if !hasStored || stat.Type != stored.Type {
		return false, nil
	}
	if stored.Mode != 0 && stat.Mode&0777 != stored.Mode&0777 {
		return false, nil
	}
	switch stat.Type {
	case "file":
		data, err := fs.Cat(ctx, remotePath)
		if err != nil {
			return false, err
		}
		if sha256Hex(data) == stored.RemoteHash {
			return true, nil
		}
		return stored.ChunkSize > 0 && compositeHash(uploadChunkHashes(data, stored.ChunkSize)) == stored.RemoteHash, nil
	case "symlink":
		target, err := fs.Readlink(ctx, remotePath)
		return target == stored.Target, err
	case "dir":
		return true, nil
	}
	return false, nil
}

func (u *uploader) processRename(ctx context.Context, op uploadOp) {
	if strings.TrimSpace(op.PrevPath) == "" {
		u.send(uploadResult{Op: op, Err: errors.New("rename upload missing previous path")})
		return
	}
	srcPath := absoluteRemotePath(op.PrevPath)
	dstPath := absoluteRemotePath(op.Path)
	before, err := u.fs.Stat(ctx, srcPath)
	if err != nil && !isClientNotFound(err) {
		u.send(uploadResult{Op: op, Err: err})
		return
	}
	if err := u.fs.Rename(client.WithExpectedStat(ctx, before), srcPath, dstPath, client.RenameNoreplace); err != nil {
		if errors.Is(err, client.ErrWriteConflict) || isClientAlreadyExists(err) || errors.Is(err, redis.Nil) || isClientNotFound(err) {
			// A source or destination parent can disappear before a queued
			// rename executes. Reconcile the current local destination rather
			// than retaining a baseline for a rename that did not complete.
			u.send(uploadResult{Op: op, Skipped: true})
			return
		}
		u.send(uploadResult{Op: op, Err: fmt.Errorf("rename remote %s -> %s: %w", op.PrevPath, op.Path, err)})
		return
	}
	stat, err := u.fs.Stat(ctx, dstPath)
	if err != nil {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("post-rename stat %s: %w", op.Path, err)})
		return
	}
	u.send(uploadResult{Op: op, RemoteStat: stat})
}

func (u *uploader) processChmod(ctx context.Context, op uploadOp) {
	remotePath := absoluteRemotePath(op.Path)
	if err := u.fs.Chmod(ctx, remotePath, op.Mode); err != nil {
		u.send(uploadResult{Op: op, Err: fmt.Errorf("chmod remote %s: %w", op.Path, err)})
		return
	}
	stat, _ := u.fs.Stat(ctx, remotePath)
	u.send(uploadResult{Op: op, RemoteStat: stat})
}

func (u *uploader) send(r uploadResult) {
	if u.results == nil {
		return
	}
	select {
	case u.results <- r:
	case <-u.stopCh:
		// Preserve the final result for save after every old worker joins.
		u.stoppedResults = append(u.stoppedResults, r)
	}
}

// absoluteRemotePath converts a workspace-relative POSIX path to the
// absolute form expected by client.Client (always rooted at "/"). Empty
// strings collapse to "/".
func absoluteRemotePath(rel string) string {
	if rel == "" || rel == "." {
		return "/"
	}
	if rel[0] == '/' {
		return rel
	}
	return "/" + rel
}

// isClientNotFound is a deliberately string-matching helper. The native client
// returns plain errors for "no such inode" / "no such path"; we don't want to
// import the internal package just to type-assert. Adjust the matched
// substrings if the client changes its error format.
func isClientNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "no such file", "not found", "ENOENT", "does not exist")
}

func isClientAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "exists", "EEXIST")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		if indexOfFold(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOfFold is a small case-insensitive substring search. We avoid
// strings.EqualFold/ToLower allocations on the hot path.
func indexOfFold(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			c1 := s[i+j]
			c2 := sub[j]
			if 'A' <= c1 && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if 'A' <= c2 && c2 <= 'Z' {
				c2 += 'a' - 'A'
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
