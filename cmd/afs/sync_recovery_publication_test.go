package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

type transientUploadClient struct {
	client.Client
	failed atomic.Bool
}

func (c *transientUploadClient) Echo(ctx context.Context, path string, data []byte) error {
	if path == "/offline-only.txt" && c.failed.CompareAndSwap(false, true) {
		return io.ErrUnexpectedEOF
	}
	return c.Client.Echo(ctx, path, data)
}

func TestSyncTransientUploadFailureRecoversWithoutAnotherFileEvent(t *testing.T) {
	env := newSyncTestEnv(t)
	flaky := &transientUploadClient{Client: client.New(env.rdb, env.mountKey)}
	env.startDaemon(t, func(cfg *syncDaemonConfig) { cfg.FS = flaky })
	env.writeLocalFile(t, "offline-only.txt", "survive one failed connection")
	assertEventually(t, 3*time.Second, "first upload reaches the injected connection failure", flaky.failed.Load)
	assertEventually(t, 3*time.Second, "failed upload retries without another local or remote event", func() bool {
		data, err := env.fsClient.Cat(context.Background(), "/offline-only.txt")
		return err == nil && string(data) == "survive one failed connection"
	})
}

func TestSyncWarmStartPublishesConflictCopiesCreatedBeforeWatcher(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			env, d := recoveryBaselineDiagnostic(t)
			ctx := context.Background()
			const rel = "work"
			const local, remote = "unpublished-local-candidate", "published-remote-candidate"
			abs := filepath.Join(env.localRoot, rel)
			if symlink {
				if err := env.fsClient.Ln(ctx, "initial", "/"+rel); err != nil {
					t.Fatal(err)
				}
			} else {
				env.writeRemoteFile(t, rel, "initial")
			}
			if err := d.full.warmStart(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Remove(abs); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(local, abs); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Rm(ctx, "/"+rel); err != nil {
					t.Fatal(err)
				}
				if err := env.fsClient.Ln(ctx, remote, "/"+rel); err != nil {
					t.Fatal(err)
				}
			} else {
				env.writeLocalFile(t, rel, local)
				env.writeRemoteFile(t, rel, remote)
			}
			// Start creates the conflict copy during warm reconciliation, before
			// installing fsnotify. No later application edit can wake the watcher.
			if err := d.Start(ctx); err != nil {
				t.Fatal(err)
			}
			env.daemon = d
			copies, err := filepath.Glob(abs + ".conflict-*")
			if err != nil || len(copies) != 1 {
				t.Fatalf("warm conflict copies = %v, %v", copies, err)
			}
			assertEventually(t, 3*time.Second, "warm-created conflict candidate is published automatically", func() bool {
				names, err := env.fsClient.Ls(ctx, "/")
				if err != nil {
					return false
				}
				for _, name := range names {
					if !strings.Contains(name, ".conflict-") {
						continue
					}
					if symlink {
						target, err := env.fsClient.Readlink(ctx, "/"+name)
						if err == nil && target == local {
							return true
						}
					} else {
						data, err := env.fsClient.Cat(ctx, "/"+name)
						if err == nil && string(data) == local {
							return true
						}
					}
				}
				return false
			})
		})
	}
}
