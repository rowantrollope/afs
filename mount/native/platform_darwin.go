//go:build darwin

package native

import (
	"context"
	"golang.org/x/sys/unix"
	"io/fs"
	"os"
	"path/filepath"
)

func mountedFilesystem(path string) (string, string, bool, error) {
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return "", "", false, err
	}
	mounts := make([]unix.Statfs_t, count+16)
	count, err = unix.Getfsstat(mounts, unix.MNT_NOWAIT)
	if err != nil {
		return "", "", false, err
	}
	for _, mount := range mounts[:count] {
		if unix.ByteSliceToString(mount.Mntonname[:]) == path {
			return unix.ByteSliceToString(mount.Fstypename[:]), unix.ByteSliceToString(mount.Mntfromname[:]), true, nil
		}
	}
	return "", "", false, nil
}
func flushMount(ctx context.Context, path string) error {
	// Darwin has no syncfs. Flush each regular vnode without reading contents;
	// directory fsync also commits directory operations. Errors are reported.
	return filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
