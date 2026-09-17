package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/filehistory"
)

func TestHistoryCommandHelpIsOffline(t *testing.T) {
	for _, args := range [][]string{{"history"}, {"history", "--help"}, {"history", "help"}} {
		out, err := captureStdout(t, func() error {
			return runCLI(append([]string{"--config", "/missing/config", "--redis", "invalid"}, args...))
		})
		if err != nil || !strings.Contains(out, "Usage: afs history <command>") {
			t.Fatalf("group help %v: %q %v", args, out, err)
		}
	}
	for _, action := range []string{"list", "show", "diff", "restore", "undelete", "export", "policy", "serve"} {
		t.Run(action, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return runCLI([]string{"--config", "/missing/config", "--redis", "invalid", "history", action, "--help"})
			})
			if err != nil || !strings.Contains(out, "Usage: afs history "+action+" ") {
				t.Fatalf("subcommand help: %q %v", out, err)
			}
			if strings.Contains(out, "Usage: afs history <command>") {
				t.Fatal("subcommand help returned only the group overview")
			}
		})
	}
	for _, args := range [][]string{{"history", "unknown"}, {"history", "unknown", "--help"}} {
		err := runCLI(append([]string{"--config", "/missing/config", "--redis", "invalid"}, args...))
		if err == nil || !strings.Contains(err.Error(), "unknown history command") {
			t.Fatalf("unknown subcommand: %v", err)
		}
	}
}

func TestHistoryCommandInvalidArgumentsDoNotConnect(t *testing.T) {
	a := &app{}
	for _, args := range [][]string{
		{}, {"unknown"}, {"list"}, {"list", "repo"}, {"list", "repo", "file", "--limit", "0"},
		{"list", "repo", "file", "--limit", "1001"}, {"list", "repo", "file", "--order", "wrong"},
		{"list", "repo", "file", "--before", "1"}, {"list", "repo", "file", "--lineages"},
		{"list", "repo", "../outside"}, {"list", "repo", "file", "--unknown"},
		{"policy"}, {"policy", "repo", "--mode", "invalid"}, {"policy", "repo", "--mode="},
		{"policy", "repo", "--max-versions", "-1"}, {"policy", "repo", "--max-age-days", "-1"},
		{"policy", "repo", "--max-bytes", "-1"}, {"policy", "repo", "--max-file-bytes", "-1"},
		{"export"}, {"export", "repo", "file"}, {"export", "repo", "../outside", "--to", "/tmp/unwritten"},
	} {
		if err := a.historyCommand(args); err == nil {
			t.Fatalf("history accepted %v", args)
		}
	}
	if a.rdb != nil {
		t.Fatal("argument validation connected to Redis")
	}
}

func TestRecoverVersionPreservesExactBytesAndMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mode uint32
	}{
		{"binary", []byte{0, 255, '\r', '\n', 128, 0}, 0o751},
		{"empty", nil, 0o640},
		{"read-only", []byte("private"), 0o400},
		{"zero-mode", []byte("protected"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recovered")
			record := filehistory.Record{Type: "file", Mode: tc.mode, ContentRef: "immutable-content"}
			if err := writeRecoveredVersion(path, record, tc.data); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != os.FileMode(tc.mode) {
				t.Fatalf("mode: %v %v", info, err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Fatalf("bytes: %v %v", got, err)
			}
		})
	}
}

func TestRecoverVersionRefusesExistingPathsAndPreservesSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := filehistory.Record{Type: "file", Mode: 0o644, ContentRef: "body"}
	for _, kind := range []string{"file", "symlink", "dangling", "directory"} {
		path := filepath.Join(dir, kind)
		var err error
		switch kind {
		case "file":
			err = os.WriteFile(path, []byte("original"), 0o600)
		case "symlink":
			err = os.Symlink(outside, path)
		case "dangling":
			err = os.Symlink("missing", path)
		case "directory":
			err = os.Mkdir(path, 0o700)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := writeRecoveredVersion(path, record, []byte("replacement")); !os.IsExist(err) {
			t.Fatalf("existing %s was accepted: %v", kind, err)
		}
		if err := writeRecoveredVersion(path, filehistory.Record{Type: "symlink", Target: "new"}, nil); !os.IsExist(err) {
			t.Fatalf("existing %s replaced with symlink: %v", kind, err)
		}
	}
	link := filepath.Join(dir, "recovered-link")
	if err := writeRecoveredVersion(link, filehistory.Record{Type: "symlink", Target: outside}, nil); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != outside {
		t.Fatalf("target %q %v", target, err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
		t.Fatalf("target changed: %q %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "file")); err != nil || string(got) != "original" {
		t.Fatalf("existing file changed: %q %v", got, err)
	}
}

func TestRecoverUnavailableVersionCreatesNothing(t *testing.T) {
	for _, record := range []filehistory.Record{{Type: "file", Deleted: true}, {Type: "file"}, {Type: "dir"}} {
		path := filepath.Join(t.TempDir(), "unwritten")
		if err := writeRecoveredVersion(path, record, nil); err == nil {
			t.Fatalf("accepted %+v", record)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("unavailable recovery created path: %v", err)
		}
	}
}

func TestFormatHistoryDistinguishesMetadataOnlyAndDeletion(t *testing.T) {
	page := controlplane.FileHistoryResponse{NextCursor: "opaque-cursor", Lineages: []controlplane.FileHistoryLineage{
		{FileID: "f_test", Versions: []controlplane.FileVersion{
			{VersionID: "v4", Ordinal: 4, Kind: controlplane.FileVersionKindTombstone, Op: "delete", Path: "/line\nbreak"},
			{VersionID: "v3", Ordinal: 3, Kind: controlplane.FileVersionKindFile, Op: "put", MetadataOnly: true, Path: "/line\nbreak"},
		}},
	}}
	out := formatFileHistory(page)
	for _, want := range []string{"f_test", "deleted", "metadata only", "--cursor opaque-cursor", `"/line\nbreak"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}
