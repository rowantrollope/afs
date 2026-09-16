package client

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

type expectedParentsKey struct{}
type expectedParents map[string]uint64

// WithExpectedParent binds a direct child operation to the directory represented
// by a native handle. Calls compose for rename's source/destination directories.
// Conflicting identities at the same path fail closed instead of overwriting a
// previously supplied handle constraint.
func WithExpectedParent(ctx context.Context, parentPath string, inode uint64) context.Context {
	old, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
	next := make(expectedParents, len(old)+1)
	for p, id := range old {
		next[p] = id
	}
	p := normalizePath(parentPath)
	if prior, exists := next[p]; exists && prior != inode {
		inode = 0
	}
	next[p] = inode
	return context.WithValue(ctx, expectedParentsKey{}, next)
}

func checkExpectedParent(ctx context.Context, childPath, parentID string) error {
	parents, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
	if len(parents) == 0 {
		return nil
	}
	expected, found := parents[parentOf(normalizePath(childPath))]
	if !found || expected == 0 || strconv.FormatUint(expected, 10) != parentID {
		return ErrWriteConflict
	}
	return nil
}

// WATCH only the dirent chain resolving a bound direct parent. A concurrent
// rename/replacement changes one of those hashes and aborts EXEC; no persistent
// ancestor locks or additional storage representation is needed.
func (c *nativeClient) watchExpectedParents(ctx context.Context, tx *redis.Tx) error {
	parents, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
	for path, expected := range parents {
		if expected == 0 {
			return ErrWriteConflict
		}
		id := rootInodeID
		for _, name := range splitComponents(path) {
			key := c.keys.dirents(id)
			if err := tx.Watch(ctx, key).Err(); err != nil {
				return err
			}
			next, err := tx.HGet(ctx, key, name).Result()
			if errors.Is(err, redis.Nil) {
				return ErrWriteConflict
			}
			if err != nil {
				return err
			}
			id = next
		}
		if id != strconv.FormatUint(expected, 10) {
			return ErrWriteConflict
		}
	}
	return nil
}

func (c *nativeClient) runMutationScript(ctx context.Context, script *redis.Script, alwaysEval bool, keys []string, args ...interface{}) (int, error) {
	parents, _ := ctx.Value(expectedParentsKey{}).(expectedParents)
	if len(parents) == 0 {
		if alwaysEval {
			return script.Eval(ctx, c.rdb, keys, args...).Int()
		}
		return script.Run(ctx, c.rdb, keys, args...).Int()
	}
	var result *redis.Cmd
	// The script validates its inode revision, dirent, generation and session
	// atomically. WATCH is only needed for parent identities read outside Lua;
	// retryWatch adds that chain and the lifetime guards. Watching every script
	// key would also watch global counters and the journal, turning unrelated
	// peer publications into transaction conflicts.
	err := c.retryWatch(ctx, nil, func(tx *redis.Tx) error {
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			// EVAL is deliberate: an uncached EVALSHA cannot fall back while
			// already queued inside an EXEC guarded by directory identities.
			result = script.Eval(ctx, pipe, keys, args...)
			return nil
		})
		return err
	})
	if err != nil {
		return 0, err
	}
	return result.Int()
}
