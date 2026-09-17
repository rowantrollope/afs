package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

func policyGenerationKey(id string) string { return "afs-lite:{" + id + "}:generation" }
func policyImportLockKey(id string) string { return "afs-lite:{" + id + "}:import_lock" }

// A failed restore remains fenced but releases its lock. Administrators may
// then adjust its history budget before retrying. An active restore must see a
// consistent policy, and a deleted workspace must never regain history keys.
func checkPolicyLifecycle(ctx context.Context, cmd redis.Cmdable, id string) error {
	generation, err := cmd.Get(ctx, policyGenerationKey(id)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if generation == "deleted" {
		return errors.New("workspace was deleted")
	}
	if strings.HasPrefix(generation, "fencing:") || strings.HasPrefix(generation, "restoring:") {
		active, err := cmd.Exists(ctx, policyImportLockKey(id)).Result()
		if err != nil {
			return err
		}
		if active != 0 {
			return errors.New("workspace restore is active; retry versioning after it completes")
		}
	}
	return nil
}

var setPolicyScript = redis.NewScript(`
local generation = redis.call('GET', KEYS[3])
if generation == 'deleted' then return redis.error_reply('workspace was deleted') end
if generation and (string.match(generation, '^fencing:') or string.match(generation, '^restoring:'))
  and redis.call('EXISTS', KEYS[4]) ~= 0 then
  return redis.error_reply('workspace restore is active; retry versioning after it completes')
end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('DEL', KEYS[2])
return 1
`)

// UpdatePolicy applies a partial change to the latest committed policy and
// returns the exact policy it committed. The callback can run more than once
// when another administrator updates concurrently; it must have no side effects.
func UpdatePolicy(ctx context.Context, rdb *redis.Client, id string, update func(Policy) (Policy, error)) (Policy, error) {
	var committed Policy
	if update == nil {
		return committed, errors.New("history policy update is required")
	}
	key := Prefix(id) + "policy"
	for attempt := 0; attempt < 8; attempt++ {
		err := rdb.Watch(ctx, func(tx *redis.Tx) error {
			if err := checkPolicyLifecycle(ctx, tx, id); err != nil {
				return err
			}
			current := Policy{Mode: ModeOff}
			data, err := tx.Get(ctx, key).Bytes()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			if err == nil {
				if err := json.Unmarshal(data, &current); err != nil {
					return fmt.Errorf("decode history policy: %w", err)
				}
			}
			if current.Mode == "" {
				current.Mode = ModeOff
			}
			if err := current.Validate(); err != nil {
				return err
			}
			next, err := update(current)
			if err != nil {
				return err
			}
			next = next.Normalize()
			if next.Mode == "" {
				next.Mode = ModeOff
			}
			if err := next.Validate(); err != nil {
				return err
			}
			data, err = json.Marshal(next)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, data, 0)
				pipe.Del(ctx, Prefix(id)+"prune_cursor")
				return nil
			})
			if err == nil {
				committed = next
			}
			return err
		}, key, policyGenerationKey(id), policyImportLockKey(id))
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return committed, err
	}
	return committed, fmt.Errorf("history policy changed concurrently; retry: %w", redis.TxFailedErr)
}
