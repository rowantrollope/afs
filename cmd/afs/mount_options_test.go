package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

func TestCheckpointSkipsStoppedReaderButRequiresWriter(t *testing.T) {
	a, _, _, mountpoint := nativeStartupFixture(t)
	ctx := context.Background()
	meta, err := a.service.GetWorkspace(ctx, "startup")
	if err != nil {
		t.Fatal(err)
	}
	rec := mountRecord{WorkspaceID: meta.ID, RedisIdentity: redisIdentity(a.config), LocalPath: mountpoint, ReadOnly: true}
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	if err := a.flushLocalMounts(ctx, "startup"); err != nil {
		t.Fatalf("stopped observer blocked checkpoint: %v", err)
	}
	rec.ReadOnly = false
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	if err := a.flushLocalMounts(ctx, "startup"); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("stopped writer bypassed checkpoint flush: %v", err)
	}
}

func TestMountOptionBoundsAndBackendValidation(t *testing.T) {
	for _, value := range []string{"-1", "4294967296", "1.5", "user"} {
		flags := flag.NewFlagSet("mount", flag.ContinueOnError)
		addMountOptions(flags)
		if _, err := parseCommandFlags(flags, []string{"--uid", value}); err == nil {
			t.Fatalf("accepted uid %q", value)
		}
	}
	for _, backend := range []string{"sync", "nfs"} {
		for _, option := range []string{"--uid=0", "--gid=4294967295", "--allow-other=false"} {
			flags := flag.NewFlagSet("mount", flag.ContinueOnError)
			addMountOptions(flags)
			if _, err := parseCommandFlags(flags, []string{option}); err != nil {
				t.Fatal(err)
			}
			if err := validateMountOptions(flags, backend); err == nil || !strings.Contains(err.Error(), "requires --backend fuse") {
				t.Fatalf("accepted %s with backend %s: %v", option, backend, err)
			}
		}
	}
}

// Exercise the actual CLI parser, saved registry and subprocess bootstrap.
// The retained fixture holds the helper lock without making a kernel mount.
func TestNativeMountOptionsReachHelper(t *testing.T) {
	a, markerPath, gate, mountpoint := nativeStartupFixture(t)
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := captureStdout(t, func() error {
		return a.mount([]string{"startup", mountpoint, "--backend", "fuse", "--readonly", "--uid", "0", "--gid", "4294967295", "--allow-other"})
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := waitStartupMarker(t, markerPath)
	reg, err := loadMountRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := mountByPath(reg, mountpoint)
	if !ok || !rec.ReadOnly || rec.UID == nil || *rec.UID != 0 || rec.GID == nil || *rec.GID != ^uint32(0) || !rec.AllowOther {
		t.Fatalf("mount options not persisted: %+v", rec)
	}
	b := marker.Bootstrap
	if !b.ReadOnly || b.UID == nil || *b.UID != 0 || b.GID == nil || *b.GID != ^uint32(0) || !b.AllowOther {
		t.Fatalf("mount options not passed to helper: %+v", b)
	}
	// Old state remains valid and preserves the process ownership default.
	var old mountRecord
	if err := json.Unmarshal([]byte(`{"backend":"fuse"}`), &old); err != nil || old.ReadOnly || old.UID != nil || old.GID != nil || old.AllowOther {
		t.Fatalf("old state changes defaults: %+v, %v", old, err)
	}
}
