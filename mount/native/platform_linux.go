//go:build linux

package native

import (
	"bufio"
	"context"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// Read the kernel table, not the mounted directory: a crashed FUSE server makes
// stat on its mountpoint fail with ENOTCONN, but the mount still needs detaching.
func mountedFilesystem(path string) (string, string, bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", "", false, err
	}
	defer f.Close()
	return parseMountInfo(f, path)
}
func parseMountInfo(reader interface{ Read([]byte) (int, error) }, path string) (string, string, bool, error) {
	scan := bufio.NewScanner(reader)
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		tail := strings.Fields(parts[1])
		if len(fields) < 5 || len(tail) < 2 {
			continue
		}
		if unescapeMountPath(fields[4]) == path {
			return tail[0], unescapeMountPath(tail[1]), true, nil
		}
	}
	return "", "", false, scan.Err()
}
func unescapeMountPath(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
func flushMount(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}
