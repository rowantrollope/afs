package native

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func mountNFS(ctx context.Context, mountpoint, endpoint, export string, readonly bool) error {
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return err
	}
	options := "vers=3,tcp,port=" + port + ",mountport=" + port + ",sync,actimeo=1"
	command := "mount.nfs"
	if runtime.GOOS == "darwin" {
		command = "mount_nfs"
		options += ",nolocks,nonegnamecache"
	} else {
		options += ",nolock"
	}
	if readonly {
		options += ",ro"
	}
	binary, err := exec.LookPath(command)
	if err != nil {
		return fmt.Errorf("native NFS mount requires %s: %w", command, err)
	}
	out, err := exec.CommandContext(ctx, binary, "-o", options, "127.0.0.1:"+export, mountpoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount NFS: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unmountOS(ctx context.Context, mountpoint string, force bool) error {
	args := []string{mountpoint}
	if force {
		args = append([]string{"-f"}, args...)
	}
	out, err := exec.CommandContext(ctx, "umount", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unmount: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isMountpoint(path string) (bool, error) {
	_, _, mounted, err := mountedFilesystem(path)
	return mounted, err
}

// Detach recovers only an explicitly requested, abandoned native mount. It does
// not connect to Redis or remove the underlying directory.
func Detach(ctx context.Context, backend, mountpoint string) error {
	if !filepath.IsAbs(mountpoint) || filepath.Clean(mountpoint) != mountpoint || mountpoint == "/" {
		return errors.New("invalid native mountpoint")
	}
	if backend != "fuse" && backend != "nfs" {
		return errors.New("native backend must be fuse or nfs")
	}
	kind, source, mounted, err := mountedFilesystem(mountpoint)
	if err != nil {
		return err
	}
	if !mounted {
		return nil
	} // The helper may have exited immediately after detach.
	expected := backend == "nfs" && kind == "nfs" && strings.HasPrefix(source, "127.0.0.1:/")
	if backend == "fuse" {
		expected = (kind == "fuse.afs" || kind == "fuse" || kind == "macfuse" || kind == "osxfuse") && source == "afs"
	}
	if !expected {
		return fmt.Errorf("refusing to detach %s: existing mount is %s from %s", mountpoint, kind, source)
	}
	return unmountOS(ctx, mountpoint, true)
}
