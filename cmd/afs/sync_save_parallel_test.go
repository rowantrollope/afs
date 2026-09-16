package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rowantrollope/afs/mount/client"
)

type gatedSaveReadClient struct {
	client.Client
	entered chan struct{}
	release <-chan struct{}
	active  atomic.Int32
	fail    bool
}

func (c *gatedSaveReadClient) Cat(ctx context.Context, path string) ([]byte, error) {
	c.active.Add(1)
	defer c.active.Add(-1)
	c.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
	}
	if c.fail {
		return nil, errors.New("injected save read failure")
	}
	return c.Client.Cat(ctx, path)
}

func TestSyncSaveRemoteReadersAreBoundedAndJoined(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail_%v", fail), func(t *testing.T) {
			env := newSyncTestEnv(t)
			for i := 0; i < defaultParallelWorkers+1; i++ {
				env.writeRemoteFile(t, fmt.Sprintf("file-%d", i), "verified bytes")
			}
			r := newSyncSaveTestReconciler(t, env)
			release := make(chan struct{})
			c := &gatedSaveReadClient{Client: env.fsClient, entered: make(chan struct{}, defaultParallelWorkers+1), release: release, fail: fail}
			r.fs = c
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				tree, err := scanSyncSaveRemote(ctx, r, r.state.snapshot())
				if err == nil && len(tree) != defaultParallelWorkers+1 {
					err = fmt.Errorf("incomplete tree: %d", len(tree))
				}
				if err == nil {
					for path, entry := range tree {
						if entry.Hash != sha256Hex([]byte("verified bytes")) {
							err = fmt.Errorf("unverified entry %s", path)
						}
					}
				}
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				if c.active.Load() != 0 {
					t.Error("save readers still active")
				}
			})
			for i := 0; i < defaultParallelWorkers; i++ {
				select {
				case <-c.entered:
				case <-time.After(time.Second):
					cancel()
					<-done
					t.Fatal("small-file verification serialized network reads")
				}
			}
			if got := c.active.Load(); got != defaultParallelWorkers {
				t.Fatalf("active reads = %d", got)
			}
			close(release)
			err := <-done
			if fail && err == nil {
				t.Fatal("read failure produced successful scan")
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			if c.active.Load() != 0 {
				t.Fatal("scan returned before readers joined")
			}
		})
	}
}
