package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFullDownloadStagePreservesNewLocalIntent(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		for _, change := range []string{"bytes", "state", "cancel"} {
			t.Run(fmt.Sprintf("conflict=%t/%s", conflict, change), func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				abs := env.writeLocalFile(t, "file", "baseline bytes")
				info, err := os.Lstat(abs)
				if err != nil {
					t.Fatal(err)
				}
				baseline := SyncEntry{Type: "file", Mode: 0o644, Size: info.Size(), Version: 1,
					LocalHash: sha256Hex([]byte("baseline bytes")), RemoteHash: sha256Hex([]byte("baseline bytes"))}
				d.stateWriter.state.Entries["file"] = baseline
				a := syncAction{kind: "download", path: "file", absPath: abs, mode: 0o644,
					conflict: conflict, checkState: true, hasStored: true, storedEntry: baseline,
					localMeta: &observedMeta{kind: "file", mode: 0o644, size: info.Size(),
						mtimeMs: info.ModTime().UnixMilli(), mtimeNs: info.ModTime().UnixNano()}}
				if !d.full.actionStillCurrent(a) {
					t.Fatal("fixture did not start from a current download action")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := "baseline bytes"
				moved, err := d.full.stageDownload(ctx, a, 0o644, func(file *os.File) error {
					if _, err := file.Write([]byte("remote replacement")); err != nil {
						return err
					}
					switch change {
					case "bytes":
						want = "closed local edit made while the download was paused"
						return os.WriteFile(abs, []byte(want), 0o644)
					case "state":
						newer := baseline
						newer.Version++
						newer.Deleted = true
						d.stateWriter.state.Entries["file"] = newer
					case "cancel":
						cancel()
					}
					return nil
				})
				wantErr := errSyncDownloadLocalChanged
				if change == "cancel" {
					wantErr = context.Canceled
				}
				if moved != "" || !errors.Is(err, wantErr) {
					t.Fatalf("stale staged download=%q,%v", moved, err)
				}
				got, readErr := os.ReadFile(abs)
				if readErr != nil || string(got) != want {
					t.Fatalf("staged full download lost local intent: got=%q read=%v", got, readErr)
				}
				entries, err := os.ReadDir(filepath.Dir(abs))
				if err != nil || len(entries) != 1 || entries[0].Name() != "file" {
					t.Fatalf("stale download left temporary/conflict files: %v,%v", entries, err)
				}
			})
		}
	}
}

func TestFullDownloadKeepsConflictAndReadonlySemantics(t *testing.T) {
	for _, tc := range []struct {
		name, local                      string
		mode                             uint32
		readonly, conflict, wantConflict bool
		wantMode                         os.FileMode
		localMode                        os.FileMode
	}{
		{name: "default-mode", local: "old", wantMode: 0o644},
		{name: "conflict", local: "local version", mode: 0o640, conflict: true, wantConflict: true, wantMode: 0o640},
		{name: "identical-readonly", local: "remote version", mode: 0o644, readonly: true, conflict: true, wantMode: 0o444},
		{name: "identical-bytes-writable-mode-conflict", local: "remote version", mode: 0o640, conflict: true, wantConflict: true, wantMode: 0o640},
		{name: "identical-bytes-readonly-mode-conflict", local: "remote version", mode: 0o644, localMode: 0o600, readonly: true, conflict: true, wantConflict: true, wantMode: 0o444},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			abs := env.writeLocalFile(t, "file", tc.local)
			localMode := tc.localMode
			if localMode == 0 {
				localMode = 0o644
			}
			if err := os.Chmod(abs, localMode); err != nil {
				t.Fatal(err)
			}
			env.writeRemoteFile(t, "file", "remote version")
			d.reconciler.readonly = tc.readonly
			if err := d.full.execDownload(context.Background(), syncAction{kind: "download", path: "file", absPath: abs, mode: tc.mode, conflict: tc.conflict}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(abs)
			if err != nil || string(got) != "remote version" {
				t.Fatalf("download=%q,%v", got, err)
			}
			info, err := os.Lstat(abs)
			if err != nil || info.Mode().Perm() != tc.wantMode {
				t.Fatalf("download mode=%v,%v", info, err)
			}
			copies, err := filepath.Glob(abs + ".conflict-*")
			wantCopies := 0
			if tc.wantConflict {
				wantCopies = 1
			}
			if err != nil || len(copies) != wantCopies {
				t.Fatalf("conflict copies=%v,%v", copies, err)
			}
			if tc.wantConflict {
				got, err = os.ReadFile(copies[0])
				if err != nil || string(got) != tc.local {
					t.Fatalf("local conflict=%q,%v", got, err)
				}
				copyInfo, err := os.Stat(copies[0])
				if err != nil || copyInfo.Mode().Perm() != localMode {
					t.Fatalf("local conflict mode=%v,%v; want %o", copyInfo, err, localMode)
				}
			}
			if entry := d.Snapshot().Entries["file"]; entry.RemoteHash != sha256Hex([]byte("remote version")) || entry.Mode != uint32(tc.wantMode) {
				t.Fatalf("wrong committed baseline: %+v", entry)
			}
		})
	}
}
