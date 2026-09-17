package main

import (
	"context"
	"encoding/json"
	"flag"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
	"github.com/rowantrollope/afs/internal/version"
)

func TestSyncAttributionMountOptionsPreserveDeclaredLabels(t *testing.T) {
	for name, value := range map[string]string{"AFS_SESSION_ID": "env-session", "AFS_AGENT_ID": "env-agent", "AFS_USER": "env-user", "AFS_AGENT_LABEL": "env-label", "AFS_AGENT_VERSION": "env-version"} {
		t.Setenv(name, value)
	}
	flags := flag.NewFlagSet("mount", flag.ContinueOnError)
	opts := addMountOptions(flags)
	if _, err := parseCommandFlags(flags, []string{"--session", " cli-session ", "--agent-id", "cli-agent", "--user", "cli-user"}); err != nil {
		t.Fatal(err)
	}
	opts.normalizeAttribution()
	if err := validateMountOptions(flags, "sync"); err != nil {
		t.Fatal(err)
	}
	if opts.SessionID != "cli-session" || opts.AgentID != "cli-agent" || opts.User != "cli-user" || opts.Label != "env-label" || opts.AgentVersion != "env-version" {
		t.Fatalf("mount attribution flag/env precedence: %+v", opts)
	}
	if err := validateMountOptions(flags, "fuse"); err == nil {
		t.Fatal("native mount silently accepted sync-only attribution options")
	}
	boot := syncDaemonBootstrap{Record: mountRecord{SessionID: opts.SessionID, AgentID: opts.AgentID, User: opts.User, Label: opts.Label, AgentVersion: opts.AgentVersion}}
	body, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded syncDaemonBootstrap
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Record != boot.Record {
		t.Fatalf("subprocess bootstrap lost attribution: %+v %v", decoded, err)
	}
	var legacy mountRecord
	if err := json.Unmarshal([]byte(`{"workspace":"old"}`), &legacy); err != nil || legacy.SessionID != "" || legacy.AgentID != "" || legacy.User != "" {
		t.Fatalf("legacy mount acquired invented identity: %+v %v", legacy, err)
	}
}

func TestSyncAttributionSurvivesInitialUploadExplicitSaveAndResume(t *testing.T) {
	env := newSyncTestEnv(t)
	ctx := context.Background()
	if err := filehistory.SetPolicy(ctx, env.rdb, env.mountKey, filehistory.Policy{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	env.writeLocalFile(t, "initial.txt", "initial upload")
	d := env.startDaemon(t, func(cfg *syncDaemonConfig) {
		cfg.SessionID, cfg.AgentID, cfg.User, cfg.Label = "sync-session", "sync-agent", "sync-user", "Sync Agent"
	})
	assertRecord := func(path string) {
		t.Helper()
		page, err := filehistory.List(ctx, env.rdb, env.mountKey, path, 20, 0, "")
		if err != nil || len(page.Versions) == 0 {
			t.Fatalf("history for %s: %+v %v", path, page, err)
		}
		for _, record := range page.Versions {
			if record.Source != "agent_sync" || record.SessionID != "sync-session" || record.AgentID != "sync-agent" || record.User != "sync-user" {
				t.Fatalf("sync publication lost attribution: %+v", record)
			}
		}
	}
	assertRecord("/initial.txt")
	if err := d.watcher.Close(); err != nil {
		t.Fatal(err)
	}
	service := &syncSaveService{ctx: context.Background(), active: d}
	t.Cleanup(func() {
		if service.active != nil {
			service.active.Stop()
		}
	})
	env.writeLocalFile(t, "saved.txt", "explicit verification")
	result := service.save(saveRequestForDaemon(d, 10*time.Second))
	if !result.Success || service.active == nil {
		t.Fatalf("explicit save failed: %+v", result)
	}
	assertRecord("/saved.txt")
	env.writeLocalFile(t, "resumed.txt", "steady state")
	assertEventually(t, 5*time.Second, "resumed worker published", func() bool {
		return env.remoteExists(t, "resumed.txt") && env.readRemoteFile(t, "resumed.txt") == "steady state"
	})
	assertRecord("/resumed.txt")
	activity, err := controlplane.NewService(env.cp).GetFileHistoryChanges(ctx, env.workspace, controlplane.FileHistoryChangesRequest{Path: "/resumed.txt", NewestFirst: true})
	if err != nil || len(activity.Entries) == 0 {
		t.Fatalf("sync activity: %+v %v", activity, err)
	}
	for _, entry := range activity.Entries {
		if entry.Source != "agent_sync" || entry.SessionID != "sync-session" || entry.AgentID != "sync-agent" || entry.User != "sync-user" || entry.Label != "Sync Agent" || entry.AgentVersion != version.String() {
			t.Fatalf("sync activity lost current caller/build labels: %+v", entry)
		}
	}
}
