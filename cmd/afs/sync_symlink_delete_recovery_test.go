package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncSymlinkDeleteRecoversConcurrentRemoteRetarget(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const rel = "pointer"
	abs := filepath.Join(env.localRoot, rel)
	if err := env.fsClient.Ln(ctx, "initial", "/"+rel); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.Ln(ctx, "competing-target", "/"+rel); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if target, err := env.fsClient.Readlink(ctx, "/"+rel); err != nil || target != "competing-target" {
		t.Fatalf("remote candidate = %q, error %v", target, err)
	}
	if target, err := os.Readlink(abs); err != nil || target != "competing-target" {
		t.Fatalf("local recovered candidate = %q, error %v", target, err)
	}
}

func TestSyncSymlinkLocalDeleteReachesRemote(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	const rel = "pointer"
	if err := env.fsClient.Ln(ctx, "initial", "/"+rel); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(env.localRoot, rel)); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if env.remoteExists(t, rel) {
		t.Fatal("unchanged remote symlink survived the local deletion")
	}
}
