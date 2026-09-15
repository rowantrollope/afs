package main

import (
	"testing"
)

func TestSyncSizeCapBytesDefault(t *testing.T) {
	t.Helper()
	if got := syncSizeCapBytes(config{}); got != int64(defaultSyncFileSizeCapMB)*1024*1024 {
		t.Fatalf("syncSizeCapBytes default = %d, want %d", got, int64(defaultSyncFileSizeCapMB)*1024*1024)
	}
	cfg := config{}
	cfg.SyncFileSizeCapMB = 8
	if got := syncSizeCapBytes(cfg); got != 8*1024*1024 {
		t.Fatalf("syncSizeCapBytes(8) = %d, want %d", got, 8*1024*1024)
	}
}

func TestEchoSuppressorMarkConsume(t *testing.T) {
	t.Helper()
	e := newEchoSuppressor()
	e.markFile("foo", "deadbeef")
	if _, ok := e.consume("foo"); !ok {
		t.Fatalf("expected echo expectation present")
	}
	if _, ok := e.consume("foo"); ok {
		t.Fatalf("echo expectation should be one-shot")
	}
	e.markSymlink("link", "/tmp/x")
	got, ok := e.consume("link")
	if !ok || got.kind != "symlink" || got.hash != "/tmp/x" {
		t.Fatalf("symlink expectation mismatch: %+v ok=%v", got, ok)
	}
}
