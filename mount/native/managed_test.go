package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/filehistory"
	"github.com/rowantrollope/afs/internal/managedclient"
	"github.com/rowantrollope/afs/mount/internal/client"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestManagedNFSExportRecordsActualRPCWritesAndCloses(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer rdb.Close()
	ctx := context.Background()
	peer := client.New(rdb, "managed-rpc")
	if err := peer.Mkdir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{managed-rpc}:generation", "generation-1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := filehistory.SetPolicy(ctx, rdb, "managed-rpc", filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	var heartbeats, closes atomic.Int32
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodDelete {
			closes.Add(1)
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			heartbeats.Add(1)
			w.Write([]byte(`{}`))
			return
		}
		var registration managedclient.Registration
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Error(err)
			return
		}
		if registration.ClientKind != "nfs" || registration.Hostname != "test-host" {
			t.Errorf("lost native identity: %+v", registration)
		}
		key := "afs:management:probe:" + registration.SessionID
		if err := rdb.Set(ctx, key, "same-redis", time.Minute).Err(); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(managedclient.Session{SessionID: registration.SessionID, AgentID: registration.AgentID, WorkspaceID: "managed-rpc", StorageProbeKey: key, StorageProbeValue: "same-redis"})
	}))
	defer management.Close()
	registration := managedclient.Registration{SessionID: "sess_native", AgentID: "native-agent", WorkspaceID: "managed-rpc", WorkspaceGeneration: "generation-1", Hostname: "test-host", ClientKind: "nfs", Label: "Native Agent", AFSVersion: "test", User: "person"}
	session, err := StartExport(ctx, Config{Backend: "nfs", RedisURL: "redis://" + server.Addr(), RedisKey: "managed-rpc", Generation: "generation-1", Management: managedclient.Settings{URL: management.URL, Token: "private-token"}, Registration: registration})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Unmount(ctx, true)
	if status := session.Status(ctx); status.Management == nil || !status.Management.Registered {
		t.Fatalf("native registration failed: %+v", status.Management)
	}
	conn, err := dialTestRPC(session.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	target, err := (&nfsc.Mount{Client: conn}).Mount("/managed-rpc", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	f, err := target.OpenFile("/written-by-nfs", 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("actual protocol write")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := peer.Cat(ctx, "/written-by-nfs")
	if err != nil || string(got) != "actual protocol write" {
		t.Fatalf("Redis bytes=%q err=%v", got, err)
	}
	history, err := filehistory.List(ctx, rdb, "managed-rpc", "/written-by-nfs", 100, 0, "")
	if err != nil || len(history.Versions) == 0 {
		t.Fatalf("history missing: %+v %v", history, err)
	}
	for _, record := range history.Versions {
		if record.SessionID != "sess_native" || record.AgentID != "native-agent" || record.Source != "mount" || record.User != "person" {
			t.Fatalf("NFS mutation lost managed identity: %+v", record)
		}
	}
	if heartbeats.Load() == 0 {
		t.Fatal("storage probe was not confirmed")
	}
	if err := session.Unmount(ctx, true); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("native close did not end presence: %d", closes.Load())
	}
}

func TestManagedNativeStartupFailureClosesRegistration(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer rdb.Close()
	ctx := context.Background()
	if err := client.New(rdb, "failed-rpc").Mkdir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "afs:{failed-rpc}:generation", "different-generation", 0).Err(); err != nil {
		t.Fatal(err)
	}
	var closes atomic.Int32
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			closes.Add(1)
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			w.Write([]byte(`{}`))
			return
		}
		key := "afs:management:probe:sess_failed"
		if err := rdb.Set(ctx, key, "proof", time.Minute).Err(); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(managedclient.Session{SessionID: "sess_failed", WorkspaceID: "failed-rpc", StorageProbeKey: key, StorageProbeValue: "proof"})
	}))
	defer management.Close()
	session, err := StartExport(ctx, Config{Backend: "nfs", RedisURL: "redis://" + server.Addr(), RedisKey: "failed-rpc", Generation: "generation-1", Management: managedclient.Settings{URL: management.URL}, Registration: managedclient.Registration{SessionID: "sess_failed", WorkspaceID: "failed-rpc"}})
	if err == nil || session != nil {
		t.Fatalf("expected generation-fenced startup, got %v %v", session, err)
	}
	if closes.Load() != 1 {
		t.Fatalf("failed native startup left registered presence: %d", closes.Load())
	}
}
