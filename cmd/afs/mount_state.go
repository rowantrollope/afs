package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const mountRegistryVersion = 1

type mountRegistry struct {
	Version   int           `json:"version"`
	UpdatedAt time.Time     `json:"updated_at"`
	Mounts    []mountRecord `json:"mounts"`
}
type mountRecord struct {
	Backend         string    `json:"backend,omitempty"` // Empty in existing records means folder sync.
	ReadOnly        bool      `json:"read_only,omitempty"`
	UID             *uint32   `json:"uid,omitempty"`
	GID             *uint32   `json:"gid,omitempty"`
	AllowOther      bool      `json:"allow_other,omitempty"`
	ManagedSession  bool      `json:"managed_session,omitempty"`
	ControlPlaneURL string    `json:"control_plane_url,omitempty"`
	SessionName     string    `json:"session_name,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	User            string    `json:"user,omitempty"`
	Label           string    `json:"label,omitempty"`
	AgentVersion    string    `json:"agent_version,omitempty"`
	ID              string    `json:"id"`
	Workspace       string    `json:"workspace"`
	WorkspaceID     string    `json:"workspace_id"`
	LocalPath       string    `json:"local_path"`
	Redis           string    `json:"redis"` // Redacted endpoint; never credentials.
	RedisIdentity   string    `json:"redis_identity"`
	RedisKey        string    `json:"redis_key"`
	Generation      string    `json:"generation"`
	PID             int       `json:"pid"`
	Token           string    `json:"token"` // Local control capability; registry is mode 0600.
	RuntimeDir      string    `json:"runtime_dir"`
	SyncLog         string    `json:"sync_log"`
	StartedAt       time.Time `json:"started_at"`
}

func mountRegistryPath() string { return filepath.Join(baseStateDir(), "mounts.json") }
func loadMountRegistry() (mountRegistry, error) {
	reg := mountRegistry{Version: mountRegistryVersion, Mounts: []mountRecord{}}
	raw, err := os.ReadFile(mountRegistryPath())
	if errors.Is(err, os.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return reg, err
	}
	if err = json.Unmarshal(raw, &reg); err != nil {
		return reg, fmt.Errorf("parse mount registry: %w", err)
	}
	if reg.Version != mountRegistryVersion {
		return reg, errors.New("unsupported mount registry version")
	}
	return reg, nil
}

// Retained atomic replacement; callers serialize read/modify/write using lockRegistry.
func saveMountRegistry(reg mountRegistry) error {
	if err := os.MkdirAll(baseStateDir(), 0o700); err != nil {
		return err
	}
	reg.Version = mountRegistryVersion
	reg.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(baseStateDir(), ".mounts-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), mountRegistryPath())
}
func lockRegistry() (func(), error) {
	if err := os.MkdirAll(baseStateDir(), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(baseStateDir(), "mounts.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func mountByPath(reg mountRegistry, p string) (mountRecord, bool) {
	for _, rec := range reg.Mounts {
		if filepath.Clean(rec.LocalPath) == filepath.Clean(p) {
			return rec, true
		}
	}
	return mountRecord{}, false
}

// Resolve all identities together: a name colliding with another mount's path
// must not silently select the wrong tree. No Redis connection is required.
func resolveCommandMount(reg mountRegistry, target string) (mountRecord, error) {
	path, _ := expandPath(target)
	canonical, _ := normalizeMountPath(target)
	matches := []mountRecord{}
	for _, rec := range reg.Mounts {
		if target == rec.Workspace || target == rec.WorkspaceID || filepath.Clean(rec.LocalPath) == path || (canonical != "" && filepath.Clean(rec.LocalPath) == canonical) {
			matches = append(matches, rec)
		}
	}
	if len(matches) == 0 {
		return mountRecord{}, fmt.Errorf("no registered mount matches %q", target)
	}
	if len(matches) > 1 {
		return mountRecord{}, fmt.Errorf("%q matches multiple registered mounts; specify an exact directory", target)
	}
	return matches[0], nil
}

func removeMountByPath(reg *mountRegistry, p string) (mountRecord, bool) {
	for i, r := range reg.Mounts {
		if filepath.Clean(r.LocalPath) == filepath.Clean(p) {
			reg.Mounts = append(reg.Mounts[:i], reg.Mounts[i+1:]...)
			return r, true
		}
	}
	return mountRecord{}, false
}
func upsertMount(reg *mountRegistry, r mountRecord) {
	removeMountByPath(reg, r.LocalPath)
	reg.Mounts = append(reg.Mounts, r)
}
func mountPathConflict(reg mountRegistry, p string) (mountRecord, bool) {
	for _, r := range reg.Mounts {
		if r.LocalPath == p || pathContains(r.LocalPath, p) || pathContains(p, r.LocalPath) {
			return r, true
		}
	}
	return mountRecord{}, false
}
