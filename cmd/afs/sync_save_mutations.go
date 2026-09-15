package main

import (
	"context"
	"path/filepath"

	"github.com/rowantrollope/afs/mount/client"
)

func removeSyncSaveEntry(ctx context.Context, r *reconciler, rel string, previous syncSaveEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.fs.Rm(client.WithExpectedStat(ctx, previous.remoteStat), absoluteRemotePath(rel)); err != nil {
		return err
	}
	return nil
}

func writeSyncSaveEntry(ctx context.Context, r *reconciler, rel string, entry, previous syncSaveEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remotePath := absoluteRemotePath(rel)
	// Mode-only changes retain file content and its version identity.
	if previous.Type != "" && sameSyncSaveContent(entry, previous) {
		return chmodSyncSaveEntry(ctx, r, rel, entry.Mode, previous)
	}
	switch entry.Type {
	case "dir":
		if err := r.fs.Mkdir(ctx, remotePath); err != nil {
			return err
		}
		// Mkdir uses 0755. Record creation before a separately fallible chmod.
		return finishSyncSaveCreate(ctx, r, rel, entry.Mode, syncSaveEntry{Type: "dir", Mode: 0o755})
	case "symlink":
		if err := r.fs.Ln(ctx, entry.Target, remotePath); err != nil {
			return err
		}
		return finishSyncSaveCreate(ctx, r, rel, entry.Mode, syncSaveEntry{Type: "symlink", Mode: 0o777, Target: entry.Target})
	case "file":
		data, err := readSyncSaveFile(ctx, filepath.Join(r.root, filepath.FromSlash(rel)), entry, r.maxFileBytes)
		if err != nil {
			return err
		}
		if err := r.fs.EchoCreate(client.WithExpectedStat(ctx, previous.remoteStat), remotePath, data, entry.Mode); err != nil {
			return err
		}
	}
	return nil
}

func chmodSyncSaveEntry(ctx context.Context, r *reconciler, rel string, mode uint32, previous syncSaveEntry) error {
	if previous.Mode == mode {
		return nil
	}
	if err := r.fs.Chmod(ctx, absoluteRemotePath(rel), mode); err != nil {
		return err
	}
	return nil
}

// Creation and chmod are separate Redis operations. If chmod fails, retain the
// creation's actual mode as the baseline so recovery/retry recognizes our own
// partial write, while still detecting an intervening remote change.
func finishSyncSaveCreate(ctx context.Context, r *reconciler, rel string, mode uint32, created syncSaveEntry) error {
	err := chmodSyncSaveEntry(ctx, r, rel, mode, created)
	if err != nil {
		r.state.mu.Lock()
		r.state.state.Entries[rel] = SyncEntry{Type: created.Type, Mode: created.Mode,
			Target: created.Target, Version: r.state.nextVersion()}
		r.state.dirty = true
		r.state.mu.Unlock()
	}
	return err
}
