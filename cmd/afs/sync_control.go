package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/client"
)

const (
	syncControlVersion           = 1
	syncControlDirName           = ".afs-lite-sync"
	syncControlRequestsDirName   = ".afs-lite-sync/requests"
	syncControlResultsDirName    = ".afs-lite-sync/results"
	syncControlOpCreateExclusive = "create-exclusive"
	syncControlOpUndelete        = "undelete"
	syncControlOpSave            = "save"
	syncControlOpStatus          = "status"
	syncControlOpShutdown        = "shutdown"
	syncControlOpDetach          = "detach"
	defaultSyncControlTimeout    = 10 * time.Second
)

type syncControlRequest struct {
	Version           int    `json:"version"`
	Operation         string `json:"operation"`
	Path              string `json:"path"`
	Content           string `json:"content"`
	VersionID         string `json:"version_id,omitempty"`
	FileID            string `json:"file_id,omitempty"`
	Ordinal           int64  `json:"ordinal,omitempty"`
	DeadlineUnixMilli int64  `json:"deadline_unix_milli,omitempty"`
	Workspace         string `json:"workspace,omitempty"`
	LocalRoot         string `json:"local_root,omitempty"`
	Token             string `json:"token,omitempty"`
}

type syncControlResult struct {
	Version   int              `json:"version"`
	Operation string           `json:"operation"`
	Path      string           `json:"path"`
	Success   bool             `json:"success"`
	Bytes     int              `json:"bytes,omitempty"`
	VersionID string           `json:"version_id,omitempty"`
	SourceID  string           `json:"source_id,omitempty"`
	Error     string           `json:"error,omitempty"`
	Workspace string           `json:"workspace,omitempty"`
	LocalRoot string           `json:"local_root,omitempty"`
	Save      *syncSaveReceipt `json:"save,omitempty"`
	Token     string           `json:"token,omitempty"`
	Status    *syncStatus      `json:"status,omitempty"`
	ReadOnly  bool             `json:"read_only,omitempty"`
}

// Queued counts are an observation, not a completed flush. Only Save supplies
// a verified receipt after joining all workers and checking actual bytes.
type syncStatus struct {
	Connected      bool   `json:"connected"`
	Queued         int    `json:"queued"`
	TrackedUploads int    `json:"tracked_uploads"`
	Entries        int    `json:"entries"`
	Conflicts      uint64 `json:"conflicts"`
	LastError      string `json:"last_error,omitempty"`
}

func syncControlRequestPath(root, requestID string) string {
	return filepath.Join(root, syncControlDirName, "requests", requestID+".json")
}

func syncControlResultPath(root, requestID string) string {
	return filepath.Join(root, syncControlDirName, "results", requestID+".json")
}

func runSyncControlRequest(localRoot string, request syncControlRequest, timeout time.Duration) (syncControlResult, error) {
	result, err := exchangeSyncControlRequest(localRoot, request, timeout)
	if err != nil {
		return syncControlResult{}, err
	}
	if !result.Success {
		if strings.TrimSpace(result.Error) == "" {
			return syncControlResult{}, fmt.Errorf("%s failed", request.Operation)
		}
		return syncControlResult{}, errors.New(result.Error)
	}
	return result, nil
}

// exchangeSyncControlRequest owns file publication, polling and decoding. Save
// has an absolute deadline and retracts its request on return. Older file
// operations retain their pending request when the caller stops waiting.
func exchangeSyncControlRequest(localRoot string, request syncControlRequest, timeout time.Duration) (syncControlResult, error) {
	requestID, err := randomSuffix()
	if err != nil {
		return syncControlResult{}, err
	}
	requestPath, resultPath := syncControlRequestPath(localRoot, requestID), syncControlResultPath(localRoot, requestID)
	isSave := request.Operation == syncControlOpSave || request.Operation == syncControlOpShutdown
	isLifecycle := isSave || request.Operation == syncControlOpStatus || request.Operation == syncControlOpDetach
	resultName := "file operation"
	timeoutErr := fmt.Errorf("timed out waiting for sync control result for %s", request.Path)
	if isSave {
		resultName = "save"
		timeoutErr = errors.New("timed out waiting for save; completion is unconfirmed and partial work may have occurred")
	}
	if isLifecycle {
		defer func() { _ = os.Remove(requestPath); _ = os.Remove(resultPath) }()
	}
	if err := writeSyncControlJSON(requestPath, request, 0o600); err != nil {
		return syncControlResult{}, err
	}

	deadline := time.Now().Add(timeout)
	if isSave {
		deadline = time.UnixMilli(request.DeadlineUnixMilli)
	}
	for {
		if isSave && !time.Now().Before(deadline) {
			return syncControlResult{}, timeoutErr
		}
		data, err := os.ReadFile(resultPath)
		if err == nil {
			_ = os.Remove(resultPath)
			var result syncControlResult
			if err := json.Unmarshal(data, &result); err != nil {
				return syncControlResult{}, fmt.Errorf("parse %s result: %w", resultName, err)
			}
			if request.Token != "" && (subtle.ConstantTimeCompare([]byte(result.Token), []byte(request.Token)) != 1 ||
				result.Version != request.Version || result.Workspace != request.Workspace || result.LocalRoot != request.LocalRoot || result.Operation != request.Operation) {
				return syncControlResult{}, errors.New("sync control result does not match the requested daemon identity")
			}
			return result, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return syncControlResult{}, err
		}
		if time.Now().After(deadline) {
			return syncControlResult{}, timeoutErr
		}
		delay := 25 * time.Millisecond
		if isSave && time.Until(deadline) < delay {
			delay = time.Until(deadline)
		}
		time.Sleep(delay)
	}
}

func isSyncControlPath(rel string) bool {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" {
		return false
	}
	return rel == syncControlDirName || strings.HasPrefix(rel, syncControlDirName+"/")
}

// Sync mode can observe path changes but not the original open flags, so
// exclusive-create requests travel through a daemon-owned request/result side
// channel under the local sync root.
func syncControlRequestID(rel string) (string, bool) {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	prefix := syncControlRequestsDirName + "/"
	if !strings.HasPrefix(rel, prefix) || !strings.HasSuffix(rel, ".json") {
		return "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(rel, prefix), ".json")
	if rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

func writeSyncControlJSON(path string, value any, mode uint32) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomicFile(path, data, mode)
}

func writeAtomicFile(absPath string, data []byte, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	tmpName := filepath.Join(dir, "."+base+".afssync.tmp."+suffix)
	f, err := os.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, os.FileMode(mode&0o7777))
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, absPath); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(absPath, os.FileMode(mode&0o7777)); err != nil && !errors.Is(err, os.ErrNotExist) {
	}
	return nil
}

func ensureSyncRemoteParentDirs(ctx context.Context, fsClient client.Client, normalizedPath string) error {
	trimmed := strings.Trim(normalizedPath, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) <= 1 {
		return nil
	}
	current := ""
	for _, part := range parts[:len(parts)-1] {
		current += "/" + part
		if stat, err := fsClient.Stat(ctx, current); err == nil && stat != nil {
			continue
		} else if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if err := fsClient.Mkdir(ctx, current); err != nil {
			return err
		}
	}
	return nil
}
