//go:build integration

package filehistory

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// This server is private to the test and never connects to an existing Redis.
func isolatedHistoryRedis(t *testing.T) *redis.Client {
	t.Helper()
	serverPath, err := exec.LookPath("redis-server")
	if err != nil {
		t.Fatal("integration tests require redis-server")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "redis-server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	server := exec.Command(serverPath, "--bind", "127.0.0.1", "--port", port, "--save", "", "--appendonly", "no", "--dir", dir, "--loglevel", "warning")
	server.Stdout, server.Stderr = logFile, logFile
	if err := server.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	var exitErr error
	exited := make(chan struct{})
	go func() { exitErr = server.Wait(); close(exited) }()
	rdb := redis.NewClient(&redis.Options{Addr: address, DialTimeout: 100 * time.Millisecond, ReadTimeout: time.Second, MaxRetries: -1})
	t.Cleanup(func() {
		_ = rdb.Close()
		_ = server.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = server.Process.Kill()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Errorf("owned history Redis pid %d did not exit", server.Process.Pid)
			}
		}
		_ = logFile.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		info, readyErr := rdb.Info(ctx, "server").Result()
		owned := false
		if readyErr == nil {
			for _, line := range strings.Split(info, "\n") {
				if strings.TrimSpace(line) == "process_id:"+strconv.Itoa(server.Process.Pid) {
					owned = true
				}
			}
		}
		select {
		case <-exited:
			log, _ := os.ReadFile(logPath)
			t.Fatalf("owned history Redis pid %d exited before readiness: %v; INFO: %v\nserver log:\n%s", server.Process.Pid, exitErr, readyErr, log)
		default:
		}
		if owned {
			return rdb
		}
		if ctx.Err() != nil {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("owned history Redis pid %d did not become ready: %v; INFO: %v\nserver log:\n%s", server.Process.Pid, ctx.Err(), readyErr, log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRealRedisHistoryPinAndPrune(t *testing.T) {
	rdb := isolatedHistoryRedis(t)
	ctx := context.Background()
	prefix := Prefix("test")
	body := strings.Repeat("immutable history body\x00", 10000)
	old := seedRecord(t, rdb, Record{FileID: "a", Version: 1, Path: "/file"}, 1, body)
	seedRecord(t, rdb, Record{FileID: "a", Version: 2, Path: "/file"}, 2, "latest")
	seedRecord(t, rdb, Record{FileID: "a", Version: 3, Path: "/file", Deleted: true}, 3, "")
	pin := prefix + "read:integration"
	if err := pinScript.Run(ctx, rdb, []string{pathKey(prefix, "/file"), prefix + "records", pin}, prefix, "a", "1", 300000, "/file").Err(); err != nil {
		t.Fatal(err)
	}
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, MaxVersions: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(ctx, rdb, "test", 100); err != nil || n != 1 {
		t.Fatalf("prune=%d,%v", n, err)
	}
	if got := rdb.Get(ctx, pin).Val(); got != body {
		t.Fatal("pinned snapshot was invalidated by retention")
	}
	if rdb.Exists(ctx, prefix+"body:"+old.ID).Val() != 0 {
		t.Fatal("retained old body")
	}
	if rdb.PTTL(ctx, pin).Val() <= 0 {
		t.Fatal("pin has no TTL")
	}
	record, got, err := Get(ctx, rdb, "test", "file", "latest", "")
	if err != nil || string(got) != "latest" || record.Version != 2 {
		t.Fatalf("latest recovery=%+v,%q,%v", record, got, err)
	}
	if n, err := Prune(ctx, rdb, "test", 100); err != nil || n != 0 {
		t.Fatalf("repeat prune=%d,%v", n, err)
	}
	if got := rdb.Get(ctx, prefix+"bytes").Val(); got != "6" {
		t.Fatalf("retention bytes=%q", got)
	}
}

func TestRealRedisHistoryGlobContract(t *testing.T) { checkLuaGlobContract(t, isolatedHistoryRedis(t)) }
