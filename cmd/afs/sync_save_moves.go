package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// An acknowledged conflict move changes a path while preserving the caller's
// exact candidate. Every other change retains the ordinary strict save guard.
func compareSyncSaveDrainTrees(root string, before, after syncSaveTree, moves []syncConflictMove) error {
	expected := make(syncSaveTree, len(before))
	for rel, entry := range before {
		expected[rel] = entry
	}
	moved := make(map[string]bool)
	for _, move := range moves {
		source, err := syncSaveMoveRelative(root, move.source)
		if err != nil {
			return err
		}
		target, err := syncSaveMoveRelative(root, move.target)
		if err != nil {
			return err
		}
		entry, exists := expected[source]
		if !exists {
			// The move may have completed before the initial scan or concern
			// an ignored path. Tree equality still rejects new unscanned data.
			continue
		}
		if entry.identity == "" || entry.identity != move.identity {
			return fmt.Errorf("changed conflict source %s", source)
		}
		var paths []string
		for rel := range expected {
			if rel == source || strings.HasPrefix(rel, source+"/") {
				paths = append(paths, rel)
			}
		}
		for _, rel := range paths {
			destination := target + strings.TrimPrefix(rel, source)
			if _, exists := expected[destination]; exists {
				return fmt.Errorf("conflict destination already present: %s", destination)
			}
			expected[destination] = expected[rel]
			delete(expected, rel)
			delete(moved, rel)
			moved[destination] = true
		}
	}
	if err := compareSyncSaveTrees(expected, after); err != nil {
		return err
	}
	for rel := range moved {
		if after[rel].identity != expected[rel].identity {
			return fmt.Errorf("replaced conflict candidate %s", rel)
		}
	}
	return nil
}

func syncSaveMoveRelative(root, absolute string) (string, error) {
	rel, err := filepath.Rel(root, absolute)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("conflict move is outside the saved tree: %s", absolute)
	}
	return filepath.ToSlash(rel), nil
}
