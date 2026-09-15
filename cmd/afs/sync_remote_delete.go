package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/client"
)

func isSyncRemoteMissingError(err error) bool {
	return errors.Is(err, redis.Nil) || isClientNotFound(err)
}

// A queued event or recovery scan is only an observation of absence. Recheck
// before touching the local tree, then let the state-version guard below reject
// any newer baseline installed while that remote read was in flight.
func applyConfirmedSyncRemoteDelete(ctx context.Context, remote client.Client, root, abs string, stored SyncEntry, hasStored bool, state *stateWriter, namer *conflictNamer, echo *echoSuppressor, checkRunning func() error) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := checkRunning(); err != nil {
		return "", false, err
	}
	if remote == nil {
		return "", false, errors.New("remote deletion requires a connected client")
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false, err
	}
	remote.InvalidateCache()
	stat, err := remote.Stat(ctx, absoluteRemotePath(filepath.ToSlash(rel)))
	if err != nil && !isSyncRemoteMissingError(err) {
		return "", false, fmt.Errorf("confirm remote deletion %s: %w", rel, err)
	}
	if stat != nil {
		return "", true, nil
	}
	return applySyncRemoteDelete(ctx, root, abs, stored, hasStored, state, namer, echo, checkRunning)
}

// Complete an inbound deletion and forget its baseline in one state critical
// section. A scan must never see our local removal as a new outbound deletion.
// Keeping a tombstone here would also erase a later identical remote recreate.
func applySyncRemoteDelete(ctx context.Context, root, abs string, stored SyncEntry, hasStored bool, state *stateWriter, namer *conflictNamer, echo *echoSuppressor, checkRunning func() error) (string, bool, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false, err
	}
	rel = filepath.ToSlash(rel)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		current, exists := state.state.Entries[rel]
		if exists != hasStored || exists && current.Version != stored.Version {
			return "", true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := checkRunning(); err != nil {
		return "", false, err
	}
	local, err := snapshotDownloadLocal(abs, stored)
	if err != nil {
		return "", false, err
	}
	conflictPath, err := preserveLocalDeleteConflict(ctx, root, abs, stored, hasStored, func(path string) (SyncEntry, bool) {
		if state == nil {
			return SyncEntry{}, false
		}
		entry, exists := state.state.Entries[path]
		return entry, exists
	}, namer, checkRunning)
	if err != nil {
		return conflictPath, false, err
	}
	if err := ctx.Err(); err != nil {
		return conflictPath, false, err
	}
	if err := checkRunning(); err != nil {
		return conflictPath, false, err
	}
	if conflictPath == "" {
		if !local.unchanged(abs) {
			return "", true, nil
		}
		if echo != nil {
			echo.markDelete(rel)
		}
		if local.info != nil && local.info.IsDir() {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
		}
		if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	} else if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		// Preserve an application recreation after the conflict copy moved.
		return conflictPath, true, err
	}
	if state != nil {
		for path := range state.state.Entries {
			if path == rel || strings.HasPrefix(path, rel+"/") {
				delete(state.state.Entries, path)
			}
		}
		state.dirty = true
	}
	return conflictPath, false, nil
}
