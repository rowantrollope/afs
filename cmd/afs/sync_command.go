package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"time"
)

const syncCommandUsage = `Usage: afs sync --wait <workspace|directory> [--timeout 2m] [--json]
       afs sync status [workspace|directory] [--json]

Synchronization runs automatically. --wait publishes and verifies the current
included local tree in Redis, then resumes synchronization without a checkpoint.
Pause application writers for a reliable completion boundary. This does not
confirm Redis disk persistence or delivery to other mounts.
status observes connection, queued work, conflicts and errors; it does not verify
publication. Workspace names must identify exactly one locally registered mount.
`
const syncStatusCommandUsage = "Usage: afs sync status [workspace|directory] [--json]"

type syncWaitOutput struct {
	Success   bool             `json:"success"`
	Operation string           `json:"operation"`
	Workspace string           `json:"workspace,omitempty"`
	Directory string           `json:"directory,omitempty"`
	Verified  bool             `json:"verified"`
	Receipt   *syncSaveReceipt `json:"receipt,omitempty"`
	Error     string           `json:"error,omitempty"`
}

func (a *app) syncCommand(args []string) (err error) {
	if len(args) == 0 {
		fmt.Print(syncCommandUsage)
		return nil
	}
	if args[0] == "status" {
		return a.syncCommandStatus(args[1:])
	}
	out := syncWaitOutput{Operation: "sync"}
	defer func() {
		if err != nil && a.options.json {
			out.Error = err.Error()
			_ = a.output(out, "")
		}
	}()
	f := flag.NewFlagSet("sync", flag.ContinueOnError)
	wait := f.Bool("wait", false, "wait for verified publication")
	timeout := f.Duration("timeout", defaultSyncSaveTimeout, "verification timeout")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if !*wait || len(pos) != 1 {
		return errors.New(syncCommandUsage)
	}
	if *timeout < time.Millisecond || *timeout > 24*time.Hour {
		return errors.New("--timeout must be between 1ms and 24h")
	}
	reg, err := loadMountRegistry()
	if err != nil {
		return err
	}
	rec, err := resolveSyncCommandMount(reg, pos[0])
	if err != nil {
		return err
	}
	out.Workspace, out.Directory = rec.Workspace, rec.LocalPath
	if isNativeMount(rec) {
		return errors.New("sync --wait requires a folder-sync mount; native mounts publish through filesystem operations")
	}
	if rec.ReadOnly {
		return errors.New("sync --wait cannot publish a read-only mount")
	}
	owned, err := syncRootOwned(rec.LocalPath)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("sync daemon is not running; remount to recover pending changes")
	}
	if err = a.redisHeader("REDIS", rec.Redis); err != nil {
		return err
	}
	result, err := controlMount(rec, syncControlOpSave, *timeout)
	if err != nil {
		return err
	}
	out.Success, out.Verified, out.Receipt = true, true, result.Save
	return a.output(out, fmt.Sprintf("Verified publication of %q in Redis; synchronization continues.\n", rec.LocalPath))
}

// Resolve all identities together: a name colliding with another mount's path
// must not silently publish the wrong tree. No Redis connection is required.
func resolveSyncCommandMount(reg mountRegistry, target string) (mountRecord, error) {
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

func (a *app) syncCommandStatus(args []string) error {
	f := flag.NewFlagSet("sync status", flag.ContinueOnError)
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New(syncStatusCommandUsage)
	}
	reg, err := loadMountRegistry()
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		rec, e := resolveSyncCommandMount(reg, pos[0])
		if e != nil {
			return e
		}
		if isNativeMount(rec) {
			return errors.New("sync status requires a folder-sync mount; use afs status for native mounts")
		}
		reg.Mounts = []mountRecord{rec}
	}
	rows := make([]map[string]any, 0, len(reg.Mounts))
	for _, rec := range reg.Mounts {
		if isNativeMount(rec) {
			continue
		}
		row := map[string]any{"workspace": rec.Workspace, "directory": rec.LocalPath, "redis": rec.Redis, "pid": rec.PID, "read_only": rec.ReadOnly, "backend": "sync"}
		owned, e := syncRootOwned(rec.LocalPath)
		if e != nil {
			row["state"], row["error"] = "unavailable", e.Error()
		} else if !owned {
			row["state"], row["error"] = "stopped", "daemon stopped; local changes may be pending"
		} else {
			result, e := controlMount(rec, syncControlOpStatus, 4*time.Second)
			if e != nil {
				row["state"], row["error"] = "unresponsive", e.Error()
			} else if result.Status == nil {
				row["state"], row["error"] = "unavailable", "daemon returned no sync status"
			} else {
				row["state"], row["sync"] = "running", result.Status
			}
		}
		rows = append(rows, row)
	}
	label, endpoint := "Configured REDIS", redisDisplay(a.config)
	if len(pos) == 1 {
		label, endpoint = "REDIS", reg.Mounts[0].Redis
	}
	if err = a.redisHeader(label, endpoint); err != nil {
		return err
	}
	text := formatMountStatus(rows, len(pos) == 1)
	return a.output(rows, text)
}
