package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/internal/mountcontrol"
)

func TestBootstrapDecodesExplicitZeroOwnership(t *testing.T) {
	dir := t.TempDir()
	zero, group := uint32(0), uint32(1234)
	boot := mountcontrol.Bootstrap{RuntimeDir: dir, ReadyPath: filepath.Join(dir, "ready"), Mountpoint: filepath.Join(dir, "mount"), WorkspaceID: "ws", Token: "token", Backend: "fuse", ReadOnly: true, UID: &zero, GID: &group, AllowOther: true}
	raw, err := json.Marshal(boot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bootstrap")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadBootstrap(path)
	if err != nil || !got.ReadOnly || !got.AllowOther || got.UID == nil || *got.UID != 0 || got.GID == nil || *got.GID != group {
		t.Fatalf("bootstrap options: %+v, %v", got, err)
	}
}
