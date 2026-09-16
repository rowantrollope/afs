package mountcontrol

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLongRuntimeUsesShortPrivateSocket(t *testing.T) {
	runtime := filepath.Join(t.TempDir(), strings.Repeat("long-runtime-", 30))
	if len(SocketPath(runtime)) >= 104 {
		t.Fatalf("socket path exceeds Darwin limit: %s", SocketPath(runtime))
	}
	if SocketPath(runtime) == SocketPath(runtime+"-other") {
		t.Fatal("runtime sockets collide")
	}
	if err := PrepareSocketDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Dir(SocketPath(runtime)))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("socket directory: %v, %v", info, err)
	}
	listener, err := net.Listen("unix", SocketPath(runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	req := Request{Operation: Status, Token: "secret", WorkspaceID: "workspace", Mountpoint: "/tmp/mount"}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var got Request
		if json.NewDecoder(conn).Decode(&got) != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(Result{Version: Version, Operation: got.Operation, Token: got.Token, WorkspaceID: got.WorkspaceID, Mountpoint: got.Mountpoint, Success: true, Connected: true})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := Call(ctx, runtime, req)
	if err != nil || !result.Connected {
		t.Fatalf("call=%+v, %v", result, err)
	}
}
func TestCallRejectsResponseIdentity(t *testing.T) {
	for _, wrong := range []string{"token", "workspace", "mountpoint", "operation", "version"} {
		t.Run(wrong, func(t *testing.T) {
			runtime := t.TempDir()
			if err := PrepareSocketDir(); err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("unix", SocketPath(runtime))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				var req Request
				if json.NewDecoder(c).Decode(&req) != nil {
					return
				}
				r := Result{Version: Version, Operation: req.Operation, Token: req.Token, WorkspaceID: req.WorkspaceID, Mountpoint: req.Mountpoint, Success: true}
				switch wrong {
				case "token":
					r.Token = "wrong"
				case "workspace":
					r.WorkspaceID = "wrong"
				case "mountpoint":
					r.Mountpoint = "wrong"
				case "operation":
					r.Operation = "wrong"
				case "version":
					r.Version++
				}
				_ = json.NewEncoder(c).Encode(r)
			}()
			if _, err := Call(context.Background(), runtime, Request{Operation: Flush, Token: "secret", WorkspaceID: "ws", Mountpoint: "/tmp/m"}); err == nil {
				t.Fatal("accepted mismatched response")
			}
		})
	}
}
