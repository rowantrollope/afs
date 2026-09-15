package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// A peer publication is deliberately interleaved after WATCH but before every
// anchored mutation's EXEC. The peer updates an existing file in another
// directory, so it changes the workspace journal/counters without changing the
// held parent or the target inode. Increasing the retry count cannot make this
// schedule succeed when unrelated workspace-wide keys are incorrectly watched.
type unrelatedPublicationHook struct {
	run func() error
}

func (h *unrelatedPublicationHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *unrelatedPublicationHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}
func (h *unrelatedPublicationHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		parents, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
		if len(parents) > 0 {
			for _, cmd := range cmds {
				if cmd.Name() == "eval" {
					if err := h.run(); err != nil {
						return err
					}
					break
				}
			}
		}
		return next(ctx, cmds)
	}
}

func TestNativeExpectedParentAllowsUnrelatedPublication(t *testing.T) {
	operations := map[string]func(context.Context, NativeClient) error{
		"create": func(ctx context.Context, c NativeClient) error {
			_, _, err := c.CreateFile(ctx, "/held/created", 0644, false)
			return err
		},
		"write": func(ctx context.Context, c NativeClient) error {
			return c.Echo(ctx, "/held/file", []byte("updated"))
		},
		"metadata": func(ctx context.Context, c NativeClient) error {
			return c.Chmod(ctx, "/held/file", 0600)
		},
		"remove": func(ctx context.Context, c NativeClient) error {
			return c.Rm(ctx, "/held/file")
		},
	}
	for name, op := range operations {
		t.Run(name, func(t *testing.T) {
			rdb, ctx := setupTestRedis(t)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			peer := setupNativeSession(t, rdb, ctx, "unrelated-publication")
			for _, path := range []string{"/held/file", "/other/file"} {
				if err := peer.Echo(ctx, path, []byte("original")); err != nil {
					t.Fatal(err)
				}
			}
			parent, err := peer.Stat(ctx, "/held")
			if err != nil {
				t.Fatal(err)
			}
			wr := redis.NewClient(rdb.Options())
			t.Cleanup(func() { _ = wr.Close() })
			writer := setupNativeSession(t, wr, ctx, "unrelated-publication")
			publications := 0
			wr.AddHook(&unrelatedPublicationHook{run: func() error {
				publications++
				return peer.Echo(ctx, "/other/file", []byte(fmt.Sprintf("peer update %d", publications)))
			}})
			if err := op(WithExpectedParent(ctx, "/held", parent.Inode), writer); err != nil {
				t.Fatalf("unrelated peer publications rejected held-parent %s after %d interleavings: %v", name, publications, err)
			}
			if publications == 0 {
				t.Fatal("test did not interleave a peer publication at EXEC")
			}
			peer.InvalidateCache()
			if got, err := peer.Cat(ctx, "/other/file"); err != nil || string(got) != fmt.Sprintf("peer update %d", publications) {
				t.Fatalf("peer publication lost: %q %v", got, err)
			}
			switch name {
			case "create":
				if st, err := peer.Stat(ctx, "/held/created"); err != nil || st == nil || st.Mode != 0644 || st.Size != 0 {
					t.Fatalf("created file: %+v %v", st, err)
				}
			case "write":
				if got, err := peer.Cat(ctx, "/held/file"); err != nil || string(got) != "updated" {
					t.Fatalf("held file update: %q %v", got, err)
				}
			case "metadata":
				if st, err := peer.Stat(ctx, "/held/file"); err != nil || st == nil || st.Mode != 0600 {
					t.Fatalf("held file mode: %+v %v", st, err)
				}
			case "remove":
				if st, err := peer.Stat(ctx, "/held/file"); st != nil || (err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, redis.Nil)) {
					t.Fatalf("removed file: %+v %v", st, err)
				}
			}
		})
	}
}
