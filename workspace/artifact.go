package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rowantrollope/afs/mount/client"
)

const MaxArtifactBytes = 1 << 20

// StagingDirectory is reserved for unpublished server-owned artifacts. Callers
// displaying the workspace should exclude this directory and its descendants.
const StagingDirectory = ".afs-artifact-staging"

var (
	ErrInvalidPath     = errors.New("artifact path must be a clean relative path outside AFS staging")
	ErrInvalidArtifact = errors.New("artifact must be UTF-8 text of at most 1 MiB with ordinary permission bits")
)

func artifactPath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\r\n") || !utf8.ValidString(p) || path.Clean(p) != p {
		return "", ErrInvalidPath
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || part == StagingDirectory {
			return "", ErrInvalidPath
		}
	}
	return "/" + p, nil
}

// ensureParent walks without following links, creating only missing directory
// components. Every creation is fenced to the observed parent inode.
func (w *Workspace) ensureParent(ctx context.Context, p string, mode uint32) (*client.StatResult, error) {
	current, err := w.fs.Stat(ctx, "/")
	if err != nil {
		return nil, err
	}
	if current == nil || current.Type != "dir" {
		return nil, ErrWorkspaceChanged
	}
	parent := "/"
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if part == "" {
			continue
		}
		next := path.Join(parent, part)
		entry, err := w.fs.Stat(ctx, next)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			bound := client.WithExpectedParent(ctx, parent, current.Inode)
			if err := w.fs.MkdirMode(bound, next, mode); err != nil && !errors.Is(err, ErrAlreadyExists) {
				return nil, err
			}
			entry, err = w.fs.Stat(ctx, next)
			if err != nil {
				return nil, err
			}
		}
		if entry == nil || entry.Type != "dir" {
			return nil, ErrInvalidPath
		}
		parent, current = next, entry
	}
	return current, nil
}

// sameArtifact validates the inode before and after reading so a concurrent
// replacement is a conflict, never an acknowledgement for another file.
func (w *Workspace) sameArtifact(ctx context.Context, p string, data []byte) (bool, error) {
	before, err := w.fs.Stat(ctx, p)
	if err != nil {
		return false, err
	}
	if before == nil {
		return false, nil
	}
	if before.Type != "file" || before.Size != int64(len(data)) {
		return false, ErrAlreadyExists
	}
	body, err := w.fs.Cat(ctx, p)
	if err != nil {
		return false, err
	}
	after, err := w.fs.Stat(ctx, p)
	if err != nil {
		return false, err
	}
	if after == nil || after.Inode != before.Inode || after.Revision != before.Revision || !bytes.Equal(body, data) {
		return false, ErrAlreadyExists
	}
	return true, nil
}

// CreateArtifact publishes complete text atomically using the engine's
// RenameNoreplace operation. An identical-content retry returns false, nil;
// existing different content is never overwritten. mode only affects creation.
// A hard process termination can leave an unpublished staging file; this helper
// never removes another request's staging files or resets the shared tree.
func (w *Workspace) CreateArtifact(ctx context.Context, p string, data []byte, mode uint32) (bool, error) {
	final, err := artifactPath(p)
	if err != nil {
		return false, err
	}
	if len(data) > MaxArtifactBytes || !utf8.Valid(data) || mode&^uint32(0o777) != 0 {
		return false, ErrInvalidArtifact
	}
	finalParentPath := path.Dir(final)
	finalParent, err := w.ensureParent(ctx, finalParentPath, 0o755)
	if err != nil {
		return false, err
	}
	if same, err := w.sameArtifact(ctx, final, data); err != nil || same {
		return false, err
	}
	stageParentPath := "/" + StagingDirectory
	stageParent, err := w.ensureParent(ctx, stageParentPath, 0o700)
	if err != nil {
		return false, err
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, err
	}
	stage := stageParentPath + "/" + hex.EncodeToString(random[:])
	stageCtx := client.WithExpectedParent(ctx, stageParentPath, stageParent.Inode)
	owned, created, err := w.fs.CreateFile(stageCtx, stage, mode, true)
	if err != nil {
		return false, err
	}
	if !created || owned == nil {
		return false, ErrAlreadyExists
	}
	defer func() {
		// Cleanup is independent of request cancellation, but bounded and
		// still fenced to this workspace, parent, and our allocated inode.
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		current, err := w.fs.Stat(cleanup, stage)
		if err != nil || current == nil || current.Inode != owned.Inode {
			return
		}
		cleanup = client.WithExpectedParent(cleanup, stageParentPath, stageParent.Inode)
		_ = w.fs.Rm(client.WithExpectedStat(cleanup, current), stage)
	}()
	if err := w.fs.EchoCreate(client.WithExpectedStat(stageCtx, owned), stage, data, mode); err != nil {
		return false, err
	}
	complete, err := w.fs.Stat(ctx, stage)
	if err != nil {
		return false, err
	}
	if complete == nil || complete.Inode != owned.Inode || complete.Type != "file" || complete.Size != int64(len(data)) {
		return false, client.ErrWriteConflict
	}
	if same, err := w.sameArtifact(ctx, stage, data); err != nil || !same {
		if err != nil {
			return false, err
		}
		return false, client.ErrWriteConflict
	}
	// Use the revision observed before the content check. Any later source
	// edit invalidates Rename's expected-stat fence, even with equal size.
	publish := client.WithExpectedParent(stageCtx, finalParentPath, finalParent.Inode)
	publish = client.WithExpectedStat(publish, complete)
	if err := w.fs.Rename(publish, stage, final, client.RenameNoreplace); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			same, sameErr := w.sameArtifact(ctx, final, data)
			if sameErr != nil {
				return false, sameErr
			}
			if !same {
				return false, client.ErrWriteConflict
			}
			return false, nil
		}
		return false, err
	}
	return true, nil
}
