package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/mountcontrol"
)

func isNativeMount(rec mountRecord) bool { return rec.Backend == "fuse" || rec.Backend == "nfs" }

// Native mounts hide their mountpoints. Ownership and control must never depend
// on files inside the mounted tree, which may be blocked on a failed gateway.
func mountOwned(rec mountRecord) (bool, error) {
	if !isNativeMount(rec) {
		return syncRootOwned(rec.LocalPath)
	}
	p := filepath.Join(rec.RuntimeDir, "owner.lock")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("invalid native mount owner lock")
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

func findNativeHelper() (string, error) {
	if explicit := os.Getenv("AFS_NATIVE_HELPER"); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", errors.New("AFS_NATIVE_HELPER must be an absolute executable path")
		}
		return explicit, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "afsmount")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	if helper, err := exec.LookPath("afsmount"); err == nil {
		return helper, nil
	}
	return "", errors.New("native mounting requires afsmount; build it with 'make native' and install it alongside afs")
}

func validateNativeMountpoint(root string) error {
	if root == string(filepath.Separator) {
		return errors.New("the filesystem root cannot be a native mountpoint")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("native mountpoint must be a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("native mountpoint must be empty; import local files with afs create --from and mount into an empty directory")
	}
	return nil
}

func (a *app) mountNative(workspace, directory, backend string, foreground bool, choices ...mountOptions) error {
	var opts mountOptions
	if len(choices) > 0 {
		opts = choices[0]
	}
	helper, err := findNativeHelper()
	if err != nil {
		return err
	}
	root, err := normalizeMountPath(directory)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err = a.connect(ctx); err != nil {
		return err
	}
	meta, err := a.service.GetWorkspace(ctx, workspace)
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
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	reg, err := loadMountRegistry()
	if err != nil {
		return err
	}
	if conflict, ok := mountPathConflict(reg, root); ok {
		return fmt.Errorf("directory overlaps registered mount %s; unmount it first", conflict.LocalPath)
	}
	if err = validateNativeMountpoint(root); err != nil {
		return err
	}
	token, err := randomSuffix()
	if err != nil {
		return err
	}
	id := sha256Hex([]byte(redisIdentity(a.config) + "\x00" + meta.ID + "\x00" + root + "\x00" + backend))[:32]
	runtimeDir := filepath.Join(baseStateDir(), "native", id)
	rec := mountRecord{Backend: backend, ID: id, Workspace: meta.Name, WorkspaceID: meta.ID,
		ReadOnly: opts.ReadOnly, UID: opts.UID, GID: opts.GID, AllowOther: opts.AllowOther,
		LocalPath: root, Redis: redisDisplay(a.config), RedisIdentity: redisIdentity(a.config),
		RedisKey: controlplane.WorkspaceFSKey(meta.ID), Generation: generation, Token: token,
		RuntimeDir: runtimeDir, SyncLog: filepath.Join(runtimeDir, "native.log"), StartedAt: time.Now().UTC()}
	// The helper is detached and may outlive this command at any point during
	// startup. Persist its control identity before allowing it to start a mount;
	// PID zero is a recoverable pending intent, never authority to signal a PID.
	upsertMount(&reg, rec)
	if err = saveMountRegistry(reg); err != nil {
		return err
	}
	cmd, err := startNativeHelper(helper, a.config, rec, false, func(pid int) error {
		rec.PID = pid
		upsertMount(&reg, rec)
		return saveMountRegistry(reg)
	})
	if err != nil {
		if cmd == nil {
			// Only a helper that never started proves this attempt could not
			// have mounted anything. A missing owner lock after a process exits
			// does not prove that its kernel mount was detached.
			removeMountByPath(&reg, root)
			if saveErr := saveMountRegistry(reg); saveErr != nil {
				return fmt.Errorf("native startup: %v; clear unstarted registration: %w", err, saveErr)
			}
			return err
		}
		return fmt.Errorf("%w; mount registration retained for recovery", err)
	}
	release()
	locked = false
	if !foreground {
		_ = cmd.Process.Release()
		return a.output(map[string]any{"workspace": meta.Name, "directory": root, "backend": backend,
			"status": "mounted", "pid": rec.PID, "read_only": rec.ReadOnly}, fmt.Sprintf("Mounted workspace %q at %q using %s (PID %d).\n", meta.Name, root, backend, rec.PID))
	}
	fmt.Fprintf(os.Stderr, "Mounted workspace %q at %q using %s; Ctrl-C flushes and stops.\n", meta.Name, root, backend)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			if owned, _ := mountOwned(rec); err == nil && !owned {
				removeFinishedMount(rec)
			}
			return err
		case <-signals:
			if _, err := callNativeMount(rec, "unmount", defaultSyncSaveTimeout); err != nil {
				fmt.Fprintf(os.Stderr, "afs: unmount failed; native mount remains active: %v\n", err)
			}
		}
	}
}

func startNativeHelper(helper string, cfg config, rec mountRecord, detachOnly bool, started func(int) error) (*exec.Cmd, error) {
	if err := os.MkdirAll(rec.RuntimeDir, 0o700); err != nil {
		return nil, err
	}
	readyFile, err := os.CreateTemp(rec.RuntimeDir, ".ready-*.json")
	if err != nil {
		return nil, err
	}
	ready := readyFile.Name()
	if err := readyFile.Close(); err != nil {
		_ = os.Remove(ready)
		return nil, err
	}
	if err := os.Remove(ready); err != nil {
		return nil, err
	}
	defer os.Remove(ready)
	boot := mountcontrol.Bootstrap{RedisURL: cfg.Redis, Backend: rec.Backend, WorkspaceID: rec.WorkspaceID,
		RedisKey: rec.RedisKey, Generation: rec.Generation, Mountpoint: rec.LocalPath,
		RuntimeDir: rec.RuntimeDir, Token: rec.Token, ReadyPath: ready, DetachOnly: detachOnly,
		ReadOnly: rec.ReadOnly, UID: rec.UID, GID: rec.GID, AllowOther: rec.AllowOther}
	raw, err := json.Marshal(boot)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(rec.RuntimeDir, ".bootstrap-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(rec.SyncLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(helper)
	cmd.Env = append(os.Environ(), "AFS_NATIVE_BOOTSTRAP="+f.Name())
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	if started != nil {
		if err := started(cmd.Process.Pid); err != nil {
			// The pre-launch intent remains valid even if recording this PID
			// fails. Keep the helper's runtime/token available for recovery.
			return cmd, fmt.Errorf("record native helper PID: %w", err)
		}
	}
	if err = waitForSyncDaemonReady(cmd.Process, ready, syncDaemonReadyTimeout); err != nil {
		if owned, _ := mountOwned(rec); !owned {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return cmd, fmt.Errorf("native helper startup: %w (log: %s)", err, rec.SyncLog)
	}
	return cmd, nil
}

func callNativeMount(rec mountRecord, op string, timeout time.Duration) (mountcontrol.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := mountcontrol.Call(ctx, rec.RuntimeDir, mountcontrol.Request{Operation: op,
		Token: rec.Token, WorkspaceID: rec.WorkspaceID, Mountpoint: rec.LocalPath})
	if err != nil {
		return result, err
	}
	if !result.Success {
		return result, fmt.Errorf("native %s failed: %s", op, result.Error)
	}
	if (op == "flush" || op == "unmount") && !result.Flushed {
		return result, errors.New("native helper did not confirm completed kernel and Redis flush")
	}
	return result, nil
}

func (a *app) unmountNative(rec mountRecord, reg *mountRegistry, force bool) error {
	owned, err := mountOwned(rec)
	if err != nil {
		return err
	}
	if !owned && force {
		helper, err := findNativeHelper()
		if err != nil {
			return err
		}
		// Start a fresh owned process that only detaches the stale OS mount.
		// Do not signal the old PID: it may now belong to another application.
		cmd, err := startNativeHelper(helper, a.config, rec, true, nil)
		if err != nil {
			return err
		}
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("native mount recovery: %w", err)
		}
		removeMountByPath(reg, rec.LocalPath)
		if err := saveMountRegistry(*reg); err != nil {
			return err
		}
		return a.output(map[string]any{"directory": rec.LocalPath, "backend": rec.Backend,
			"unmounted": true, "synchronized": false}, fmt.Sprintf("Detached stale %s mount at %q.\n", rec.Backend, rec.LocalPath))
	}
	op := "unmount"
	if force {
		op = "detach"
	}
	if _, err := callNativeMount(rec, op, defaultSyncSaveTimeout); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		owned, err := mountOwned(rec)
		if err != nil {
			return err
		}
		if !owned {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("native helper acknowledged unmount but still owns the mount")
		}
		time.Sleep(25 * time.Millisecond)
	}
	removeMountByPath(reg, rec.LocalPath)
	if err := saveMountRegistry(*reg); err != nil {
		return err
	}
	text := fmt.Sprintf("Unmounted %q (%s); pending writes flushed.\n", rec.LocalPath, rec.Backend)
	if force {
		text = fmt.Sprintf("Detached %q (%s) without a flush guarantee.\n", rec.LocalPath, rec.Backend)
	} else if rec.ReadOnly {
		text = fmt.Sprintf("Unmounted read-only %s mount at %q.\n", rec.Backend, rec.LocalPath)
	}
	return a.output(map[string]any{"directory": rec.LocalPath, "backend": rec.Backend,
		"unmounted": true, "synchronized": !force && !rec.ReadOnly, "read_only": rec.ReadOnly}, text)
}

func nativeMountStatus(rec mountRecord, row map[string]any) {
	row["backend"] = rec.Backend
	row["read_only"] = rec.ReadOnly
	row["uid"], row["gid"], row["allow_other"] = rec.UID, rec.GID, rec.AllowOther
	owned, err := mountOwned(rec)
	if err != nil {
		row["state"], row["error"] = "unavailable", err.Error()
		return
	}
	if !owned {
		row["state"], row["error"] = "stopped", "native helper stopped; kernel mount may need detaching"
		return
	}
	result, err := callNativeMount(rec, "status", 4*time.Second)
	if err != nil {
		row["state"], row["error"] = "unresponsive", err.Error()
		return
	}
	row["state"] = "running"
	row["native"] = map[string]any{"connected": result.Connected, "backend": result.Backend, "endpoint": result.Endpoint}
	if result.Error != "" {
		row["error"] = result.Error
	}
}
