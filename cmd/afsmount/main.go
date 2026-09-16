// afsmount is the optional native driver helper. Workspace/config UX lives in afs.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rowantrollope/afs/internal/mountcontrol"
	"github.com/rowantrollope/afs/mount/native"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "afsmount is started by afs mount --backend fuse|nfs")
		os.Exit(2)
	}
	if err := run(os.Getenv(mountcontrol.BootstrapEnv)); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func loadBootstrap(path string) (mountcontrol.Bootstrap, error) {
	var boot mountcontrol.Bootstrap
	if path == "" {
		return boot, errors.New("native helper requires a bootstrap file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return boot, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return boot, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ok || stat.Uid != uint32(os.Getuid()) {
		return boot, errors.New("bootstrap must be a regular mode-0600 file owned by this user")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&boot); err != nil {
		return boot, err
	}
	if !filepath.IsAbs(boot.RuntimeDir) || filepath.Clean(boot.RuntimeDir) != boot.RuntimeDir ||
		boot.Token == "" || boot.WorkspaceID == "" || !filepath.IsAbs(boot.Mountpoint) ||
		filepath.Dir(boot.ReadyPath) != boot.RuntimeDir {
		return boot, errors.New("invalid native bootstrap identity or paths")
	}
	if err = os.Remove(path); err != nil {
		return boot, err
	}
	return boot, nil
}

func writeReady(path string, ready mountcontrol.Ready) error {
	data, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ready-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func lockRuntime(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("native runtime directory must be owned by this user with mode 0700")
	}
	fd, err := syscall.Open(filepath.Join(dir, "owner.lock"), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "owner.lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("native runtime is already owned: %w", err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}

func run(path string) (err error) {
	boot, err := loadBootstrap(path)
	if err != nil {
		return err
	}
	ready := false
	defer func() {
		if err != nil && !ready {
			_ = writeReady(boot.ReadyPath, mountcontrol.Ready{Error: err.Error()})
		}
	}()
	unlock, err := lockRuntime(boot.RuntimeDir)
	if err != nil {
		return err
	}
	defer unlock()
	if boot.DetachOnly {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err = native.Detach(ctx, boot.Backend, boot.Mountpoint); err != nil {
			return err
		}
		return writeReady(boot.ReadyPath, mountcontrol.Ready{Ready: true})
	}
	if err = mountcontrol.PrepareSocketDir(); err != nil {
		return err
	}
	socket := mountcontrol.SocketPath(boot.RuntimeDir)
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("native control path is not a socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	session, err := native.Start(ctx, native.Config{Backend: boot.Backend, Mountpoint: boot.Mountpoint, RedisURL: boot.RedisURL, RedisKey: boot.RedisKey, Generation: boot.Generation, ReadOnly: boot.ReadOnly, UID: boot.UID, GID: boot.GID, AllowOther: boot.AllowOther})
	cancel()
	if err != nil && session == nil {
		return err
	}
	startupErr := err
	// Once a mount exists, ordinary errors must retain authenticated control.
	readiness := mountcontrol.Ready{Ready: startupErr == nil, Endpoint: session.Endpoint()}
	if startupErr != nil {
		readiness.Error = startupErr.Error()
	}
	if err = writeReady(boot.ReadyPath, readiness); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := session.Unmount(cleanup, false); cleanupErr == nil {
			return err
		}
		log.Printf("cannot report readiness; control remains available: %v", err)
	}
	ready = true
	stop := make(chan struct{}, 1)
	operations := make(chan struct{}, 1)
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go serveControl(conn, boot, session, operations, stop)
		}
	}()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	for {
		select {
		case <-stop:
			return nil
		case <-session.Done():
			// Let the control handler publish its final response before main exits.
			operations <- struct{}{}
			<-operations
			return nil
		case <-signals:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var err error
			select {
			case operations <- struct{}{}:
				err = session.Unmount(ctx, false)
				<-operations
			case <-ctx.Done():
				err = ctx.Err()
			}
			cancel()
			if err == nil {
				return nil
			}
			log.Printf("native unmount failed; still serving: %v", err)
		}
	}
}

type controlledSession interface {
	Status(context.Context) mountcontrol.Result
	Flush(context.Context) error
	Unmount(context.Context, bool) error
}

func serveControl(conn net.Conn, boot mountcontrol.Bootstrap, session controlledSession, operations chan struct{}, stop chan<- struct{}) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var req mountcontrol.Request
	if err := json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&req); err != nil {
		return
	}
	result := mountcontrol.Result{Version: mountcontrol.Version, Operation: req.Operation, Token: boot.Token, WorkspaceID: boot.WorkspaceID, Mountpoint: boot.Mountpoint, Backend: boot.Backend}
	valid := req.Version == mountcontrol.Version && req.WorkspaceID == boot.WorkspaceID && req.Mountpoint == boot.Mountpoint && subtle.ConstantTimeCompare([]byte(req.Token), []byte(boot.Token)) == 1
	if !valid {
		result.Token = ""
		result.Error = "native control identity does not match"
		_ = json.NewEncoder(conn).Encode(result)
		return
	}
	deadline := time.UnixMilli(req.DeadlineUnixMilli)
	if req.DeadlineUnixMilli <= 0 || !time.Now().Before(deadline) {
		result.Error = "native control deadline exceeded"
		_ = json.NewEncoder(conn).Encode(result)
		return
	}
	_ = conn.SetDeadline(deadline)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	shutdown := false
	if req.Operation == mountcontrol.Status {
		status := session.Status(ctx)
		result.Success = status.Success
		result.Connected = status.Connected
		result.Error = status.Error
		result.Endpoint = status.Endpoint
	} else {
		select {
		case operations <- struct{}{}:
			defer func() { <-operations }()
		case <-ctx.Done():
			result.Error = ctx.Err().Error()
			_ = json.NewEncoder(conn).Encode(result)
			return
		}
		var err error
		switch req.Operation {
		case mountcontrol.Flush:
			err = session.Flush(ctx)
			result.Flushed = err == nil
		case mountcontrol.Unmount:
			err = session.Unmount(ctx, false)
			result.Flushed = err == nil
			shutdown = err == nil
		case mountcontrol.Detach:
			err = session.Unmount(ctx, true)
			shutdown = err == nil
		default:
			err = errors.New("unsupported native control operation")
		}
		result.Success = err == nil
		if err != nil {
			result.Error = err.Error()
		}
	}
	_ = json.NewEncoder(conn).Encode(result)
	if shutdown {
		select {
		case stop <- struct{}{}:
		default:
		}
	}
}
