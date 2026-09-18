// Package mountcontrol is the small, dependency-free protocol between afs and
// its optional native mount helper. It does not import filesystem drivers.
package mountcontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rowantrollope/afs/internal/managedclient"
)

const (
	BootstrapEnv = "AFS_NATIVE_BOOTSTRAP"
	Version      = 1
	Status       = "status"
	Flush        = "flush"
	Unmount      = "unmount"
	Detach       = "detach"
)

type Bootstrap struct {
	Management   managedclient.Settings     `json:"management,omitempty"`
	Registration managedclient.Registration `json:"registration,omitempty"`
	RedisURL     string                     `json:"redis_url"`
	Backend      string                     `json:"backend"`
	WorkspaceID  string                     `json:"workspace_id"`
	RedisKey     string                     `json:"redis_key"`
	Generation   string                     `json:"generation"`
	Mountpoint   string                     `json:"mountpoint"`
	RuntimeDir   string                     `json:"runtime_dir"`
	Token        string                     `json:"token"`
	ReadyPath    string                     `json:"ready_path"`
	ReadOnly     bool                       `json:"read_only,omitempty"`
	UID          *uint32                    `json:"uid,omitempty"`
	GID          *uint32                    `json:"gid,omitempty"`
	AllowOther   bool                       `json:"allow_other,omitempty"`
	DetachOnly   bool                       `json:"detach_only,omitempty"`
}

type Ready struct {
	Ready    bool   `json:"ready"`
	Error    string `json:"error,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

type Request struct {
	Version           int    `json:"version"`
	Operation         string `json:"operation"`
	Token             string `json:"token"`
	WorkspaceID       string `json:"workspace_id"`
	Mountpoint        string `json:"mountpoint"`
	DeadlineUnixMilli int64  `json:"deadline_unix_milli"`
}

type Result struct {
	Management  *managedclient.Status `json:"management,omitempty"`
	Version     int                   `json:"version"`
	Operation   string                `json:"operation"`
	Token       string                `json:"token"`
	WorkspaceID string                `json:"workspace_id"`
	Mountpoint  string                `json:"mountpoint"`
	Success     bool                  `json:"success"`
	Error       string                `json:"error,omitempty"`
	Connected   bool                  `json:"connected"`
	Flushed     bool                  `json:"flushed"`
	Backend     string                `json:"backend,omitempty"`
	Endpoint    string                `json:"endpoint,omitempty"`
}

func SocketPath(runtimeDir string) string {
	hash := sha256.Sum256([]byte(filepath.Clean(runtimeDir)))
	return filepath.Join(socketDir(), fmt.Sprintf("%x.sock", hash[:16]))
}

func socketDir() string { return filepath.Join("/tmp", fmt.Sprintf("afs-native-%d", os.Getuid())) }

// PrepareSocketDir keeps UNIX paths below the macOS limit even with a long
// home/runtime directory. The fixed temporary parent is private to this UID.
func PrepareSocketDir() error {
	dir := socketDir()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("native control directory must be owned by this user with mode 0700")
	}
	return nil
}

func Call(ctx context.Context, runtimeDir string, request Request) (Result, error) {
	if err := PrepareSocketDir(); err != nil {
		return Result{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	request.Version = Version
	request.DeadlineUnixMilli = deadline.UnixMilli()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(runtimeDir))
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Result{}, err
	}
	var result Result
	if err := json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&result); err != nil {
		return Result{}, err
	}
	if result.Version != Version || result.Operation != request.Operation || result.WorkspaceID != request.WorkspaceID ||
		result.Mountpoint != request.Mountpoint || subtle.ConstantTimeCompare([]byte(result.Token), []byte(request.Token)) != 1 {
		return Result{}, errors.New("native mount response does not match the requested identity")
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "native mount operation failed"
		}
		return result, fmt.Errorf("%s", result.Error)
	}
	return result, nil
}
