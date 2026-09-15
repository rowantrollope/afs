package main

import (
	"fmt"
	"os"
)

// A vanished or unreadable mount root is never evidence of intentional file
// deletions. Check again when applying queued deletes, not only while scanning.
func checkSyncLocalRoot(root, expectedIdentity string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("local sync root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local sync root %s is not a directory", root)
	}
	if expectedIdentity != "" && localFileIdentity(info) != expectedIdentity {
		return fmt.Errorf("local sync root was replaced; preserve local files and remount")
	}
	dir, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("open local sync root: %w", err)
	}
	return dir.Close()
}

func (r *reconciler) checkLocalRoot() error {
	return checkSyncLocalRoot(r.root, r.rootIdentity)
}
