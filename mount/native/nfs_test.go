package native

import (
	"bytes"
	"context"
	"errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/mount/internal/client"
	"github.com/rowantrollope/afs/mount/internal/nfsfs"
	nfs "github.com/willscott/go-nfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// An invalidation can be missed during a reconnect. Repeated kernel LOOKUPs
// must not keep the retired inode cached beyond the configured one-second TTL.
func TestNFSRPCRepeatedLookupExpiresMissedReplacement(t *testing.T) {
	_, peer, _, target := testExport(t)
	ctx := context.Background()
	peer.DisableInvalidationPublishing()
	if err := peer.Echo(ctx, "/cached", []byte("before")); err != nil {
		t.Fatal(err)
	}
	_, oldHandle, err := target.Lookup("/cached", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Echo(ctx, "/replacement", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := peer.Rename(ctx, "/replacement", "/cached", 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, handle, err := target.Lookup("/cached", false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(handle, oldHandle) {
			f, err := target.Open("/cached")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := io.ReadAll(f)
			if err != nil || string(got) != "after" {
				t.Fatalf("replacement bytes=%q, %v", got, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("repeated LOOKUP renewed a retired inode past its cache TTL")
}

func testExport(t *testing.T) (*Session, client.Client, *redis.Client, *nfsc.Target) {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	peer := client.New(rdb, "rpc-test")
	if err := peer.Mkdir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs-lite:{rpc-test}:generation", "generation-1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	session, err := StartExport(ctx, Config{Backend: "nfs", RedisURL: "redis://" + server.Addr(), RedisKey: "rpc-test", Generation: "generation-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := session.Unmount(ctx, true); err != nil {
			t.Error(err)
		}
	})
	conn, err := dialTestRPC(session.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	mounter := &nfsc.Mount{Client: conn}
	target, err := mounter.Mount("/rpc-test", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	return session, peer, rdb, target
}
func TestNFSRPCHandleFollowsPeerRenameAndRejectsReplacement(t *testing.T) {
	session, peer, _, target := testExport(t)
	ctx := context.Background()
	if err := peer.Echo(ctx, "/before", []byte("original")); err != nil {
		t.Fatal(err)
	}
	fh, err := target.Open("/before")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := peer.Rename(ctx, "/before", "/after", 0); err != nil {
		t.Fatal(err)
	}
	if err := peer.Echo(ctx, "/before", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("UPDATED!")); err != nil {
		t.Fatalf("write held handle after rename: %v", err)
	}
	got, err := peer.Cat(ctx, "/after")
	if err != nil || string(got) != "UPDATED!" {
		t.Fatalf("renamed inode=%q, %v", got, err)
	}
	got, err = peer.Cat(ctx, "/before")
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement overwritten=%q, %v", got, err)
	}
	if err := peer.Rm(ctx, "/after"); err != nil {
		t.Fatal(err)
	}
	if err := peer.Echo(ctx, "/after", []byte("new inode")); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("must fail")); err == nil {
		t.Fatal("unlinked handle wrote replacement inode")
	}
	got, err = peer.Cat(ctx, "/after")
	if err != nil || string(got) != "new inode" {
		t.Fatalf("new inode=%q, %v", got, err)
	}
	if err := session.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestNFSRPCPeerDirectoryRenameKeepsChildHandle(t *testing.T) {
	_, peer, _, target := testExport(t)
	ctx := context.Background()
	if err := peer.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if err := peer.Echo(ctx, "/dir/child", []byte("old")); err != nil {
		t.Fatal(err)
	}
	fh, err := target.Open("/dir/child")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := peer.Rename(ctx, "/dir", "/moved", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := peer.Cat(ctx, "/moved/child")
	if err != nil || string(got) != "new" {
		t.Fatalf("nested renamed inode=%q, %v", got, err)
	}
}
func TestNFSRPCGenerationFenceAndExportClose(t *testing.T) {
	session, peer, rdb, target := testExport(t)
	ctx := context.Background()
	if err := peer.Echo(ctx, "/file", []byte("initial")); err != nil {
		t.Fatal(err)
	}
	fh, err := target.Open("/file")
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs-lite:{rpc-test}:generation", "generation-2", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("stale")); err == nil {
		t.Fatal("stale handle wrote after generation changed")
	}
	if err := session.Flush(ctx); !errors.Is(err, client.ErrWorkspaceChanged) {
		t.Fatalf("stale flush: %v", err)
	}
	got, err := peer.Cat(ctx, "/file")
	if err != nil || string(got) != "initial" {
		t.Fatalf("stale write changed bytes=%q, %v", got, err)
	}
	_ = fh.Close()
	if err := session.Unmount(ctx, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("closed export still running")
	}
}
func TestNFSRPCAppleDoubleIsSharedPersistentFile(t *testing.T) {
	_, peer, _, target := testExport(t)
	f, err := target.OpenFile("/._metadata", 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("exact\x00bytes")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := peer.Cat(context.Background(), "/._metadata")
	if err != nil || string(got) != "exact\x00bytes" {
		t.Fatalf("sidecar bytes=%q, %v", got, err)
	}
	f, err = target.Open("/._metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err = io.ReadAll(f)
	if err != nil || string(got) != "exact\x00bytes" {
		t.Fatalf("read sidecar=%q, %v", got, err)
	}
	entries, err := target.ReadDirPlus("/")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == "._metadata" {
			found = true
		}
	}
	if !found {
		t.Fatal("sidecar missing from NFS listing")
	}
	if err := target.Remove("/._metadata"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Lookup("/._metadata", false); !os.IsNotExist(err) {
		t.Fatalf("removed sidecar still exists: %v", err)
	}
}

func TestNFSRPCRenameAcrossDirectories(t *testing.T) {
	_, peer, _, target := testExport(t)
	ctx := context.Background()
	if err := peer.Mkdir(ctx, "/src"); err != nil {
		t.Fatal(err)
	}
	if err := peer.Mkdir(ctx, "/dst"); err != nil {
		t.Fatal(err)
	}
	if err := peer.Echo(ctx, "/src/file", []byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := target.Rename("/src/file", "/dst/file"); err != nil {
		t.Fatalf("rename across directory handles: %v", err)
	}
	got, err := peer.Cat(ctx, "/dst/file")
	if err != nil || string(got) != "content" {
		t.Fatalf("rename=%q,%v", got, err)
	}
}

type afterWriteClient struct {
	client.NativeClient
	after func() error
}

func (c *afterWriteClient) WriteInodeAtPath(ctx context.Context, inode uint64, path string, data []byte, off int64) error {
	if err := c.NativeClient.WriteInodeAtPath(ctx, inode, path, data, off); err != nil {
		return err
	}
	if c.after != nil {
		return c.after()
	}
	return nil
}
func TestNFSWriteResponseDoesNotCertifyLaterPeerAttributes(t *testing.T) {
	_, peer, rdb, _ := testExport(t)
	ctx := context.Background()
	if err := peer.Echo(ctx, "/file", []byte("initial")); err != nil {
		t.Fatal(err)
	}
	nativeClient, err := client.NewNative(ctx, rdb, "rpc-test", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	defer nativeClient.Close()
	wrapped := &afterWriteClient{NativeClient: nativeClient, after: func() error {
		if err := peer.Echo(ctx, "/file", []byte("peer publication")); err != nil {
			return err
		}
		future := time.Now().Add(time.Hour).UnixMilli()
		return peer.Utimens(ctx, "/file", future, future)
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := newTrackedListener(listener)
	defer tracked.Close()
	handler := newNFSHandler(nfsfs.New(wrapped, false), "/rpc-test")
	done := make(chan error, 1)
	go func() { done <- nfs.Serve(tracked, handler) }()
	defer func() { _ = tracked.Close(); <-done }()
	conn, err := dialTestRPC(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mounter := &nfsc.Mount{Client: conn}
	target, err := mounter.Mount("/rpc-test", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	_, handle, err := target.Lookup("/file", false)
	if err != nil {
		t.Fatal(err)
	}
	request := struct {
		rpc.Header
		Handle []byte
		Offset uint64
		Count  uint32
		How    uint32
		Data   []byte
	}{
		Header: rpc.Header{Rpcvers: 2, Prog: nfsc.Nfs3Prog, Vers: nfsc.Nfs3Vers, Proc: nfsc.NFSProc3Write, Cred: rpc.AuthNull, Verf: rpc.AuthNull}, Handle: handle, Count: 3, How: 2, Data: []byte("own")}
	body, err := conn.Call(&request)
	if err != nil {
		t.Fatal(err)
	}
	var status uint32
	if err := xdr.Read(body, &status); err != nil || status != 0 {
		t.Fatalf("write status=%d,%v", status, err)
	}
	var response struct {
		Wcc      nfsc.WccData
		Count    uint32
		How      uint32
		Verifier uint64
	}
	if err := xdr.Read(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 3 || response.Wcc.After.IsSet {
		t.Fatalf("write response certified later peer attrs: %+v", response)
	}
	got, err := peer.Cat(ctx, "/file")
	if err != nil || string(got) != "peer publication" {
		t.Fatalf("peer interleaving not exercised: %q,%v", got, err)
	}
}
func TestNFSTransientFailureDoesNotRetireHandle(t *testing.T) {
	h := &nfsHandler{}
	network := &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}
	wrapped := &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale, WrappedErr: network}
	var result *nfs.NFSStatusError
	if !errors.As(h.MapError(wrapped), &result) || result.NFSStatus != nfs.NFSStatusIO {
		t.Fatalf("transient connectivity classified as stale: %v", h.MapError(wrapped))
	}
	wrapped.WrappedErr = client.ErrWorkspaceChanged
	if !errors.As(h.MapError(wrapped), &result) || result.NFSStatus != nfs.NFSStatusStale {
		t.Fatal("generation failure did not remain stale")
	}
}
func TestNFSExportsActualAccessAndChangeTimes(t *testing.T) {
	_, peer, rdb, _ := testExport(t)
	ctx := context.Background()
	if err := peer.Echo(ctx, "/file", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := peer.Utimens(ctx, "/file", 123000, 456000); err != nil {
		t.Fatal(err)
	}
	c, err := client.NewNative(ctx, rdb, "rpc-test", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := nfsfs.New(c, false).Stat("/file")
	if err != nil {
		t.Fatal(err)
	}
	attrs := nfs.ToFileAttribute(info, "/file")
	st, err := peer.Stat(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	if attrs.Atime != nfs.ToNFSTime(time.UnixMilli(st.Atime)) || attrs.Mtime != nfs.ToNFSTime(time.UnixMilli(st.Mtime)) || attrs.Ctime != nfs.ToNFSTime(time.UnixMilli(st.Ctime)) {
		t.Fatalf("wrong NFS timestamps: %+v; stored %+v", attrs, st)
	}
}
