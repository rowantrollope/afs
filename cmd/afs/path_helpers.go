package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var runtimeStateDir string

func baseStateDir() string {
	if p := os.Getenv("AFS_STATE_DIR"); p != "" {
		abs, err := filepath.Abs(p)
		if err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return filepath.Join(home, ".afs-lite")
}
func stateDir() string {
	if runtimeStateDir != "" {
		return runtimeStateDir
	}
	return baseStateDir()
}
func expandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}
func processAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func normalizeMountPath(raw string) (string, error) {
	p, err := expandPath(raw)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("directory is required")
	}
	// Resolve symlinked ancestors too, so aliases cannot acquire separate owners.
	suffix := []string{}
	base := p
	for {
		resolved, e := filepath.EvalSymlinks(base)
		if e == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		parent := filepath.Dir(base)
		if parent == base {
			return "", e
		}
		suffix = append(suffix, filepath.Base(base))
		base = parent
	}
}

// Retained populated-root inspection from mount_commands.go.
func countMountableLocalEntries(root string) (int, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", root)
	}
	count := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.Name() == syncControlDirName || d.Name() == ".DS_Store" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		count++
		return nil
	})
	return count, err
}
