package native

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/willscott/go-nfs-client/nfs/rpc"
)

const rpcFixtureDialAttempts = 16

type rpcFixtureDialFunc func(string, string, bool) (*rpc.Client, error)

func dialTestRPC(address string) (*rpc.Client, error) {
	return dialRPCFixture(address, rpc.DialTCP)
}

func dialRPCFixture(address string, dial rpcFixtureDialFunc) (*rpc.Client, error) {
	// This test-only RPC client picks a random explicit source port even for
	// unprivileged connections. Retry an occupied source port without retrying
	// protocol, server, permission, or network failures.
	var last error
	for range rpcFixtureDialAttempts {
		client, err := dial("tcp", address, false)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			return client, err
		}
		last = err
	}
	return nil, fmt.Errorf("NFS test RPC dial exhausted %d source-port attempts: %w", rpcFixtureDialAttempts, last)
}

func TestNFSRPCFixtureDialRetry(t *testing.T) {
	// Obtain the real net.OpError -> os.SyscallError wrapping used by the RPC
	// library, without depending on its random source-port selection.
	occupied, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	connection, bindError := net.DialTCP("tcp4", occupied.Addr().(*net.TCPAddr), occupied.Addr().(*net.TCPAddr))
	if connection != nil {
		connection.Close()
	}
	if !errors.Is(bindError, syscall.EADDRINUSE) {
		t.Fatalf("occupied source port error = %v; want wrapped EADDRINUSE", bindError)
	}

	t.Run("collision_then_success", func(t *testing.T) {
		calls := 0
		want := &rpc.Client{}
		got, err := dialRPCFixture("fixture", func(network, address string, privileged bool) (*rpc.Client, error) {
			calls++
			if network != "tcp" || address != "fixture" || privileged {
				t.Fatalf("changed dial options: %q, %q, %v", network, address, privileged)
			}
			if calls == 1 {
				return nil, bindError
			}
			return want, nil
		})
		if err != nil || got != want || calls != 2 {
			t.Fatalf("dial = %p, %v after %d attempts; want successful second attempt", got, err, calls)
		}
	})
	t.Run("collision_exhaustion", func(t *testing.T) {
		calls := 0
		got, err := dialRPCFixture("fixture", func(string, string, bool) (*rpc.Client, error) {
			calls++
			return nil, bindError
		})
		if got != nil || !errors.Is(err, syscall.EADDRINUSE) || calls != rpcFixtureDialAttempts {
			t.Fatalf("dial = %p, %v after %d attempts; want bounded EADDRINUSE", got, err, calls)
		}
	})
	t.Run("other_error_not_retried", func(t *testing.T) {
		calls := 0
		want := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		got, err := dialRPCFixture("fixture", func(string, string, bool) (*rpc.Client, error) {
			calls++
			return nil, want
		})
		if got != nil || err != want || calls != 1 {
			t.Fatalf("dial = %p, %v after %d attempts; want unchanged non-bind error", got, err, calls)
		}
	})
}
