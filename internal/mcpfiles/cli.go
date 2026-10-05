// Package mcpfiles adapts one server-managed folder-sync mount. It has no Redis
// storage implementation: publication and recovery belong to the AFS CLI.
package mcpfiles

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"time"
)

type CLI struct {
	Binary, Config, StateDir, Workspace, Directory string
	Timeout                                        time.Duration
}

func (c CLI) run(ctx context.Context, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Binary, append([]string{"--config", c.Config, "--json"}, args...)...)
	cmd.Env = append(os.Environ(), "AFS_STATE_DIR="+c.StateDir)
	// Do not forward diagnostics, connection settings or local paths to agents.
	data, err := cmd.Output()
	if err != nil {
		return errors.New("AFS mount unavailable; operator must check the daemon")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid AFS response")
	}
	return nil
}

func (c CLI) Check(ctx context.Context) error {
	var rows []struct {
		Workspace string `json:"workspace"`
		Directory string `json:"directory"`
		Backend   string `json:"backend"`
		State     string `json:"state"`
		ReadOnly  bool   `json:"read_only"`
		Sync      *struct {
			Connected bool   `json:"connected"`
			LastError string `json:"last_error"`
		} `json:"sync"`
	}
	if err := c.run(ctx, &rows, "sync", "status", c.Directory); err != nil {
		return err
	}
	if len(rows) != 1 {
		return errors.New("configured mount not found")
	}
	r := rows[0]
	if r.Workspace != c.Workspace || r.Directory != c.Directory || r.Backend != "sync" || r.ReadOnly || r.State != "running" || r.Sync == nil || !r.Sync.Connected || r.Sync.LastError != "" {
		return errors.New("configured workspace is not a healthy writable folder-sync mount")
	}
	return nil
}

func (c CLI) Verify(ctx context.Context) error {
	var result struct {
		Success   bool   `json:"success"`
		Verified  bool   `json:"verified"`
		Workspace string `json:"workspace"`
		Directory string `json:"directory"`
	}
	if err := c.run(ctx, &result, "sync", "--wait", c.Directory, "--timeout", c.Timeout.String()); err != nil {
		return err
	}
	if !result.Success || !result.Verified || result.Workspace != c.Workspace || result.Directory != c.Directory {
		return errors.New("AFS did not verify the configured workspace")
	}
	return nil
}
