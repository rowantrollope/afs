package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// conflictNamer assigns ".conflict-<host>-<ts>" suffixes to local files when
// the daemon detects bidirectional divergence. The suffix carries the host so
// that two daemons running against the same workspace from different machines
// don't fight over the same conflict copy.
type conflictNamer struct {
	hostname string
	counter  uint64
}

func newConflictNamer() *conflictNamer {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	host = sanitizeConflictHost(host)
	return &conflictNamer{hostname: host}
}

// sanitizeConflictHost strips characters that would be awkward in filenames
// across platforms. We keep alnum, dot, dash, and underscore.
func sanitizeConflictHost(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

// conflictPath produces a unique path for the local copy that just lost a
// conflict. The format mirrors Dropbox: "<base>.conflict-<host>-<timestamp>".
// The counter guarantees uniqueness if two conflicts on the same path land in
// the same millisecond.
func (n *conflictNamer) conflictPath(localAbsPath string) string {
	ts := time.Now().UTC().Format("20060102T150405.000")
	seq := atomic.AddUint64(&n.counter, 1)
	suffix := fmt.Sprintf(".conflict-%s-%s-%d", n.hostname, ts, seq)
	dir := filepath.Dir(localAbsPath)
	base := filepath.Base(localAbsPath)
	return filepath.Join(dir, base+suffix)
}

// moveLocalToConflict renames a local file to its conflict-copy path so the
// downloader can write the remote-wins version into the original location.
// If the source does not exist (e.g. user already deleted it), this is a
// no-op.
func moveLocalToConflict(namer *conflictNamer, localAbsPath string) (string, error) {
	if _, err := os.Lstat(localAbsPath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	target := namer.conflictPath(localAbsPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(localAbsPath, target); err != nil {
		return "", err
	}
	return target, nil
}

// A remote deletion still conflicts with unsynchronized local content. Reuse
// the existing conflict-copy policy before removing a modified file/subtree.
func preserveLocalDeleteConflict(ctx context.Context, root, abs string, stored SyncEntry, hasStored bool, state *stateWriter, namer *conflictNamer) (string, error) {
	changed := false
	err := filepath.WalkDir(abs, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		baseline, known := stored, hasStored
		if current != abs {
			rel, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			known = false
			if state != nil {
				state.mu.Lock()
				baseline, known = state.state.Entries[filepath.ToSlash(rel)]
				state.mu.Unlock()
			}
		}
		// A deletion event may have marked this entry before the downloader
		// runs. Its content fields still describe the last synchronized bytes.
		baseline.Deleted = false
		local, err := snapshotDownloadLocal(current, baseline)
		if err != nil {
			return err
		}
		if !local.matchesStored(downloadOp{StoredEntry: baseline, HasStored: known}) || !local.unchanged(current) {
			changed = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || !changed {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return moveLocalToConflict(namer, abs)
}

// conflictCheckpointName produces the auto-checkpoint id used when conflicts
// are detected. It is bounded by the existing checkpoint name validator
// (alnum + dash + underscore + dot only).
func conflictCheckpointName(workspace string) string {
	ts := time.Now().UTC().Format("20060102-150405")
	return "conflict-" + workspace + "-" + ts
}

// triggerConflictCheckpoint creates an auto-checkpoint for the workspace via
// the existing live-workspace save path. Errors are logged but do not block
// the daemon — the conflict copy on disk is the user's primary recovery path.
func triggerConflictCheckpoint(ctx context.Context, store *afsStore, workspace string) {
	if store == nil {
		return
	}
	name := conflictCheckpointName(workspace)
	saved, err := saveLiveWorkspaceCheckpoint(ctx, store, workspace, name, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afs sync: auto-checkpoint %s failed: %v\n", name, err)
		return
	}
	if saved {
		fmt.Fprintf(os.Stderr, "afs sync: created conflict checkpoint %s\n", name)
	}
}
