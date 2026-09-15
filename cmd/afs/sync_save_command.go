package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const defaultSyncSaveTimeout = 2 * time.Minute

func runSyncSaveControlRequest(localRoot string, request syncControlRequest) (syncControlResult, error) {
	result := syncControlResult{Version: syncControlVersion, Operation: syncControlOpSave,
		Workspace: request.Workspace, LocalRoot: request.LocalRoot}
	deadline := time.UnixMilli(request.DeadlineUnixMilli)
	if request.DeadlineUnixMilli <= 0 || !time.Now().Before(deadline) {
		return result, errors.New("save deadline has expired")
	}
	// Do not follow a user-created control directory symlink outside the mount.
	for _, rel := range []string{syncControlDirName, syncControlRequestsDirName, syncControlResultsDirName} {
		path := filepath.Join(localRoot, rel)
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return result, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return result, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("save control path %s must be a directory, not a symlink", rel)
		}
	}
	reply, err := exchangeSyncControlRequest(localRoot, request, 0)
	if err != nil {
		return result, err
	}
	if reply.Version != syncControlVersion || reply.Operation != syncControlOpSave ||
		reply.Workspace != request.Workspace || reply.LocalRoot != request.LocalRoot {
		return result, errors.New("save result does not match the requested workspace and local root")
	}
	if !reply.Success {
		if reply.Error == "" {
			reply.Error = "save failed; completion was not confirmed"
		}
		return reply, errors.New(reply.Error)
	}
	if reply.Save == nil {
		return result, errors.New("save result is missing its verification receipt")
	}
	return reply, nil
}
