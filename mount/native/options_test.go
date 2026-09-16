package native

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestFUSEOptionsPreserveDefaultAndExplicitZero(t *testing.T) {
	defaults := fuseOptions(context.Background(), Config{})
	if defaults.UID != uint32(os.Getuid()) || defaults.GID != uint32(os.Getgid()) || defaults.AllowOther || defaults.ReadOnly {
		t.Fatalf("changed default mount options: %+v", defaults)
	}
	zero, group := uint32(0), uint32(1234)
	opts := fuseOptions(context.Background(), Config{UID: &zero, GID: &group, ReadOnly: true, AllowOther: true})
	if opts.UID != 0 || opts.GID != 1234 || !opts.AllowOther || !opts.ReadOnly {
		t.Fatalf("explicit FUSE options lost: %+v", opts)
	}
	if _, err := StartExport(context.Background(), Config{Backend: "nfs", UID: &zero}); err == nil {
		t.Fatal("NFS accepted a FUSE ownership option")
	}
}

func TestNFSReadOnlyExportRejectsMutations(t *testing.T) {
	_, peer, rdb, _ := testExport(t)
	ctx := context.Background()
	if err := peer.Echo(ctx, "/file", []byte("published")); err != nil {
		t.Fatal(err)
	}
	session, err := StartExport(ctx, Config{Backend: "nfs", RedisURL: "redis://" + rdb.Options().Addr, RedisKey: "rpc-test", Generation: "generation-1", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Unmount(context.Background(), true) })
	conn, err := dialTestRPC(session.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	target, err := (&nfsc.Mount{Client: conn}).Mount("/rpc-test", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	f, err := target.Open("/file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got, err := io.ReadAll(f); err != nil || string(got) != "published" {
		t.Fatalf("reader cannot read: %q, %v", got, err)
	}
	if _, err := f.Write([]byte("forbidden")); err == nil {
		t.Fatal("read-only NFS write succeeded")
	}
	if created, err := target.OpenFile("/forbidden", 0644); err == nil {
		created.Close()
		t.Fatal("read-only NFS create succeeded")
	}
	if err := target.Remove("/file"); err == nil {
		t.Fatal("read-only NFS remove succeeded")
	}
	if err := target.Rename("/file", "/renamed"); err == nil {
		t.Fatal("read-only NFS rename succeeded")
	}
	if got, err := peer.Cat(ctx, "/file"); err != nil || string(got) != "published" {
		t.Fatalf("reader changed published data: %q, %v", got, err)
	}
	flushCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := session.Flush(flushCtx); err != nil {
		t.Fatalf("read-only barrier failed: %v", err)
	}
}
