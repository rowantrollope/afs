package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

func TestSyncLiveDirectoryModeDoesNotBecomeLocalChmod(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_directory", true: "remote_chmod"}[existing], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "moved"
			if err := env.fsClient.Mkdir(ctx, "/"+rel); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
			}
			// The requested mode is already fully published before any sync
			// notification. No intermediate native mkdir mode is required.
			if err := env.fsClient.Chmod(ctx, "/"+rel, 0o750); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/" + rel})
			select {
			case op := <-d.reconciler.downloadCh:
				if op.Kind != opDownloadMkdir || op.Mode != 0o750 {
					t.Fatalf("directory notification = %+v", op)
				}
				d.downloader.process(ctx, op)
				result := <-d.reconciler.downloadResCh
				if result.Err != nil || result.Skipped {
					t.Fatalf("directory download = %+v", result)
				}
				d.reconciler.handleDownloadResult(ctx, result)
			default:
				t.Fatal("directory notification did not enqueue a download")
			}
			// A later scan must not interpret sync's own default mkdir mode
			// as an application chmod and send it back to every native peer.
			env.writeRemoteFile(t, rel+"/file", "cross-directory payload")
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			local, err := os.Stat(filepath.Join(env.localRoot, rel))
			if err != nil {
				t.Fatal(err)
			}
			remote, err := env.fsClient.Stat(ctx, "/"+rel)
			if err != nil {
				t.Fatal(err)
			}
			if local.Mode().Perm() != 0o750 || remote.Mode != 0o750 {
				t.Fatalf("directory mode widened by live sync: local=%o remote=%o, want 750", local.Mode().Perm(), remote.Mode)
			}
			if got := env.readLocalFile(t, rel+"/file"); got != "cross-directory payload" {
				t.Fatalf("child contents = %q", got)
			}
		})
	}
}

type replaceParentBeforeEchoClient struct {
	client.Client
	replace func()
}

func (c *replaceParentBeforeEchoClient) Echo(ctx context.Context, path string, data []byte) error {
	c.replace()
	return c.Client.Echo(ctx, path, data)
}

func TestSyncChildUploadRejectsReplacedParent(t *testing.T) {
	env, d := recoveryBaselineDiagnostic(t)
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(env.localRoot, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := env.fsClient.MkdirMode(ctx, "/private", 0o700); err != nil {
		t.Fatal(err)
	}
	const rel = "private/file"
	const content = "local candidate survives replaced parent"
	if err := os.WriteFile(filepath.Join(env.localRoot, rel), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	d.uploader.fs = &replaceParentBeforeEchoClient{Client: env.fsClient, replace: func() {
		if err := env.fsClient.Rm(ctx, "/private"); err != nil {
			t.Fatal(err)
		}
		if err := env.fsClient.MkdirMode(ctx, "/private", 0o750); err != nil {
			t.Fatal(err)
		}
	}}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: rel, KindHint: "create"})
	d.uploader.process(ctx, nextDiagnosticUpload(t, d))
	result := <-d.reconciler.uploadResCh
	if result.Err != nil || !result.Conflict {
		t.Fatalf("publication into replaced parent = %+v", result)
	}
	stat, err := env.fsClient.Stat(ctx, "/private/file")
	if err != nil || stat != nil {
		t.Fatalf("child published in peer's replacement directory: %+v, %v", stat, err)
	}
	parent, err := env.fsClient.Stat(ctx, "/private")
	if err != nil || parent == nil || parent.Mode != 0o750 {
		t.Fatalf("peer parent changed: %+v, %v", parent, err)
	}
	if got := env.readLocalFile(t, rel); got != content {
		t.Fatalf("local candidate = %q", got)
	}
}

func TestSyncLiveLocalDirectoryPublishesMode(t *testing.T) {
	for _, peerExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_700", true: "peer_750"}[peerExists], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "private"
			want := uint32(0o700)
			if peerExists {
				if err := env.fsClient.Mkdir(ctx, "/"+rel); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Chmod(ctx, "/"+rel, 0o750); err != nil {
					t.Fatal(err)
				}
				want = 0o750
			}
			abs := filepath.Join(env.localRoot, rel)
			if err := os.Mkdir(abs, 0o700); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(abs)
			if err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleLocalDir(ctx, rel, abs, info)
			select {
			case op := <-d.reconciler.uploadCh:
				d.uploader.process(ctx, op)
				result := <-d.reconciler.uploadResCh
				if result.Err != nil || result.Skipped {
					t.Fatalf("directory upload = %+v", result)
				}
				d.reconciler.handleUploadResult(ctx, result)
			case <-time.After(time.Second):
				t.Fatal("directory creation was not queued")
			}
			remote, err := env.fsClient.Stat(ctx, "/"+rel)
			if err != nil || remote.Mode != want {
				t.Fatalf("directory publication: remote=%+v error=%v, want mode %o", remote, err, want)
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			info, err = os.Stat(abs)
			if err != nil || uint32(info.Mode().Perm()) != want {
				t.Fatalf("directory did not retain published permissions: local=%v error=%v, want %o", info, err, want)
			}
		})
	}
}

func TestSyncRecoveryDirectoryPlanPreservesNewerPeerMode(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer_created", true: "peer_chmod"}[existing], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "private"
			abs := filepath.Join(env.localRoot, rel)
			if err := os.Mkdir(abs, 0o755); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := d.full.warmStart(ctx, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(abs, 0o700); err != nil {
				t.Fatal(err)
			}
			local, err := d.full.scanLocalMeta()
			if err != nil {
				t.Fatal(err)
			}
			remote, err := d.full.scanRemoteMeta(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			plan := d.full.buildPlan(ctx, local, remote)
			if len(plan) != 1 || plan[0].kind != "mkdir-remote" {
				t.Fatalf("directory plan = %+v", plan)
			}
			if err := env.fsClient.Mkdir(ctx, "/"+rel); err != nil {
				t.Fatal(err)
			}
			if err := env.fsClient.Chmod(ctx, "/"+rel, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := d.full.executePlan(ctx, plan, nil); err != nil {
				t.Fatal(err)
			}
			stat, err := env.fsClient.Stat(ctx, "/"+rel)
			if err != nil || stat.Mode != 0o750 {
				t.Fatalf("stale directory plan overwrote peer mode: %+v, %v", stat, err)
			}
		})
	}
}

func TestSyncChildUploadBeforeDirectoryEventPreservesModes(t *testing.T) {
	for _, kind := range []string{"file", "chunked", "symlink", "directory"} {
		for _, peerExists := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/new", true: "/peer_exists"}[peerExists], func(t *testing.T) {
				env, d := recoveryBaselineDiagnostic(t)
				ctx := context.Background()
				for rel, mode := range map[string]os.FileMode{"private": 0o700, "private/nested": 0o750} {
					if err := os.MkdirAll(filepath.Join(env.localRoot, rel), mode); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(filepath.Join(env.localRoot, rel), mode); err != nil {
						t.Fatal(err)
					}
				}
				want := map[string]uint32{"private": 0o700, "private/nested": 0o750}
				if peerExists {
					want = map[string]uint32{"private": 0o755, "private/nested": 0o710}
					for _, rel := range []string{"private", "private/nested"} {
						if err := env.fsClient.MkdirMode(ctx, "/"+rel, want[rel]); err != nil {
							t.Fatal(err)
						}
					}
				}
				const rel = "private/nested/child"
				abs := filepath.Join(env.localRoot, rel)
				const content = "child before parent notification"
				switch kind {
				case "symlink":
					if err := os.Symlink("target", abs); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(abs, 0o700); err != nil {
						t.Fatal(err)
					}
				default:
					if kind == "chunked" {
						d.reconciler.chunkThreshold = 4
						d.reconciler.chunkSize = 4
					}
					if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// The first watcher notification names the child. No directory
				// creation event or full scan has run to publish its ancestors.
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: rel, KindHint: "create"})
				op := nextDiagnosticUpload(t, d)
				if op.Path != rel {
					t.Fatalf("first queued operation = %+v", op)
				}
				d.uploader.process(ctx, op)
				result := <-d.reconciler.uploadResCh
				if result.Err != nil || result.Skipped || result.Conflict {
					t.Fatalf("child upload = %+v", result)
				}
				d.reconciler.handleUploadResult(ctx, result)
				for _, parent := range []string{"private", "private/nested"} {
					stat, err := env.fsClient.Stat(ctx, "/"+parent)
					if err != nil || stat == nil || stat.Mode != want[parent] {
						t.Fatalf("child-first parent %s = %+v, %v; want mode %o", parent, stat, err, want[parent])
					}
				}
				if kind == "file" || kind == "chunked" {
					data, err := env.fsClient.Cat(ctx, "/"+rel)
					if err != nil || string(data) != content {
						t.Fatalf("child bytes = %q, %v", data, err)
					}
				}
			})
		}
	}
}
