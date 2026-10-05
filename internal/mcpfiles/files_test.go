package mcpfiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testLifecycle struct{ check, verify error }

func (l *testLifecycle) Check(context.Context) error  { return l.check }
func (l *testLifecycle) Verify(context.Context) error { return l.verify }
func folderForTest(t *testing.T) (*Folder, *testLifecycle) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".afs-lite-sync"), 0o700); err != nil {
		t.Fatal(err)
	}
	l := &testLifecycle{}
	f, err := Open(root, "shared", l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, l
}

func TestImmutableWritesAndPublicationRetry(t *testing.T) {
	f, l := folderForTest(t)
	ctx := context.Background()
	data := []byte("# shared\nλ\n")
	l.verify = errors.New("Redis down")
	created, err := f.Write(ctx, "notes/readme.md", data, digest(data))
	if !created || err == nil {
		t.Fatalf("unconfirmed create: %v %v", created, err)
	}
	l.verify = nil
	created, err = f.Write(ctx, "notes/readme.md", data, digest(data))
	if created || err != nil {
		t.Fatalf("retry: %v %v", created, err)
	}
	other := []byte("different")
	if _, err = f.Write(ctx, "notes/readme.md", other, digest(other)); err == nil {
		t.Fatal("overwrote existing file")
	}
	if got, err := f.Read("notes/readme.md"); err != nil || string(got) != string(data) {
		t.Fatalf("read %q %v", got, err)
	}
	if _, err = f.Write(ctx, "wrong.md", data, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted incorrect hash")
	}
	if _, err = os.Stat(filepath.Join(f.directory, "wrong.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bad hash created file")
	}
}

func TestPathIsolationAndFileLimits(t *testing.T) {
	f, _ := folderForTest(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"escape": outside, "internal": ".afs-lite-sync", "leaf": secret} {
		if err := os.Symlink(target, filepath.Join(f.directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(secret, filepath.Join(f.directory, "hardlink")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret", "/secret", "a/../../secret", "a//b", "a/./b", "a\\b", "C:secret", ".afsignore", ".afs-lite-sync/mcp-write.lock", "dir/.AFS-sync.tmp.x", "escape/secret", "internal/mcp-write.lock", "leaf", "hardlink"} {
		t.Run(p, func(t *testing.T) {
			if _, err := f.Read(p); err == nil {
				t.Fatal("unsafe read accepted")
			}
			if _, err := f.Write(context.Background(), p, []byte("bad"), digest([]byte("bad"))); err == nil {
				t.Fatal("unsafe write accepted")
			}
		})
	}
	if data, _ := os.ReadFile(secret); string(data) != "private" {
		t.Fatal("outside file changed")
	}
	if _, err := f.List("escape"); err == nil {
		t.Fatal("listed outside directory")
	}
	entries, err := f.List("")
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsafe entries visible: %+v %v", entries, err)
	}
	for _, data := range [][]byte{[]byte(strings.Repeat("x", MaxFileBytes+1)), {0xff}} {
		if _, err := f.Write(context.Background(), "invalid", data, digest(data)); err == nil {
			t.Fatal("invalid content accepted")
		}
		if err := os.WriteFile(filepath.Join(f.directory, "invalid"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Read("invalid"); err == nil {
			t.Fatal("invalid read accepted")
		}
	}
	data := []byte(strings.Repeat("x", MaxFileBytes))
	if _, err := f.Write(context.Background(), "maximum", data, digest(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(context.Background(), "empty", nil, digest(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentAdaptersRaceAndRecoverStages(t *testing.T) {
	f, _ := folderForTest(t)
	f2, err := Open(f.directory, "shared", &testLifecycle{})
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, folder := range []*Folder{f, f2} {
		wg.Add(1)
		go func(f *Folder) {
			defer wg.Done()
			_, err := f.Write(context.Background(), "same", []byte("complete"), digest([]byte("complete")))
			errs <- err
		}(folder)
	}
	wg.Wait()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	// Reproduce termination after atomic link but before staging unlink.
	stage := filepath.Join(f.directory, ".afs-lite-sync", "mcp-"+strings.Repeat("a", 32)+".tmp")
	if err := os.Link(filepath.Join(f.directory, "same"), stage); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read("same"); err == nil {
		t.Fatal("hard link accepted before recovery")
	}
	if err := f2.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.Read("same"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging link survived recovery")
	}
	unlock, err := f.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := f2.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored deadline: %v", err)
	}
	unlock()
}

func TestReadinessRejectsUnusableMount(t *testing.T) {
	f, l := folderForTest(t)
	l.check = errors.New("disconnected")
	if err := f.Ready(context.Background()); err == nil {
		t.Fatal("reported ready while disconnected")
	}
	l.check = nil
	if err := os.WriteFile(filepath.Join(f.directory, ".afsignore"), []byte("private/"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("accepted ignored workspace")
	}
	if err := os.Remove(filepath.Join(f.directory, ".afsignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.directory, f.directory+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(f.directory + "-old") })
	if err := os.Mkdir(f.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("accepted replaced root")
	}
}

func TestSDKDiscoveryStructuredErrorsAndAnnotations(t *testing.T) {
	f, _ := folderForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := f.Server().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "independent-client", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.InitializeResult().ServerInfo.Name != "afs-mcp" {
		t.Fatal("wrong initialized server")
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("tools: %+v", tools)
	}
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("missing schema or truthful hint: %+v", tool)
		}
		if tool.Name == "write_file" && (tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint) {
			t.Fatal("wrong write annotations")
		}
		if tool.Name != "write_file" && !tool.Annotations.ReadOnlyHint {
			t.Fatal("wrong read annotation")
		}
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"path": "../escape"}})
	if err != nil || !result.IsError || result.StructuredContent == nil {
		t.Fatalf("unstructured error: %+v %v", result, err)
	}
}
