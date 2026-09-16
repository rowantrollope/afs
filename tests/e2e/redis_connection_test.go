//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireRedisConnectionError(t *testing.T, diagnostic, addr string) {
	t.Helper()
	prefix := "afs: Failed to connect to Redis on " + addr + ": "
	if !strings.HasPrefix(diagnostic, prefix) || !strings.HasSuffix(diagnostic, "\n") {
		t.Fatalf("unfriendly connection error: %q", diagnostic)
	}
	reason := strings.TrimSuffix(strings.TrimPrefix(diagnostic, prefix), "\n")
	if strings.TrimSpace(reason) == "" || strings.Contains(reason, "\n") {
		t.Fatalf("connection failure must contain one reason without retry logs: %q", diagnostic)
	}
	for _, raw := range []string{"redis://", "rediss://", "dial tcp", "i/o timeout", "pool.go", "connection pool"} {
		if strings.Contains(diagnostic, raw) {
			t.Errorf("connection failure leaked transport details %q: %q", raw, diagnostic)
		}
	}
}

func TestRedisConnectionRefusedPresentation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	c := newCLI(t, &redisServer{addr: addr})
	const user, password = "private-user", "private-password"
	endpoint := (&url.URL{Scheme: "redis", Host: addr, User: url.UserPassword(user, password), Path: "/0"}).String()
	for _, machine := range []bool{false, true} {
		name := "human"
		if machine {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"--redis", endpoint, "list"}
			if machine {
				args = append(args, "--json")
			}
			out, diagnostic, err := c.runTimeout(15*time.Second, nil, args...)
			if err == nil || len(out) != 0 {
				t.Fatalf("failed connection must exit nonzero with empty stdout: err=%v stdout=%q", err, out)
			}
			requireRedisConnectionError(t, string(diagnostic), addr)
			for _, secret := range []string{user, password, endpoint} {
				if strings.Contains(string(diagnostic), secret) {
					t.Errorf("connection error exposed credential-bearing configuration: %q", diagnostic)
				}
			}
		})
	}
}

func TestRedisConnectionTimeoutPresentation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var connections []net.Conn
	accepted, stopped := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
			// Keep the socket open without replying to Redis's handshake.
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-stopped
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	addr := listener.Addr().String()
	c := newCLI(t, &redisServer{addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := c.command(ctx, "list")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || ctx.Err() != nil || stdout.Len() != 0 {
		t.Fatalf("Redis timeout must fail independently of the test deadline: err=%v context=%v stdout=%q", err, ctx.Err(), stdout.Bytes())
	}
	select {
	case <-accepted:
	default:
		t.Fatal("CLI did not connect to the owned silent listener")
	}
	diagnostic := stderr.String()
	requireRedisConnectionError(t, diagnostic, addr)
	wantDiagnostic := "afs: Failed to connect to Redis on " + addr + ": connection timed out\n"
	if diagnostic != wantDiagnostic {
		t.Fatalf("silent Redis listener must report a timeout: got %q, want %q", diagnostic, wantDiagnostic)
	}
}

func TestRedisConnectionHealthyAndOfflineOutput(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	for _, machine := range []bool{false, true} {
		args := []string{"list"}
		if machine {
			args = append(args, "--json")
		}
		out, diagnostic, err := c.runTimeout(10*time.Second, nil, args...)
		if err != nil || len(diagnostic) != 0 || len(out) == 0 {
			t.Fatalf("healthy output: machine=%t err=%v stdout=%q stderr=%q", machine, err, out, diagnostic)
		}
		if json.Valid(out) != machine {
			t.Fatalf("healthy output mode changed: machine=%t stdout=%q", machine, out)
		}
	}
	r.stop()
	for _, args := range [][]string{{"status"}, {"--help"}, {"--version"}} {
		t.Run(args[0], func(t *testing.T) {
			out, diagnostic, err := c.runTimeout(5*time.Second, nil, args...)
			if err != nil || len(diagnostic) != 0 || len(out) == 0 {
				t.Fatalf("offline command attempted a Redis connection: %v err=%v stdout=%q stderr=%q", args, err, out, diagnostic)
			}
		})
	}
}
