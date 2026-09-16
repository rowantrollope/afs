package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// A full reconcile may create or update children of a read-only directory.
// Keep owner access until all local mutations finish, then restore modes from
// deepest to shallowest, including when the pass fails or is cancelled. The
// sync baseline continues to hold the final mode, never the temporary one.
type syncDirectoryModes struct {
	r        *reconciler
	prepared map[string]bool
	changed  map[string]*temporaryDirectoryMode
}

func newSyncDirectoryModes(r *reconciler) *syncDirectoryModes {
	return &syncDirectoryModes{r: r, prepared: make(map[string]bool), changed: make(map[string]*temporaryDirectoryMode)}
}

func (m *syncDirectoryModes) prepare(abs string) error {
	abs = filepath.Clean(abs)
	// The mount root's access policy is managed by the mount lifecycle.
	if abs == m.r.root || m.prepared[abs] {
		return nil
	}
	rel, err := filepath.Rel(m.r.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %s is outside sync root", abs)
	}
	if err := m.prepare(filepath.Dir(abs)); err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		// A later mkdir action creates it; prepare it after that action.
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("sync directory %s is not a directory", rel)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		marker := m.r.echo.holdDirectoryMode(filepath.ToSlash(rel), info)
		m.r.echo.markDir(filepath.ToSlash(rel), uint32(info.Mode().Perm()|0o700), info)
		if err := os.Chmod(abs, info.Mode()|0o700); err != nil {
			m.r.echo.releaseDirectoryMode(filepath.ToSlash(rel), marker)
			return fmt.Errorf("prepare sync directory %s: %w", rel, err)
		}
		m.changed[abs] = marker
	}
	m.prepared[abs] = true
	return nil
}

func (m *syncDirectoryModes) restore() error {
	paths := make([]string, 0, len(m.changed))
	for abs := range m.changed {
		paths = append(paths, abs)
	}
	// Descendants precede ancestors even when final modes remove search access.
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	var result error
	for _, abs := range paths {
		rel, _ := filepath.Rel(m.r.root, abs)
		changed, err := m.r.echo.restoreDirectoryMode(filepath.ToSlash(rel), abs, m.changed[abs])
		if changed {
			m.r.requestFullSweep()
		}
		result = errors.Join(result, err)
	}
	return result
}

// Keep failed restoration markers alive: event and queued-upload paths must
// not publish leftover temporary permissions before the next recovery retry.
func (e *echoSuppressor) restoreDirectoryMode(rel, abs string, marker *temporaryDirectoryMode) (bool, error) {
	fail := func(err error) (bool, error) {
		e.mu.Lock()
		if e.temporaryDirs[rel] == marker {
			marker.retry = true
		}
		e.mu.Unlock()
		return false, err
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
		e.releaseDirectoryMode(rel, marker)
		return true, nil
	}
	if err != nil {
		return fail(fmt.Errorf("stat sync directory %s: %w", abs, err))
	}
	if !os.SameFile(marker.before, info) || info.Mode() != marker.before.Mode()|0o700 {
		// Application replacements and distinct chmods retain their new modes.
		e.releaseDirectoryMode(rel, marker)
		return true, nil
	}
	e.markDir(rel, uint32(marker.before.Mode().Perm()), marker.before)
	if err := os.Chmod(abs, marker.before.Mode()); err != nil {
		return fail(fmt.Errorf("restore sync directory %s: %w", rel, err))
	}
	e.releaseDirectoryMode(rel, marker)
	return false, nil
}

func (e *echoSuppressor) retryDirectoryModes(root string) error {
	e.mu.Lock()
	pending := make(map[string]*temporaryDirectoryMode)
	for rel, marker := range e.temporaryDirs {
		if marker.retry {
			pending[rel] = marker
		}
	}
	e.mu.Unlock()
	paths := make([]string, 0, len(pending))
	for rel := range pending {
		paths = append(paths, rel)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	var result error
	for _, rel := range paths {
		_, err := e.restoreDirectoryMode(rel, filepath.Join(root, filepath.FromSlash(rel)), pending[rel])
		result = errors.Join(result, err)
	}
	return result
}
