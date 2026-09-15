package main

import (
	"context"
	"testing"
	"time"
)

func TestSyncLifecycleControlRejectsStaleIdentity(t *testing.T) {
	env := newSyncTestEnv(t)
	d := env.startDaemon(t)
	s := startSyncSaveServiceWithToken(context.Background(), d, "current-token")
	t.Cleanup(s.Stop)
	for _, operation := range []string{syncControlOpStatus, syncControlOpShutdown, syncControlOpDetach, syncControlOpSave} {
		request := saveRequestForDaemon(d, 2*time.Second)
		request.Operation, request.Token = operation, "stale-token"
		result, err := exchangeSyncControlRequest(env.localRoot, request, 2*time.Second)
		if result.Success || (result.Error == "" && err == nil) || result.Token == "current-token" {
			t.Fatalf("stale identity accepted or current token exposed: %+v", result)
		}
	}
	select {
	case <-s.Done():
		t.Fatal("stale identity stopped daemon")
	default:
	}
	request := saveRequestForDaemon(d, 2*time.Second)
	request.Operation, request.Token = syncControlOpStatus, "current-token"
	result, err := exchangeSyncControlRequest(env.localRoot, request, 2*time.Second)
	if err != nil || !result.Success || result.Token != request.Token || result.Status == nil || !result.Status.Connected {
		t.Fatalf("authenticated status: %+v, %v", result, err)
	}
}

func TestSyncLifecycleShutdownFlushesThenStops(t *testing.T) {
	env := newSyncTestEnv(t)
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.WatcherDebounce = time.Hour })
	s := startSyncSaveServiceWithToken(context.Background(), d, "shutdown-token")
	t.Cleanup(s.Stop)
	env.writeLocalFile(t, "pending.txt", "must survive unmount")
	request := saveRequestForDaemon(d, 5*time.Second)
	request.Operation, request.Token = syncControlOpShutdown, "shutdown-token"
	result, err := exchangeSyncControlRequest(env.localRoot, request, 5*time.Second)
	if err != nil || !result.Success || result.Save == nil || result.Token != request.Token {
		t.Fatalf("shutdown result: %+v, %v", result, err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not stop supervisor")
	}
	if got := env.readRemoteFile(t, "pending.txt"); got != "must survive unmount" {
		t.Fatalf("remote = %q", got)
	}
	if got := env.readLocalFile(t, "pending.txt"); got != "must survive unmount" {
		t.Fatalf("local = %q", got)
	}
}

func TestSyncLifecycleDetachDoesNotClaimFlush(t *testing.T) {
	env := newSyncTestEnv(t)
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.WatcherDebounce = time.Hour })
	s := startSyncSaveServiceWithToken(context.Background(), d, "detach-token")
	t.Cleanup(s.Stop)
	env.writeLocalFile(t, "pending.txt", "local unsynchronized data")
	request := saveRequestForDaemon(d, 2*time.Second)
	request.Operation, request.Token = syncControlOpDetach, "detach-token"
	result, err := exchangeSyncControlRequest(env.localRoot, request, 2*time.Second)
	if err != nil || !result.Success || result.Save != nil {
		t.Fatalf("detach: %+v, %v", result, err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("detach did not stop supervisor")
	}
	if got := env.readLocalFile(t, "pending.txt"); got != "local unsynchronized data" {
		t.Fatalf("local = %q", got)
	}
}
