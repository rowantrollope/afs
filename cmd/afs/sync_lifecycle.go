package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/mount/client"
)

const syncDaemonBootstrapEnv = "AFS_LITE_SYNC_BOOTSTRAP"
const syncDaemonReadyTimeout = 2 * time.Minute

type syncDaemonBootstrap struct {
	Config     config      `json:"config"`
	Record     mountRecord `json:"record"`
	ReadyPath  string      `json:"ready_path"`
	Foreground bool        `json:"foreground"`
}
type syncDaemonReady struct {
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}
type successfulMountState struct {
	Generation   string `json:"generation"`
	RootIdentity string `json:"root_identity"`
}

func redisIdentity(cfg config) string {
	opts := buildRedisOptions(cfg, 1)
	addr := opts.Addr
	if opts.Network != "unix" {
		addr = strings.ToLower(addr)
	}
	// Credentials and transport options do not identify a separate database.
	return sha256Hex([]byte(fmt.Sprintf("%s\x00%s\x00%d", opts.Network, addr, opts.DB)))
}

func (a *app) mount(args []string) error {
	flags := flag.NewFlagSet("mount", flag.ContinueOnError)
	foreground := flags.Bool("foreground", false, "stay attached")
	backend := flags.String("backend", "sync", "sync, fuse, or nfs")
	pos, err := parseCommandFlags(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New(commandUsage["mount"])
	}
	if *backend != "sync" {
		if *backend != "fuse" && *backend != "nfs" {
			return fmt.Errorf("unknown mount backend %q; choose sync, fuse, or nfs", *backend)
		}
		return a.mountNative(pos[0], pos[1], *backend, *foreground)
	}
	localRoot, err := normalizeMountPath(pos[1])
	if err != nil {
		return err
	}
	if localRoot == string(filepath.Separator) {
		return errors.New("the filesystem root cannot be a sync directory")
	}
	ctx := context.Background()
	if err = a.connect(ctx); err != nil {
		return err
	}
	meta, err := a.service.GetWorkspace(ctx, pos[0])
	if err != nil {
		return err
	}
	generation, err := a.service.WorkspaceGeneration(ctx, meta.ID)
	if err != nil {
		return err
	}
	release, err := lockRegistry()
	if err != nil {
		return err
	}
	reg, err := loadMountRegistry()
	if err != nil {
		release()
		return err
	}
	// Never signal a PID from the registry. A held root lock proves ownership.
	kept := make([]mountRecord, 0, len(reg.Mounts))
	for _, rec := range reg.Mounts {
		conflict := rec.LocalPath == localRoot || pathContains(rec.LocalPath, localRoot) || pathContains(localRoot, rec.LocalPath)
		if conflict {
			if isNativeMount(rec) {
				release()
				return fmt.Errorf("directory overlaps a registered native mount: %s; unmount it first", rec.LocalPath)
			}
			owned, e := mountOwned(rec)
			if e != nil {
				release()
				return e
			}
			if owned {
				release()
				return fmt.Errorf("directory overlaps an active sync root: %s", rec.LocalPath)
			}
			continue
		}
		kept = append(kept, rec)
	}
	reg.Mounts = kept
	token, err := randomSuffix()
	if err != nil {
		release()
		return err
	}
	id := sha256Hex([]byte(redisIdentity(a.config) + "\x00" + meta.ID + "\x00" + localRoot))[:32]
	runtimeDir := filepath.Join(baseStateDir(), "clients", id)
	rec := mountRecord{ID: id, Workspace: meta.Name, WorkspaceID: meta.ID, LocalPath: localRoot,
		Redis: redisDisplay(a.config), RedisIdentity: redisIdentity(a.config), RedisKey: controlplane.WorkspaceFSKey(meta.ID), Generation: generation,
		Token: token, RuntimeDir: runtimeDir, SyncLog: filepath.Join(runtimeDir, "sync.log"), StartedAt: time.Now().UTC()}
	boot := syncDaemonBootstrap{Config: a.config, Record: rec, Foreground: *foreground}
	if *foreground {
		rec.PID = os.Getpid()
		boot.Record = rec
		upsertMount(&reg, rec)
		err = saveMountRegistry(reg)
		release()
		if err != nil {
			return err
		}
		defer removeFinishedMount(rec)
		return serveSyncDaemon(boot)
	}
	pid, err := startSyncDaemonProcess(boot)
	if err != nil {
		release()
		return err
	}
	rec.PID = pid
	upsertMount(&reg, rec)
	if err = saveMountRegistry(reg); err != nil {
		_, _ = controlMount(rec, syncControlOpDetach, 5*time.Second)
		release()
		return err
	}
	release()
	return a.output(map[string]any{"workspace": meta.Name, "directory": localRoot, "status": "syncing", "pid": pid}, fmt.Sprintf("Syncing workspace %q at %q (PID %d).\n", meta.Name, localRoot, pid))
}

func removeFinishedMount(rec mountRecord) {
	release, err := lockRegistry()
	if err != nil {
		return
	}
	defer release()
	reg, err := loadMountRegistry()
	if err != nil {
		return
	}
	if current, ok := mountByPath(reg, rec.LocalPath); ok && current.Token == rec.Token {
		removeMountByPath(&reg, rec.LocalPath)
		_ = saveMountRegistry(reg)
	}
}

// Retained re-exec/bootstrap/readiness flow. Reconciliation runs in the child
// once, so there is no parent-to-child subscription gap or duplicate scan.
func startSyncDaemonProcess(bootstrap syncDaemonBootstrap) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if err = os.MkdirAll(bootstrap.Record.RuntimeDir, 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(bootstrap.Record.SyncLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	readyPath, err := reserveSyncDaemonReadyPath()
	if err != nil {
		return 0, err
	}
	defer os.Remove(readyPath)
	bootstrap.ReadyPath = readyPath
	bootstrapPath, err := writeSyncDaemonBootstrap(bootstrap)
	if err != nil {
		return 0, err
	}
	defer os.Remove(bootstrapPath)
	cmd := exec.Command(exe, "_sync-daemon")
	cmd.Env = append(os.Environ(), syncDaemonBootstrapEnv+"="+bootstrapPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return 0, err
	}
	if err = waitForSyncDaemonReady(cmd.Process, readyPath, syncDaemonReadyTimeout); err != nil {
		// This is our newly started child handle, never a PID loaded from disk.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func runSyncDaemon() error {
	boot, ok, err := loadSyncDaemonBootstrap()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("missing private sync bootstrap")
	}
	if err = serveSyncDaemon(boot); err != nil {
		_ = writeSyncDaemonReady(boot.ReadyPath, err)
	}
	return err
}

func serveSyncDaemon(boot syncDaemonBootstrap) error {
	rec := boot.Record
	if rec.LocalPath == "" || rec.WorkspaceID == "" || rec.Token == "" || rec.Generation == "" {
		return errors.New("incomplete sync bootstrap")
	}
	oldStateDir := runtimeStateDir
	runtimeStateDir = rec.RuntimeDir
	defer func() { runtimeStateDir = oldStateDir }()
	if err := os.MkdirAll(rec.RuntimeDir, 0o700); err != nil {
		return err
	}
	localSnapshot, err := inspectMountLocalRoot(rec.LocalPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(rec.LocalPath, 0o755); err != nil {
		return err
	}
	releaseRoot, err := claimSyncRoot(rec.LocalPath)
	if err != nil {
		return err
	}
	defer releaseRoot()
	// A restore invalidates saved baselines even across crashes and restarts.
	genPath := filepath.Join(rec.RuntimeDir, "generation")
	prior, err := os.ReadFile(genPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(prior) > 0 && string(prior) != rec.Generation {
		return errors.New("workspace was restored since this local state was saved; preserve local edits and mount into a new directory")
	}
	mountedPath := filepath.Join(rec.RuntimeDir, "mounted")
	successfulBytes, readErr := os.ReadFile(mountedPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	var successful successfulMountState
	if readErr == nil {
		if err := json.Unmarshal(successfulBytes, &successful); err != nil {
			return fmt.Errorf("read established mount state: %w", err)
		}
	}
	rootIdentity := localFileIdentityFromPath(rec.LocalPath)
	if rootIdentity == "" {
		return errors.New("cannot identify local sync root")
	}
	if localSnapshot.Exists && successful.RootIdentity != "" && successful.RootIdentity != rootIdentity {
		return errors.New("local sync root was replaced; preserve its contents and mount into a new directory")
	}
	knownMount := localSnapshot.Exists && successful.Generation == rec.Generation && successful.RootIdentity == rootIdentity
	if err = os.WriteFile(genPath, []byte(rec.Generation), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = client.WithWorkspaceGeneration(ctx, rec.Generation)
	rdb := redis.NewClient(buildRedisOptions(boot.Config, 8))
	defer rdb.Close()
	ping, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	err = rdb.Ping(ping).Err()
	cancelPing()
	if err != nil {
		return redisConnectionError(boot.Config)
	}
	current, err := controlplane.NewStore(rdb).WorkspaceGeneration(ctx, rec.WorkspaceID)
	if err != nil {
		return err
	}
	if current != rec.Generation {
		return errors.New("workspace changed while mounting; retry")
	}
	d, err := newSyncDaemon(syncDaemonConfig{Workspace: rec.WorkspaceID, LocalRoot: rec.LocalPath, FS: client.New(rdb, rec.RedisKey), Store: newAFSStore(rdb), MaxFileBytes: syncSizeCapBytes(boot.Config), WatcherQueueCapacity: syncWatcherQueueCapacity(boot.Config), Interactive: boot.Foreground})
	if err != nil {
		return err
	}
	d.full.requireRemountOnRootReplace = true
	if !localSnapshot.Exists {
		resetMountSyncState(d)
	}
	plan, err := buildMountReconcilePlan(ctx, d)
	if err != nil {
		return err
	}
	if mountPlanShouldResetEmptyLocalState(plan, localSnapshot) {
		resetMountSyncState(d)
		plan, err = buildMountReconcilePlan(ctx, d)
		if err != nil {
			return err
		}
	}
	if mountPlanDeletesRemoteFromEmptyLocal(plan, localSnapshot) {
		return errors.New("empty local directory would delete remote files; preserve this directory and mount into a new directory")
	}
	// Existing exact-tree check safely permits a populated identical tree. All
	// other first-mount merges need explicit import through afs create --from.
	if plan.requiresConfirmation() && !knownMount {
		return errors.New("mount would merge or upload an existing populated directory; use an empty directory or afs create <new-workspace> --from <directory>")
	}
	if plan.ConflictCount > 0 && !knownMount {
		return fmt.Errorf("mount has %d conflict(s); preserve local edits before mounting", plan.ConflictCount)
	}
	approveMountReconcilePlan(d, plan)
	if err = d.Start(ctx); err != nil {
		return err
	}
	// This marker distinguishes recovery of an established root from a
	// first mount into unrelated populated content, even after a hard crash.
	if err = writeSyncControlJSON(mountedPath, successfulMountState{Generation: rec.Generation, RootIdentity: rootIdentity}, 0o600); err != nil {
		d.Stop()
		return err
	}
	service := startSyncSaveServiceWithToken(ctx, d, rec.Token)
	defer service.Stop()
	if err = writeSyncDaemonReady(boot.ReadyPath, nil); err != nil {
		return err
	}
	if boot.Foreground {
		fmt.Fprintf(os.Stderr, "Syncing %s at %s; Ctrl-C flushes and stops.\n", rec.Workspace, rec.LocalPath)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-service.Done():
		return nil
	case <-signals:
		_, err = controlMount(rec, syncControlOpShutdown, defaultSyncSaveTimeout)
		if err != nil {
			return fmt.Errorf("shutdown flush failed; local changes are preserved: %w", err)
		}
		<-service.Done()
		return nil
	}
}

func prepareSyncControlDirs(root string) error {
	for _, rel := range []string{syncControlDirName, syncControlRequestsDirName, syncControlResultsDirName} {
		p := filepath.Join(root, rel)
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("sync control path %s must be a directory, not a symlink", rel)
		}
	}
	return nil
}
func claimSyncRoot(root string) (func(), error) {
	if err := prepareSyncControlDirs(root); err != nil {
		return nil, err
	}
	p := filepath.Join(root, syncControlDirName, "owner.lock")
	if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("sync owner lock must not be a symlink")
	}
	file, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("local directory already has a sync owner")
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func syncRootOwned(root string) (bool, error) {
	p := filepath.Join(root, syncControlDirName, "owner.lock")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("invalid sync owner lock")
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

func controlMount(rec mountRecord, op string, timeout time.Duration) (syncControlResult, error) {
	if err := prepareSyncControlDirs(rec.LocalPath); err != nil {
		return syncControlResult{}, err
	}
	req := syncControlRequest{Version: syncControlVersion, Operation: op, Workspace: rec.WorkspaceID, LocalRoot: rec.LocalPath, Token: rec.Token, DeadlineUnixMilli: time.Now().Add(timeout).UnixMilli()}
	result, err := exchangeSyncControlRequest(rec.LocalPath, req, timeout)
	if err != nil {
		return result, err
	}
	if result.Version != syncControlVersion || result.Operation != op || result.Workspace != req.Workspace || result.LocalRoot != req.LocalRoot || result.Token != req.Token {
		return result, errors.New("daemon identity did not match local mount record")
	}
	if !result.Success {
		return result, fmt.Errorf("%s failed: %s", op, result.Error)
	}
	if (op == syncControlOpSave || op == syncControlOpShutdown) && result.Save == nil {
		return result, errors.New("daemon did not return a verified flush receipt")
	}
	return result, nil
}

func (a *app) unmount(args []string) error {
	f := flag.NewFlagSet("unmount", flag.ContinueOnError)
	force := f.Bool("force", false, "detach without synchronization")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(commandUsage["unmount"])
	}
	root, err := expandPath(pos[0])
	if err != nil {
		return err
	}
	release, err := lockRegistry()
	if err != nil {
		return err
	}
	defer release()
	reg, err := loadMountRegistry()
	if err != nil {
		return err
	}
	if _, ok := mountByPath(reg, root); !ok {
		root, err = normalizeMountPath(pos[0])
		if err != nil {
			return err
		}
	}
	rec, ok := mountByPath(reg, root)
	if !ok {
		return errors.New("directory is not registered as mounted")
	}
	if err := a.redisHeader("REDIS", rec.Redis); err != nil {
		return err
	}
	if isNativeMount(rec) {
		return a.unmountNative(rec, &reg, *force)
	}
	owned, err := syncRootOwned(root)
	if err != nil {
		return err
	}
	if !owned {
		if !*force {
			return errors.New("sync daemon is not running; pending local changes cannot be flushed. Remount to recover, or use --force to remove stale registration")
		}
		removeMountByPath(&reg, root)
		if err = saveMountRegistry(reg); err != nil {
			return err
		}
		return a.output(map[string]any{"directory": root, "detached": true, "synchronized": false}, formatUnmount(root, true))
	}
	op := syncControlOpShutdown
	if *force {
		op = syncControlOpDetach
	}
	result, err := controlMount(rec, op, defaultSyncSaveTimeout)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		owned, e := syncRootOwned(root)
		if e != nil {
			return e
		}
		if !owned {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("daemon acknowledged shutdown but still owns the directory")
		}
		time.Sleep(25 * time.Millisecond)
	}
	removeMountByPath(&reg, root)
	if err = saveMountRegistry(reg); err != nil {
		return err
	}
	return a.output(map[string]any{"directory": root, "unmounted": true, "synchronized": !*force, "save": result.Save}, formatUnmount(root, *force))
}
func (a *app) status(args []string) error {
	if len(args) > 1 {
		return errors.New(commandUsage["status"])
	}
	reg, err := loadMountRegistry()
	if err != nil {
		return err
	}
	if len(args) == 1 {
		root, e := expandPath(args[0])
		if e != nil {
			return e
		}
		if _, ok := mountByPath(reg, root); !ok {
			root, e = normalizeMountPath(args[0])
			if e != nil {
				return e
			}
		}
		rec, ok := mountByPath(reg, root)
		if !ok {
			return errors.New("directory is not registered as mounted")
		}
		reg.Mounts = []mountRecord{rec}
	}
	out := make([]map[string]any, 0, len(reg.Mounts))
	for _, rec := range reg.Mounts {
		row := map[string]any{"workspace": rec.Workspace, "directory": rec.LocalPath, "redis": rec.Redis, "pid": rec.PID}
		if isNativeMount(rec) {
			nativeMountStatus(rec, row)
			out = append(out, row)
			continue
		}
		owned, e := syncRootOwned(rec.LocalPath)
		if e != nil {
			row["error"] = e.Error()
			row["state"] = "unavailable"
		} else if !owned {
			row["state"] = "stopped"
			row["error"] = "daemon stopped; local changes may be pending"
		} else {
			result, e := controlMount(rec, syncControlOpStatus, 4*time.Second)
			if e != nil {
				row["state"] = "unresponsive"
				row["error"] = e.Error()
			} else {
				row["state"] = "running"
				row["sync"] = result.Status
			}
		}
		out = append(out, row)
	}
	label, endpoint := "Configured REDIS", redisDisplay(a.config)
	if len(args) == 1 {
		label, endpoint = "REDIS", reg.Mounts[0].Redis
	}
	if err := a.redisHeader(label, endpoint); err != nil {
		return err
	}
	return a.output(out, formatMountStatus(out, len(args) == 1))
}
func (a *app) localWorkspaceMounts(ctx context.Context, workspace string) ([]mountRecord, error) {
	meta, err := a.service.GetWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	reg, err := loadMountRegistry()
	if err != nil {
		return nil, err
	}
	var records []mountRecord
	for _, rec := range reg.Mounts {
		if rec.WorkspaceID == meta.ID && rec.RedisIdentity == redisIdentity(a.config) {
			records = append(records, rec)
		}
	}
	return records, nil
}
func (a *app) flushLocalMounts(ctx context.Context, workspace string) error {
	records, err := a.localWorkspaceMounts(ctx, workspace)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if isNativeMount(rec) {
			if _, err := callNativeMount(rec, "flush", defaultSyncSaveTimeout); err != nil {
				return fmt.Errorf("flush %s: %w", rec.LocalPath, err)
			}
			continue
		}
		owned, e := syncRootOwned(rec.LocalPath)
		if e != nil {
			return e
		}
		if !owned {
			return fmt.Errorf("local mount %s is stopped; recover it before checkpointing", rec.LocalPath)
		}
		if _, err = controlMount(rec, syncControlOpSave, defaultSyncSaveTimeout); err != nil {
			return fmt.Errorf("flush %s: %w", rec.LocalPath, err)
		}
	}
	return nil
}
func (a *app) requireUnmounted(ctx context.Context, workspace string) error {
	records, err := a.localWorkspaceMounts(ctx, workspace)
	if err != nil {
		return err
	}
	if len(records) > 0 {
		return fmt.Errorf("unmount local workspace directory %s first", records[0].LocalPath)
	}
	return nil
}

func reserveSyncDaemonReadyPath() (string, error) {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(stateDir(), ".sync-ready-*.json")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	_ = os.Remove(name)
	return name, nil
}

func writeSyncDaemonReady(path string, daemonErr error) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	payload := syncDaemonReady{Ready: daemonErr == nil}
	if daemonErr != nil {
		payload.Error = daemonErr.Error()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func failSyncDaemonReady(path string, err error) error {
	_ = writeSyncDaemonReady(path, err)
	return err
}

func waitForSyncDaemonReady(process *os.Process, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			_ = os.Remove(path)
			var ready syncDaemonReady
			if err := json.Unmarshal(raw, &ready); err != nil {
				return fmt.Errorf("parse sync daemon ready marker: %w", err)
			}
			if ready.Ready {
				return nil
			}
			if strings.TrimSpace(ready.Error) != "" {
				return fmt.Errorf("sync daemon failed before ready: %s", ready.Error)
			}
			return errors.New("sync daemon failed before ready")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read sync daemon ready marker: %w", err)
		}
		if time.Now().After(deadline) {
			_ = os.Remove(path)
			return fmt.Errorf("sync daemon did not become ready within %s", timeout)
		}
		if process != nil && !processAlive(process.Pid) {
			_ = os.Remove(path)
			return errors.New("sync daemon exited before it became ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func writeSyncDaemonBootstrap(bootstrap syncDaemonBootstrap) (string, error) {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return "", err
	}
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(stateDir(), ".sync-bootstrap-*.json")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func loadSyncDaemonBootstrap() (syncDaemonBootstrap, bool, error) {
	path := strings.TrimSpace(os.Getenv(syncDaemonBootstrapEnv))
	if path == "" {
		return syncDaemonBootstrap{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return syncDaemonBootstrap{}, false, fmt.Errorf("read sync bootstrap: %w", err)
	}
	_ = os.Remove(path)
	var bootstrap syncDaemonBootstrap
	if err := json.Unmarshal(raw, &bootstrap); err != nil {
		return syncDaemonBootstrap{}, false, fmt.Errorf("parse sync bootstrap: %w", err)
	}
	return bootstrap, true, nil
}
