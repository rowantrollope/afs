package native

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"github.com/rowantrollope/afs/mount/internal/client"
	"github.com/rowantrollope/afs/mount/internal/nfsfs"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs/helpers"
)

type nfsHandler struct {
	nfs.Handler
	export string
	native *nfsfs.FS
	nonce  [16]byte
}

func newNFSHandler(fs billy.Filesystem, export string) *nfsHandler {
	h := &nfsHandler{Handler: helpers.NewCachingHandler(helpers.NewNullAuthHandler(fs), 16384), export: export}
	h.native, _ = fs.(*nfsfs.FS)
	if _, err := rand.Read(h.nonce[:]); err != nil {
		panic(err)
	}
	return h
}
func (h *nfsHandler) Mount(ctx context.Context, c net.Conn, r nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	if string(r.Dirpath) != h.export {
		return nfs.MountStatusErrNoEnt, nil, nil
	}
	status, fs, _ := h.Handler.Mount(ctx, c, r)
	return status, fs, []nfs.AuthFlavor{nfs.AuthFlavorUnix, nfs.AuthFlavorNull}
}
func (h *nfsHandler) RenameHandle(fs billy.Filesystem, oldPath, newPath []string) error {
	if r, ok := h.Handler.(nfs.HandleRenamer); ok {
		return r.RenameHandle(fs, oldPath, newPath)
	}
	return h.Handler.InvalidateHandle(fs, h.Handler.ToHandle(fs, oldPath))
}
func (h *nfsHandler) InvalidateVerifier(path string) {
	if v, ok := h.Handler.(nfs.VerifierInvalidator); ok {
		v.InvalidateVerifier(path)
	}
}

// Closing an NFS listener alone leaves accepted RPC connections alive.
type trackedListener struct {
	net.Listener
	mu     sync.Mutex
	conns  map[*trackedConn]struct{}
	closed bool
}
type trackedConn struct {
	net.Conn
	owner *trackedListener
	once  sync.Once
}

func newTrackedListener(l net.Listener) *trackedListener {
	return &trackedListener{Listener: l, conns: make(map[*trackedConn]struct{})}
}
func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	t := &trackedConn{Conn: c, owner: l}
	l.conns[t] = struct{}{}
	return t, nil
}
func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.conns, c); c.owner.mu.Unlock() })
	return err
}
func (l *trackedListener) Close() error {
	l.mu.Lock()
	l.closed = true
	conns := make([]*trackedConn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	return err
}

func (h *nfsHandler) ToHandle(fs billy.Filesystem, path []string) []byte {
	if adapter, ok := fs.(*nfsfs.FS); ok {
		inode, err := adapter.StableHandle(fs.Join(path...))
		if err != nil {
			return nil
		}
		if inode != 0 {
			handle := make([]byte, 28)
			copy(handle, "AFS1")
			copy(handle[4:20], h.nonce[:])
			binary.BigEndian.PutUint64(handle[20:], inode)
			return handle
		}
	}
	return h.Handler.ToHandle(fs, path)
}
func (h *nfsHandler) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	if h.native != nil {
		if err := h.native.Check(); err != nil {
			h.debugf("FromHandle session check: %T %v", err, err)
			return nil, nil, err
		}
		if len(handle) == 28 && string(handle[:4]) == "AFS1" {
			if subtle.ConstantTimeCompare(handle[4:20], h.nonce[:]) != 1 {
				h.debugf("FromHandle rejected mount nonce inode=%d", binary.BigEndian.Uint64(handle[20:]))
				return nil, nil, errors.New("handle belongs to another mount")
			}
			return h.native.FromStableHandle(binary.BigEndian.Uint64(handle[20:]))
		}
	}
	h.debugf("FromHandle fallback length=%d", len(handle))
	return h.Handler.FromHandle(handle)
}

func (h *nfsHandler) debugf(format string, args ...any) {
	if os.Getenv("AFS_NFS_DEBUG") == "1" {
		log.Printf("nfs handler: "+format, args...)
	}
}
func (h *nfsHandler) VerifierFor(path string, contents []os.FileInfo) uint64 {
	return h.Handler.(nfs.CachingHandler).VerifierFor(path, contents)
}
func (h *nfsHandler) DataForVerifier(path string, id uint64) []os.FileInfo {
	return h.Handler.(nfs.CachingHandler).DataForVerifier(path, id)
}

// MapError preserves native session fencing across the generic RPC adapter's
// filesystem-error wrappers; stale mounts must report stale handles.
func (h *nfsHandler) MapError(err error) error {
	h.debugf("RPC error: %T %v", err, err)
	if errors.Is(err, client.ErrWorkspaceChanged) || errors.Is(err, client.ErrNativeSessionLost) {
		return &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale, WrappedErr: err}
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusIO, WrappedErr: err}
	}
	return err
}
