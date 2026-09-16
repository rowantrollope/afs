package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rowantrollope/afs/mount/client"
)

func directoryChmodDiagnostic(t *testing.T) (*syncTestEnv, *syncDaemon, string) {
	t.Helper()
	env, d := recoveryBaselineDiagnostic(t)
	abs := filepath.Join(env.localRoot, "directory")
	if err := os.Mkdir(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return env, d, abs
}

func TestSyncTrackedDirectoryChmodAfterRename(t *testing.T) {
	env, d, old := directoryChmodDiagnostic(t)
	ctx := context.Background()
	moved := filepath.Join(env.localRoot, "moved")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	if err := d.full.warmStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(moved, 0o750); err != nil {
		t.Fatal(err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "moved", KindHint: "chmod"})
	op := nextDiagnosticUpload(t, d)
	if op.Kind != opUploadChmod || op.Mode != 0o750 {
		t.Fatalf("directory chmod operation = %+v", op)
	}
	d.uploader.process(ctx, op)
	res := <-d.reconciler.uploadResCh
	if res.Err != nil || res.Conflict || res.Skipped {
		t.Fatalf("chmod upload = %+v", res)
	}
	d.reconciler.handleUploadResult(ctx, res)
	remote, err := env.fsClient.Stat(ctx, "/moved")
	if err != nil || remote == nil || remote.Mode != 0o750 {
		t.Fatalf("directory chmod not published: %+v, %v", remote, err)
	}
	if stored := d.Snapshot().Entries["moved"]; stored.Mode != 0o750 {
		t.Fatalf("directory chmod baseline = %+v", stored)
	}
	// Repeated notifications for the acknowledged mode must not write again.
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "moved", KindHint: "chmod"})
	if len(d.reconciler.uploadCh) != 0 {
		t.Fatal("unchanged directory chmod queued another upload")
	}
}

func TestSyncDirectoryEchoMatchesMode(t *testing.T) {
	for _, localEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "own_chmod", true: "application_chmod"}[localEdit], func(t *testing.T) {
			env, d, abs := directoryChmodDiagnostic(t)
			ctx := context.Background()
			if err := env.fsClient.Chmod(ctx, "/directory", 0o750); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/directory"})
			d.downloader.process(ctx, <-d.reconciler.downloadCh)
			res := <-d.reconciler.downloadResCh
			if res.Err != nil || res.Skipped {
				t.Fatalf("directory inbound result = %+v", res)
			}
			d.reconciler.handleDownloadResult(ctx, res)
			if mode := d.Snapshot().Entries["directory"].Mode; mode != 0o750 {
				t.Fatalf("inbound baseline mode = %o", mode)
			}
			if localEdit {
				if err := os.Chmod(abs, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
			if !localEdit {
				if len(d.reconciler.uploadCh) != 0 {
					t.Fatal("inbound directory chmod was echoed back")
				}
				return
			}
			op := nextDiagnosticUpload(t, d)
			if op.Kind != opUploadChmod || op.Mode != 0o700 || op.StoredEntry.Mode != 0o750 {
				t.Fatalf("application chmod was hidden by inbound echo: %+v", op)
			}
			d.uploader.process(ctx, op)
			result := <-d.reconciler.uploadResCh
			if result.Err != nil || result.Conflict {
				t.Fatalf("application chmod = %+v", result)
			}
		})
	}
}

func TestSyncDirectoryNotificationKeepsPendingLocalChmod(t *testing.T) {
	env, d, abs := directoryChmodDiagnostic(t)
	ctx := context.Background()
	if err := os.Chmod(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	// A parent notification caused by another child's write retains mode 0755.
	env.writeRemoteFile(t, "directory/peer", "peer contents")
	d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/directory"})
	d.downloader.process(ctx, <-d.reconciler.downloadCh)
	res := <-d.reconciler.downloadResCh
	if res.Err != nil || !res.Skipped {
		t.Fatalf("unchanged remote directory should defer to pending local chmod: %+v", res)
	}
	info, err := os.Stat(abs)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("local chmod overwritten: %v, %v", info, err)
	}
	d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
	d.uploader.process(ctx, nextDiagnosticUpload(t, d))
	resUpload := <-d.reconciler.uploadResCh
	if resUpload.Err != nil || resUpload.Conflict {
		t.Fatalf("local chmod after child publication = %+v", resUpload)
	}
}

type directoryChmodRaceClient struct {
	client.Client
	before func()
}

func (c *directoryChmodRaceClient) Chmod(ctx context.Context, path string, mode uint32) error {
	c.before()
	return c.Client.Chmod(ctx, path, mode)
}

func TestSyncDirectoryChmodPreservesPeerPublication(t *testing.T) {
	for _, timing := range []string{"before_observation", "before_commit"} {
		for _, replacement := range []string{"mode", "file"} {
			t.Run(timing+"/"+replacement, func(t *testing.T) {
				env, d, abs := directoryChmodDiagnostic(t)
				ctx := context.Background()
				if err := os.Chmod(abs, 0o700); err != nil {
					t.Fatal(err)
				}
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
				op := nextDiagnosticUpload(t, d)
				publish := func() {
					if replacement == "file" {
						if err := env.fsClient.Rm(ctx, "/directory"); err != nil {
							t.Fatal(err)
						}
						env.writeRemoteFile(t, "directory", "replacement bytes")
					}
					if err := env.fsClient.Chmod(ctx, "/directory", 0o750); err != nil {
						t.Fatal(err)
					}
				}
				if timing == "before_observation" {
					publish()
				} else {
					d.uploader.fs = &directoryChmodRaceClient{Client: env.fsClient, before: publish}
				}
				d.uploader.process(ctx, op)
				res := <-d.reconciler.uploadResCh
				if res.Err != nil || !res.Conflict {
					t.Fatalf("concurrent directory chmod = %+v", res)
				}
				stat, err := env.fsClient.Stat(ctx, "/directory")
				if err != nil || stat == nil || stat.Mode != 0o750 {
					t.Fatalf("peer mode overwritten: %+v, %v", stat, err)
				}
				if replacement == "file" && env.readRemoteFile(t, "directory") != "replacement bytes" {
					t.Fatal("peer replacement bytes lost")
				}
				d.reconciler.handleUploadResult(ctx, res)
				select {
				case download := <-d.reconciler.downloadCh:
					d.downloader.process(ctx, download)
					result := <-d.reconciler.downloadResCh
					if result.Err != nil || result.Skipped {
						t.Fatalf("peer mode conflict download = %+v", result)
					}
					d.reconciler.handleDownloadResult(ctx, result)
				default:
					t.Fatal("peer conflict did not schedule inbound recovery")
				}
				local, err := os.Stat(abs)
				if err != nil || local.Mode().Perm() != 0o750 {
					t.Fatalf("peer-winning permissions did not converge: %v, %v", local, err)
				}
			})
		}
	}
}

func TestSyncTemporaryDirectoryModesNeverPublish(t *testing.T) {
	for _, applicationChange := range []string{"none", "chmod", "replacement"} {
		t.Run(applicationChange, func(t *testing.T) {
			env, d, abs := directoryChmodDiagnostic(t)
			ctx := context.Background()
			if err := env.fsClient.Chmod(ctx, "/directory", 0o500); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleRemoteEvent(ctx, remoteEvent{Path: "/directory"})
			d.downloader.process(ctx, <-d.reconciler.downloadCh)
			res := <-d.reconciler.downloadResCh
			if res.Err != nil || res.Skipped {
				t.Fatalf("initial directory download = %+v", res)
			}
			d.reconciler.handleDownloadResult(ctx, res)
			modes := newSyncDirectoryModes(d.reconciler)
			defer modes.restore()
			if err := modes.prepare(abs); err != nil {
				t.Fatal(err)
			}
			// Child writes can generate repeated parent events after the chmod
			// echo itself. All observations of the temporary 0700 are internal.
			for _, kind := range []string{"chmod", "write", "write", "chmod"} {
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: kind})
			}
			if len(d.reconciler.uploadCh) != 0 {
				t.Fatal("temporary recovery permissions queued a remote chmod")
			}
			want := os.FileMode(0o500)
			switch applicationChange {
			case "chmod":
				want = 0o750
				if err := os.Chmod(abs, want); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				want = 0o700
				if err := os.Rename(abs, abs+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(abs, want); err != nil {
					t.Fatal(err)
				}
			}
			if applicationChange != "none" {
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
				op := nextDiagnosticUpload(t, d)
				if op.Kind != opUploadChmod || op.Mode != uint32(want) {
					t.Fatalf("application directory change suppressed: %+v", op)
				}
				d.uploader.process(ctx, op)
				result := <-d.reconciler.uploadResCh
				if result.Err != nil || result.Conflict || result.Skipped {
					t.Fatalf("application chmod = %+v", result)
				}
				d.reconciler.handleUploadResult(ctx, result)
			}
			if err := modes.restore(); err != nil {
				t.Fatal(err)
			}
			local, err := os.Stat(abs)
			if err != nil || local.Mode().Perm() != want {
				t.Fatalf("restored local mode = %v, %v; want %o", local, err, want)
			}
			remote, err := env.fsClient.Stat(ctx, "/directory")
			if err != nil || remote.Mode != uint32(want) {
				t.Fatalf("temporary mode leaked remotely: %+v, %v; want %o", remote, err, want)
			}
			if applicationChange == "none" {
				// The lifetime guard must end with restore so a later identical
				// application chmod to 0700 is a real publication.
				if err := os.Chmod(abs, 0o700); err != nil {
					t.Fatal(err)
				}
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
				if op := nextDiagnosticUpload(t, d); op.Mode != 0o700 {
					t.Fatalf("restored directory mode guard retained: %+v", op)
				}
			}
		})
	}
}

type directoryChmodStatClient struct {
	client.Client
	afterStat func()
}

func (c *directoryChmodStatClient) Stat(ctx context.Context, path string) (*client.StatResult, error) {
	stat, err := c.Client.Stat(ctx, path)
	c.afterStat()
	return stat, err
}

func TestSyncQueuedChmodNeverPublishesTemporaryMode(t *testing.T) {
	for _, timing := range []string{"before_upload", "during_remote_stat"} {
		t.Run(timing, func(t *testing.T) {
			env, d, abs := directoryChmodDiagnostic(t)
			ctx := context.Background()
			if err := os.Chmod(abs, 0o500); err != nil {
				t.Fatal(err)
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(abs, 0o700); err != nil {
				t.Fatal(err)
			}
			d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory", KindHint: "chmod"})
			op := nextDiagnosticUpload(t, d)
			modes := newSyncDirectoryModes(d.reconciler)
			defer modes.restore()
			prepare := func() {
				// The application reverts its queued chmod before recovery
				// independently enables the same owner-write permission.
				if err := os.Chmod(abs, 0o500); err != nil {
					t.Fatal(err)
				}
				if err := modes.prepare(abs); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before_upload" {
				prepare()
			} else {
				d.uploader.fs = &directoryChmodStatClient{Client: env.fsClient, afterStat: prepare}
			}
			d.uploader.process(ctx, op)
			res := <-d.reconciler.uploadResCh
			if res.Err != nil || res.Conflict || !res.Skipped {
				t.Fatalf("queued chmod of temporary mode = %+v", res)
			}
			remote, err := env.fsClient.Stat(ctx, "/directory")
			if err != nil || remote.Mode != 0o500 {
				t.Fatalf("temporary mode published: %+v, %v", remote, err)
			}
		})
	}
}

func TestSyncTemporaryDirectoryModeRestorationRetriesBeforeScan(t *testing.T) {
	for _, applicationChmod := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore_original", true: "preserve_application_chmod"}[applicationChmod], func(t *testing.T) {
			env, d, parent := directoryChmodDiagnostic(t)
			ctx := context.Background()
			child := filepath.Join(parent, "child")
			if err := os.Mkdir(child, 0o500); err != nil {
				t.Fatal(err)
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			modes := newSyncDirectoryModes(d.reconciler)
			if err := modes.prepare(child); err != nil {
				t.Fatal(err)
			}
			// Make the ancestor temporarily unresolvable even when tests run
			// as root. The original directory remains available to restore.
			parked := filepath.Join(t.TempDir(), "parent")
			if err := os.Rename(parent, parked); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("directory", parent); err != nil {
				t.Fatal(err)
			}
			if err := modes.restore(); err == nil {
				t.Fatal("restoring an unresolvable child unexpectedly succeeded")
			}
			if err := d.full.warmStart(ctx, nil); err == nil {
				t.Fatal("warm recovery scanned with an unresolved restoration")
			}
			if err := os.Remove(parent); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(parked, parent); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(child)
			if err != nil || !d.reconciler.echo.matchesTemporaryDirectory("directory/child", info) {
				t.Fatalf("failed restoration lost its lifetime guard: %v, %v", info, err)
			}
			for _, kind := range []string{"chmod", "write", "chmod"} {
				d.reconciler.handleLocalEvent(ctx, LocalEvent{Path: "directory/child", KindHint: kind})
			}
			if len(d.reconciler.uploadCh) != 0 {
				t.Fatal("failed restoration queued temporary permissions")
			}
			want := uint32(0o500)
			if applicationChmod {
				want = 0o750
				if err := os.Chmod(child, os.FileMode(want)); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			local, err := os.Lstat(child)
			if err != nil || uint32(local.Mode().Perm()) != want {
				t.Fatalf("retry final mode = %v, %v; want %o", local, err, want)
			}
			remote, err := env.fsClient.Stat(ctx, "/directory/child")
			if err != nil || remote.Mode != want {
				t.Fatalf("retry published temporary mode: %+v, %v; want %o", remote, err, want)
			}
			d.reconciler.echo.mu.Lock()
			remaining := len(d.reconciler.echo.temporaryDirs)
			d.reconciler.echo.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("retry retained %d stale mode markers", remaining)
			}
		})
	}
}

func TestSyncTemporaryDirectoryModeRetiresReplacedAncestor(t *testing.T) {
	_, d, parent := directoryChmodDiagnostic(t)
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o500); err != nil {
		t.Fatal(err)
	}
	modes := newSyncDirectoryModes(d.reconciler)
	if err := modes.prepare(child); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, filepath.Join(t.TempDir(), "parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("application replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := modes.restore(); err != nil {
		t.Fatalf("ancestor replacement should retire the missing child: %v", err)
	}
	if err := d.reconciler.echo.retryDirectoryModes(d.reconciler.root); err != nil {
		t.Fatalf("ancestor replacement blocked the recovery retry: %v", err)
	}
	d.reconciler.echo.mu.Lock()
	remaining := len(d.reconciler.echo.temporaryDirs)
	d.reconciler.echo.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("replacement retained %d stale mode markers", remaining)
	}
}
