package main

import (
	"fmt"
)

// defaultSyncFileSizeCapMB is raised from 64 to 2048 (2 GB) now that
// chunked delta sync avoids loading entire files into memory.
const defaultSyncFileSizeCapMB = 2048

const (
	defaultSyncWatcherQueueCapacity = 1024
	maxSyncWatcherQueueCapacity     = 1 << 20
)

func validateSyncWatcherQueueCapacity(capacity int) error {
	if capacity < 0 || capacity > maxSyncWatcherQueueCapacity {
		return fmt.Errorf("sync.watcherQueueCapacity must be between 0 and %d (0 uses the default %d)", maxSyncWatcherQueueCapacity, defaultSyncWatcherQueueCapacity)
	}
	return nil
}

func syncWatcherQueueCapacity(cfg config) int {
	if cfg.SyncWatcherQueueCapacity == 0 {
		return defaultSyncWatcherQueueCapacity
	}
	return cfg.SyncWatcherQueueCapacity
}

// syncSizeCapBytes returns the configured per-file size cap in bytes, falling
// back to the default if unset or non-positive.
func syncSizeCapBytes(cfg config) int64 {
	mb := cfg.SyncFileSizeCapMB
	if mb <= 0 {
		mb = defaultSyncFileSizeCapMB
	}
	return int64(mb) * 1024 * 1024
}
