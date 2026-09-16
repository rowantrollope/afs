//go:build integration

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedConfigConnection(t *testing.T) {
	home := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "HOME="+home, "AFS_STATE_DIR="+filepath.Join(home, "state"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("afs %v: %v stdout=%s stderr=%s", args, err, out, &stderr)
		}
		return out
	}
	// An unreachable endpoint must still save successfully without Redis running.
	out := run("config", "set", "redis", "redis://user:secret@offline.invalid:6379/0")
	if strings.Contains(string(out), "secret") {
		t.Fatal("password printed")
	}
	r := newRedis(t)
	run("config", "set", "redis", "redis://"+r.addr+"/0")
	run("config", "set", "sync.fileSizeCapMB", "512")
	run("create", "saved-config")
	if out := run("--json", "list"); !bytes.Contains(out, []byte("saved-config")) {
		t.Fatalf("saved connection not used: %s", out)
	}
	custom := filepath.Join(home, "alternate", "config.json")
	run("--config", custom, "config", "set", "redis", "redis://"+r.addr+"/1")
	jsonEqual(t, run("--config", custom, "--json", "list"), []any{})
	out = run("--config", custom, "--redis", "redis://"+r.addr+"/0", "--json", "list")
	if !bytes.Contains(out, []byte("saved-config")) {
		t.Fatalf("override failed: %s", out)
	}
}
