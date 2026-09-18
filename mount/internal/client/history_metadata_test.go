package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/rowantrollope/afs/internal/filehistory"
)

func TestHistoryMutationAttributionCoversPublicationPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
		run  func(context.Context, *nativeClient) error
	}{
		{"write", "/file", func(ctx context.Context, c *nativeClient) error { return c.Echo(ctx, "/file", []byte("new")) }},
		{"chunks", "/file", func(ctx context.Context, c *nativeClient) error {
			return c.WriteChunks(ctx, "/file", map[int][]byte{0: []byte("new")}, 3, 3, nil)
		}},
		{"range", "/file", func(ctx context.Context, c *nativeClient) error {
			stat, err := c.Stat(ctx, "/file")
			if err != nil {
				return err
			}
			return c.WriteInodeAt(ctx, stat.Inode, []byte("N"), 0)
		}},
		{"chmod", "/file", func(ctx context.Context, c *nativeClient) error { return c.Chmod(ctx, "/file", 0600) }},
		{"rename", "/moved", func(ctx context.Context, c *nativeClient) error { return c.Mv(ctx, "/file", "/moved") }},
		{"delete", "/file", func(ctx context.Context, c *nativeClient) error { return c.Rm(ctx, "/file") }},
		{"symlink", "/link", func(ctx context.Context, c *nativeClient) error { return c.Ln(ctx, "target", "/link") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			id := "history-metadata-" + test.name
			c := New(rdb, id).(*nativeClient)
			if err := c.Echo(ctx, "/file", []byte("old")); err != nil {
				t.Fatal(err)
			}
			if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
				t.Fatal(err)
			}
			metadata := FileVersionMutationMetadata{Source: " agent_sync ", SessionID: " session ", AgentID: " agent ", User: " user ", CheckpointID: " checkpoint ", Label: " Agent Name ", AgentVersion: " 1.2.3 "}
			actor := WithFileVersionMutationMetadata(ctx, metadata)
			metadata.User = "changed after context construction"
			if err := test.run(actor, c); err != nil {
				t.Fatal(err)
			}
			records := historyRecords(t, ctx, rdb, id, test.path)
			if len(records) == 0 {
				t.Fatal("no captured publication")
			}
			for _, record := range records {
				if record.Source != "agent_sync" || record.SessionID != "session" || record.AgentID != "agent" || record.User != "user" || len(record.CheckpointIDs) != 1 || record.CheckpointIDs[0] != "checkpoint" {
					t.Fatalf("lost publication/baseline attribution: %+v", record)
				}
			}
			entries, err := rdb.XRevRange(ctx, c.keys.changesStream(), "+", "-").Result()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range entries {
				var payload struct {
					Change map[string]any `json:"change"`
				}
				if err := json.Unmarshal([]byte(fmt.Sprint(entry.Values["payload"])), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Change["path"] != test.path || payload.Change["session_id"] != "session" {
					continue
				}
				if payload.Change["source"] != "agent_sync" || payload.Change["agent_id"] != "agent" || payload.Change["user"] != "user" || payload.Change["label"] != "Agent Name" || payload.Change["agent_version"] != "1.2.3" || payload.Change["checkpoint_id"] != "checkpoint" {
					t.Fatalf("lost activity attribution: %+v", payload.Change)
				}
				found = true
				break
			}
			if !found {
				t.Fatal("no attributable activity")
			}
		})
	}
}

func TestHistoryMutationAttributionIsolatedAcrossConcurrentCallers(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-actors"
	c := New(rdb, id)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Echo(ctx, "/native", []byte("native")); err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 2)
	var writers sync.WaitGroup
	for _, name := range []string{"alice", "bob"} {
		writers.Add(1)
		go func(name string) {
			defer writers.Done()
			actor := WithFileVersionMutationMetadata(ctx, FileVersionMutationMetadata{Source: "agent_sync", SessionID: name, AgentID: "agent-" + name, User: name})
			for i := 0; i < 4; i++ {
				if err := c.Echo(actor, "/"+name, []byte(fmt.Sprint(i))); err != nil {
					errors <- err
					return
				}
			}
		}(name)
	}
	writers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		records := historyRecords(t, ctx, rdb, id, "/"+name)
		if len(records) != 4 {
			t.Fatalf("%s versions=%d", name, len(records))
		}
		for _, record := range records {
			if record.Source != "agent_sync" || record.SessionID != name || record.AgentID != "agent-"+name || record.User != name {
				t.Fatalf("actor leak: %+v", record)
			}
		}
	}
	native := historyRecords(t, ctx, rdb, id, "/native")[0]
	if native.Source != "mount" || native.SessionID != "" || native.AgentID != "" || native.User != "" {
		t.Fatalf("native default attribution: %+v", native)
	}
}

func TestHistoryMutationAttributionSurvivesSuppressedNotifications(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "history-suppressed-attribution"
	c := New(rdb, id).(*nativeClient)
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	c.DisableInvalidationPublishing()
	actor := WithFileVersionMutationMetadata(ctx, FileVersionMutationMetadata{Source: "agent_sync", SessionID: "session"})
	if err := c.Echo(actor, "/file", []byte("body")); err != nil {
		t.Fatal(err)
	}
	records := historyRecords(t, ctx, rdb, id, "/file")
	if len(records) != 1 || records[0].Source != "agent_sync" || records[0].SessionID != "session" {
		t.Fatalf("suppressed attribution: %+v", records)
	}
	if n := rdb.XLen(ctx, c.keys.changesStream()).Val(); n != 0 {
		t.Fatalf("suppressed notifications published: %d", n)
	}
}

func TestNativeSessionBindsAttributionAcrossFreshAdapterContexts(t *testing.T) {
	rdb, ctx := setupTestRedis(t)
	id := "native-session-attribution"
	if err := filehistory.SetPolicy(ctx, rdb, id, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	identity := FileVersionMutationMetadata{Source: "mount", SessionID: "managed-session", AgentID: "managed-agent", User: "person", Label: "Native Agent", AgentVersion: "test"}
	native := setupNativeSession(t, rdb, WithFileVersionMutationMetadata(ctx, identity), id)
	// Both adapters create new per-request contexts rather than reusing mount startup.
	if err := native.Echo(context.Background(), "/file", []byte("first")); err != nil {
		t.Fatal(err)
	}
	stat, err := native.Stat(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if err := native.WriteInodeAt(context.Background(), stat.Inode, []byte("N"), 0); err != nil {
		t.Fatal(err)
	}
	records := historyRecords(t, ctx, rdb, id, "/file")
	if len(records) == 0 {
		t.Fatal("native publication did not capture history")
	}
	for _, record := range records {
		if record.SessionID != "managed-session" || record.AgentID != "managed-agent" || record.Source != "mount" {
			t.Fatalf("fresh adapter context lost managed attribution: %+v", record)
		}
	}
}
