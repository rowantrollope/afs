package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmountAcceptsWorkspaceOrDirectory(t *testing.T) {
	for _, selector := range []string{"workspace", "absolute directory", "relative directory", "symlink directory"} {
		t.Run(selector, func(t *testing.T) {
			t.Setenv("AFS_STATE_DIR", t.TempDir())
			root, err := normalizeMountPath(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			rec := mountRecord{Workspace: "project", WorkspaceID: "ws_project", LocalPath: root}
			other := mountRecord{Workspace: "other", LocalPath: t.TempDir()}
			if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec, other}}); err != nil {
				t.Fatal(err)
			}
			target := rec.Workspace
			switch selector {
			case "absolute directory":
				target = root
			case "relative directory":
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				target, err = filepath.Rel(cwd, root)
				if err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				target = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, target); err != nil {
					t.Fatal(err)
				}
			}
			a := app{options: cliOptions{json: true}}
			out, err := captureStdout(t, func() error { return a.unmount([]string{target, "--force"}) })
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Directory string `json:"directory"`
				Detached  bool   `json:"detached"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Directory != root || !result.Detached {
				t.Fatalf("unexpected result: %s (%v)", out, err)
			}
			reg, err := loadMountRegistry()
			if err != nil || len(reg.Mounts) != 1 || reg.Mounts[0].LocalPath != other.LocalPath {
				t.Fatalf("wrong registration removed: %+v (%v)", reg, err)
			}
		})
	}
}

func TestUnmountRejectsAmbiguityBeforeChangingRegistry(t *testing.T) {
	for _, collision := range []string{"same workspace", "different databases", "workspace and directory"} {
		t.Run(collision, func(t *testing.T) {
			t.Setenv("AFS_STATE_DIR", t.TempDir())
			first := mountRecord{Workspace: "project", WorkspaceID: "ws_project", LocalPath: t.TempDir(), RedisIdentity: "database-one"}
			second := first
			second.LocalPath = t.TempDir()
			switch collision {
			case "different databases":
				second.WorkspaceID, second.RedisIdentity = "ws_other", "database-two"
			case "workspace and directory":
				second.Workspace, second.WorkspaceID = "other", "ws_other"
				var err error
				second.LocalPath, err = expandPath(first.Workspace)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{first, second}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(mountRegistryPath())
			if err != nil {
				t.Fatal(err)
			}
			a := app{options: cliOptions{json: true}}
			for _, args := range [][]string{{first.Workspace}, {first.Workspace, "--force"}} {
				out, err := captureStdout(t, func() error { return a.unmount(args) })
				if err == nil || !strings.Contains(err.Error(), "multiple registered mounts") || !strings.Contains(err.Error(), "specify an exact directory") || out != "" {
					t.Fatalf("ambiguous unmount: output=%q err=%v", out, err)
				}
			}
			after, err := os.ReadFile(mountRegistryPath())
			if err != nil || string(after) != string(before) {
				t.Fatalf("ambiguous unmount changed registry: %v", err)
			}
			if _, err := captureStdout(t, func() error { return a.unmount([]string{first.LocalPath, "--force"}) }); err != nil {
				t.Fatalf("exact directory did not disambiguate: %v", err)
			}
			reg, err := loadMountRegistry()
			if err != nil || len(reg.Mounts) != 1 || reg.Mounts[0].LocalPath != second.LocalPath {
				t.Fatalf("wrong registration removed: %+v (%v)", reg, err)
			}
		})
	}
}

func TestUnmountMissingWorkspaceAndStoppedDaemonPreserveRegistry(t *testing.T) {
	t.Setenv("AFS_STATE_DIR", t.TempDir())
	rec := mountRecord{Workspace: "project", LocalPath: t.TempDir()}
	if err := saveMountRegistry(mountRegistry{Mounts: []mountRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	a := app{options: cliOptions{json: true}}
	for _, tc := range []struct{ target, want string }{
		{"missing", "no registered mount matches"},
		{rec.Workspace, "sync daemon is not running"},
	} {
		_, err := captureStdout(t, func() error { return a.unmount([]string{tc.target}) })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", tc.target, err)
		}
	}
	reg, err := loadMountRegistry()
	if err != nil || len(reg.Mounts) != 1 || reg.Mounts[0].LocalPath != rec.LocalPath {
		t.Fatalf("failed unmount changed registry: %+v (%v)", reg, err)
	}
}

func TestUnmountHelpAcceptsWorkspaceOrDirectory(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"unmount", "--help"}} {
		out, err := captureStdout(t, func() error { return runCLI(args) })
		if err != nil || !strings.Contains(out, "unmount <workspace|directory>") {
			t.Fatalf("%v: output=%q error=%v", args, out, err)
		}
		if len(args) > 1 && !strings.Contains(out, "if ambiguous, specify the local directory") {
			t.Fatalf("help omits ambiguity guidance: %s", out)
		}
	}
}
