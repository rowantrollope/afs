package fuse

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
)

var reservedFDs []*os.File

func init() {
	// Both Darwin and Linux invoke a subprocess with one
	// inherited file descriptor to create the mount. To protect
	// against deadlock, we must ensure that file descriptor 3
	// never points to a FUSE filesystem. We do this by simply
	// grabbing fd 3 and never releasing it. (This is not
	// completely foolproof: a preceding init routine could grab fd 3,
	// and then release it later.)
	for {
		r, w, err := os.Pipe()
		if err != nil {
			panic(fmt.Sprintf("os.Pipe(): %v", err))
		}
		w.Close()
		if r.Fd() > 3 {
			r.Close()
			break
		}
		reservedFDs = append(reservedFDs, r)
	}
}

func getConnection(local *os.File) (int, error) {
	return getConnectionContext(context.Background(), local)
}
func mountContext(opts *MountOptions) context.Context {
	if opts != nil && opts.MountContext != nil {
		return opts.MountContext
	}
	return context.Background()
}
func getConnectionContext(ctx context.Context, local *os.File) (int, error) {
	conn, err := net.FileConn(local)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unixConn := conn.(*net.UnixConn)

	var data [4]byte
	control := make([]byte, 4*256)

	_, oobn, _, _, err := unixConn.ReadMsgUnix(data[:], control[:])
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}

	messages, err := syscall.ParseSocketControlMessage(control[:oobn])
	if err != nil {
		return 0, err
	}
	if len(messages) != 1 {
		return 0, fmt.Errorf("getConnection: expect 1 control message, got %#v", messages)
	}
	message := messages[0]

	fds, err := syscall.ParseUnixRights(&message)
	if err != nil {
		return 0, err
	}
	if len(fds) != 1 {
		return 0, fmt.Errorf("getConnection: expect 1 fd, got %#v", fds)
	}
	fd := fds[0]

	if fd < 0 {
		return 0, fmt.Errorf("getConnection: fd < 0: %d", fd)
	}
	return fd, nil
}

// waitMountReadable bounds the initial kernel INIT request. Polling only occurs
// during setup; the normal request/read path remains unchanged.
func waitMountReadable(ctx context.Context, fd int) error {
	if ctx.Done() == nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 50)
		if err != nil && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if n > 0 {
			return nil
		}
	}
}
