package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncCommandResolution(t *testing.T) {
	root := t.TempDir()
	first := mountRecord{Workspace: "one", WorkspaceID: "id-one", LocalPath: filepath.Join(root, "one")}
	reg := mountRegistry{Mounts: []mountRecord{first}}
	for _, target := range []string{"one", "id-one", first.LocalPath} {
		got, err := resolveSyncCommandMount(reg, target)
		if err != nil || got.LocalPath != first.LocalPath {
			t.Fatalf("%s: %+v %v", target, got, err)
		}
	}
	if _, err := resolveSyncCommandMount(reg, filepath.Join(first.LocalPath, "child")); err == nil {
		t.Fatal("accepted nested path")
	}
	reg.Mounts = append(reg.Mounts, mountRecord{Workspace: "one", WorkspaceID: "id-one", LocalPath: filepath.Join(root, "other")})
	if _, err := resolveSyncCommandMount(reg, "one"); err == nil {
		t.Fatal("accepted ambiguous workspace")
	}
	if _, err := resolveSyncCommandMount(reg, first.LocalPath); err != nil {
		t.Fatal(err)
	}
}

func TestSyncWaitValidationJSON(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	reg := mountRegistry{Mounts: []mountRecord{
		{Workspace: "readonly", LocalPath: filepath.Join(t.TempDir(), "ro"), ReadOnly: true, Token: "secret-control-token"},
		{Workspace: "native", LocalPath: filepath.Join(t.TempDir(), "native"), Backend: "fuse"},
		{Workspace: "stopped", LocalPath: filepath.Join(t.TempDir(), "stopped")},
	}}
	if err := saveMountRegistry(reg); err != nil {
		t.Fatal(err)
	}
	a := app{options: cliOptions{json: true}}
	for _, args := range [][]string{{"--wait", "missing"}, {"--wait", "readonly"}, {"--wait", "native"}, {"--wait", "stopped"}, {"--wait", "stopped", "--timeout", "0"}, {"--wait", "stopped", "--timeout", "-1s"}, {"--wait", "stopped", "--timeout", "25h"}, {"--wait"}, {"--timeout", "1s", "stopped"}} {
		raw, err := captureStdout(t, func() error { return a.syncCommand(args) })
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		var out syncWaitOutput
		if e := json.Unmarshal([]byte(raw), &out); e != nil {
			t.Fatalf("%v: invalid JSON %q: %v", args, raw, e)
		}
		if out.Success || out.Verified || out.Error == "" || strings.Contains(raw, "secret-control-token") {
			t.Fatalf("unsafe failure: %s", raw)
		}
	}
}

func TestSyncStatusFiltersNativeAndOmitsSecrets(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	reg := mountRegistry{Mounts: []mountRecord{{Workspace: "sync", LocalPath: filepath.Join(t.TempDir(), "sync"), ReadOnly: true, Token: "secret-control-token"}, {Workspace: "native", Backend: "nfs", LocalPath: t.TempDir()}}}
	if err := saveMountRegistry(reg); err != nil {
		t.Fatal(err)
	}
	a := app{options: cliOptions{json: true}}
	raw, err := captureStdout(t, func() error { return a.syncCommand([]string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["state"] != "stopped" || rows[0]["read_only"] != true || strings.Contains(raw, "secret-control-token") || strings.Contains(raw, "verified") {
		t.Fatalf("unexpected status: %s", raw)
	}
}
