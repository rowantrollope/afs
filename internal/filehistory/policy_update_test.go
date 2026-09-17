package filehistory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestUpdatePolicyPreservesConcurrentOmittedFields(t *testing.T) {
	rdb := testHistory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll, Include: []string{"docs/**"}}); err != nil {
		t.Fatal(err)
	}
	initialRead, release := make(chan struct{}), make(chan struct{})
	type result struct {
		policy   Policy
		err      error
		attempts int
	}
	done := make(chan result, 1)
	go func() {
		attempts := 0
		policy, err := UpdatePolicy(ctx, rdb, "test", func(p Policy) (Policy, error) {
			attempts++
			if attempts == 1 {
				close(initialRead)
				select {
				case <-release:
				case <-ctx.Done():
					return p, ctx.Err()
				}
			}
			p.MaxVersions = 23
			return p, nil
		})
		done <- result{policy, err, attempts}
	}()
	select {
	case <-initialRead:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err := UpdatePolicy(ctx, rdb, "test", func(p Policy) (Policy, error) {
		p.MaxAgeDays = 7
		return p, nil
	})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, Prefix("test")+"prune_cursor", "99", 0).Err(); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	got := <-done
	if got.err != nil || got.attempts != 2 || got.policy.MaxVersions != 23 || got.policy.MaxAgeDays != 7 || !reflect.DeepEqual(got.policy.Include, []string{"docs/**"}) {
		t.Fatalf("concurrent update: %+v", got)
	}
	saved, err := GetPolicy(ctx, rdb, "test")
	if err != nil || !reflect.DeepEqual(saved, got.policy) {
		t.Fatalf("returned %+v differs from saved %+v: %v", got.policy, saved, err)
	}
	if err := rdb.Get(ctx, Prefix("test")+"prune_cursor").Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("prune cursor not reset: %v", err)
	}
}

func TestUpdatePolicyFailurePreservesPolicy(t *testing.T) {
	rdb := testHistory(t)
	ctx := context.Background()
	original := Policy{Mode: ModeAll, MaxVersions: 5}
	if err := SetPolicy(ctx, rdb, "test", original); err != nil {
		t.Fatal(err)
	}
	for _, update := range []func(Policy) (Policy, error){
		nil,
		func(p Policy) (Policy, error) { return p, errors.New("cancelled") },
		func(p Policy) (Policy, error) { p.Mode = ModePaths; return p, nil },
		func(p Policy) (Policy, error) { p.MaxVersions = -1; return p, nil },
	} {
		if _, err := UpdatePolicy(ctx, rdb, "test", update); err == nil {
			t.Fatal("invalid update accepted")
		}
		got, err := GetPolicy(ctx, rdb, "test")
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("failed update changed policy %+v: %v", got, err)
		}
	}
}

func TestPolicyUpdatesRespectWorkspaceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, generation string
		active, allowed  bool
	}{
		{"unmanaged", "", false, true},
		{"live", "g_live", false, true},
		{"custom", "generation-1", false, true},
		{"deleted", "deleted", false, false},
		{"deleted-active", "deleted", true, false},
		{"fencing-active", "fencing:g_restore", true, false},
		{"restoring-active", "restoring:g_restore", true, false},
		{"failed-fencing", "fencing:g_restore", false, true},
		{"failed-restoring", "restoring:g_restore", false, true},
	} {
		for _, method := range []string{"set", "update"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				rdb := testHistory(t)
				ctx := context.Background()
				if tc.generation != "" {
					if err := rdb.Set(ctx, policyGenerationKey("test"), tc.generation, 0).Err(); err != nil {
						t.Fatal(err)
					}
				}
				if tc.active {
					if err := rdb.Set(ctx, policyImportLockKey("test"), "owner", time.Minute).Err(); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if method == "set" {
					err = SetPolicy(ctx, rdb, "test", Policy{Mode: ModeAll})
				} else {
					_, err = UpdatePolicy(ctx, rdb, "test", func(p Policy) (Policy, error) { p.Mode = ModeAll; return p, nil })
				}
				if (err == nil) != tc.allowed {
					t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
				}
				policy, err := GetPolicy(ctx, rdb, "test")
				if err != nil {
					t.Fatal(err)
				}
				want := ModeOff
				if tc.allowed {
					want = ModeAll
				}
				if policy.Mode != want {
					t.Fatalf("policy=%+v, want mode %s", policy, want)
				}
				if !tc.allowed {
					if n, err := rdb.Exists(ctx, Prefix("test")+"policy").Result(); err != nil || n != 0 {
						t.Fatalf("blocked update created policy: %d %v", n, err)
					}
				}
			})
		}
	}
}

func TestUpdatePolicyFencesLifecycleTransitionBeforeCommit(t *testing.T) {
	for _, generation := range []string{"deleted", "fencing:g_new", "restoring:g_new"} {
		t.Run(generation, func(t *testing.T) {
			rdb := testHistory(t)
			ctx := context.Background()
			if err := rdb.Set(ctx, policyGenerationKey("test"), "g_original", 0).Err(); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			_, err := UpdatePolicy(ctx, rdb, "test", func(p Policy) (Policy, error) {
				attempts++
				_, err := rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Set(ctx, policyGenerationKey("test"), generation, 0)
					if generation != "deleted" {
						pipe.Set(ctx, policyImportLockKey("test"), "restore-owner", time.Minute)
					}
					return nil
				})
				p.Mode = ModeAll
				return p, err
			})
			if err == nil || attempts != 1 {
				t.Fatalf("stale enable accepted or callback repeated: attempts=%d error=%v", attempts, err)
			}
			if n, err := rdb.Exists(ctx, Prefix("test")+"policy").Result(); err != nil || n != 0 {
				t.Fatalf("stale update created policy: %d %v", n, err)
			}
		})
	}
}
