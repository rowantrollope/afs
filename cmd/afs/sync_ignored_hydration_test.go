package main

import (
	"context"
	"testing"
)

func TestSyncInitialHydrationPreservesIgnoredLocalFiles(t *testing.T) {
	env := newSyncTestEnv(t)
	env.writeLocalFile(t, ".afsignore", "private/\n")
	env.writeLocalFile(t, "private/secret", "local-only secret")
	env.writeRemoteFile(t, "public.txt", "published remote bytes")
	d, err := newSyncDaemon(syncDaemonConfig{Workspace: env.workspace, LocalRoot: env.localRoot, FS: env.fsClient, Store: env.store})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.full.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := env.readLocalFile(t, "private/secret"); got != "local-only secret" {
		t.Fatalf("ignored local file changed: %q", got)
	}
	if got := env.readLocalFile(t, ".afsignore"); got != "private/\n" {
		t.Fatalf("ignore rules changed: %q", got)
	}
	if got := env.readLocalFile(t, "public.txt"); got != "published remote bytes" {
		t.Fatalf("remote hydration failed: %q", got)
	}
	if env.remoteExists(t, "private/secret") {
		t.Fatal("ignored local contents were published")
	}
}
