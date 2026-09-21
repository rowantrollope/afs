package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
)

// APIKey is public metadata. The token is returned only when a key is created.
// Every key grants the same trusted-administrator access as the team token.
type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	Status     string `json:"status"`
}

type APIKeyList struct {
	Keys       []APIKey `json:"keys"`
	Enabled    bool     `json:"enabled"`
	NextCursor string   `json:"next_cursor,omitempty"`
}
type APIKeyCreated struct {
	Key   APIKey `json:"key"`
	Token string `json:"token"`
}

type APIKeyCreateInput struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// A shared hash tag keeps the index and records together on Redis Cluster.
const apiKeysIndex = "afs:{management-auth}:keys"

func apiKeyRecord(id string) string { return "afs:{management-auth}:key:" + id }

func validAPIKeyID(id string) bool {
	if !strings.HasPrefix(id, "key_") || len(id) != 36 {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil && strings.ToLower(id) == id
}
func apiKeyHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
func validateAPIKeyInput(input APIKeyCreateInput, now time.Time) (APIKey, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return APIKey{}, errors.New("provide an API key name up to 128 characters without control characters")
	}
	expires := now.Add(30 * 24 * time.Hour)
	if input.ExpiresAt != nil {
		if *input.ExpiresAt == "" {
			expires = time.Time{}
		} else {
			var err error
			expires, err = time.Parse(time.RFC3339Nano, *input.ExpiresAt)
			if err != nil || !expires.After(now) {
				return APIKey{}, errors.New("expires_at must be a future RFC3339 timestamp, or empty for no expiry")
			}
		}
	}
	key := APIKey{Name: name, CreatedAt: serverTime(now), Status: "active"}
	if !expires.IsZero() {
		key.ExpiresAt = serverTime(expires)
	}
	return key, nil
}
func apiKeyFromFields(fields []interface{}, now time.Time) (APIKey, error) {
	if len(fields) != 6 || fields[0] == nil {
		return APIKey{}, os.ErrNotExist
	}
	values := make([]string, 6)
	for i, field := range fields {
		if field != nil {
			values[i] = fmt.Sprint(field)
		}
	}
	key := APIKey{ID: values[0], Name: values[1], CreatedAt: values[2], LastUsedAt: values[3], ExpiresAt: values[4], RevokedAt: values[5], Status: "active"}
	if !validAPIKeyID(key.ID) || key.Name == "" || key.CreatedAt == "" {
		return APIKey{}, errors.New("invalid API key metadata")
	}
	if key.RevokedAt != "" {
		key.Status = "revoked"
	} else if key.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339Nano, key.ExpiresAt)
		if err != nil {
			return APIKey{}, errors.New("invalid API key expiry")
		}
		if !expires.After(now) {
			key.Status = "expired"
		}
	}
	return key, nil
}

var apiKeyFields = []string{"id", "name", "created_at", "last_used_at", "expires_at", "revoked_at"}

func (s *Store) createAPIKey(ctx context.Context, key APIKey) (APIKey, string, error) {
	var id [16]byte
	var secret [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return APIKey{}, "", err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return APIKey{}, "", err
	}
	key.ID = "key_" + hex.EncodeToString(id[:])
	token := "afs_" + key.ID + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	expiresMillis := int64(0)
	if key.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339Nano, key.ExpiresAt)
		if err != nil {
			return APIKey{}, "", err
		}
		expiresMillis = expires.UnixMilli()
	}
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, apiKeyRecord(key.ID), "id", key.ID, "name", key.Name, "created_at", key.CreatedAt, "last_used_at", "", "expires_at", key.ExpiresAt, "expires_ms", expiresMillis, "revoked_at", "", "hash", apiKeyHash(token))
		p.ZAdd(ctx, apiKeysIndex, redis.Z{Score: 0, Member: key.ID})
		return nil
	})
	if err != nil {
		return APIKey{}, "", err
	}
	return key, token, nil
}

// Validation and the last-used touch are one Redis operation. In particular a
// concurrent revoke can never be overwritten by a stale authentication write.
var authenticateAPIKeyScript = redis.NewScript(`
local hash = redis.call('HGET', KEYS[1], 'hash')
if not hash or hash ~= ARGV[1] then return {} end
local revoked = redis.call('HGET', KEYS[1], 'revoked_at')
local expires = tonumber(redis.call('HGET', KEYS[1], 'expires_ms'))
if not revoked or revoked ~= '' or not expires or (expires ~= 0 and expires <= tonumber(ARGV[2])) then return {} end
redis.call('HSET', KEYS[1], 'last_used_at', ARGV[3])
return redis.call('HMGET', KEYS[1], 'id', 'name', 'created_at', 'last_used_at', 'expires_at', 'revoked_at')
`)

func (s *Store) authenticateAPIKey(ctx context.Context, token string, now time.Time) (APIKey, bool, error) {
	prefix, secret, ok := strings.Cut(token, ".")
	id := strings.TrimPrefix(prefix, "afs_")
	if !ok || !strings.HasPrefix(prefix, "afs_") || !validAPIKeyID(id) || len(secret) != 43 {
		return APIKey{}, false, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		return APIKey{}, false, nil
	}
	fields, err := authenticateAPIKeyScript.Run(ctx, s.rdb, []string{apiKeyRecord(id)}, apiKeyHash(token), now.UnixMilli(), serverTime(now)).Slice()
	if err != nil {
		return APIKey{}, false, err
	}
	if len(fields) == 0 {
		return APIKey{}, false, nil
	}
	key, err := apiKeyFromFields(fields, now)
	if err != nil {
		return APIKey{}, false, err
	}
	return key, key.ID == id && key.Status == "active", nil
}

var revokeAPIKeyScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {} end
if redis.call('HGET', KEYS[1], 'revoked_at') == '' then
 redis.call('HSET', KEYS[1], 'revoked_at', ARGV[1])
end
return redis.call('HMGET', KEYS[1], 'id', 'name', 'created_at', 'last_used_at', 'expires_at', 'revoked_at')
`)

func (s *Store) revokeAPIKey(ctx context.Context, id string, now time.Time) (APIKey, error) {
	fields, err := revokeAPIKeyScript.Run(ctx, s.rdb, []string{apiKeyRecord(id)}, serverTime(now)).Slice()
	if err != nil {
		return APIKey{}, err
	}
	if len(fields) == 0 {
		return APIKey{}, os.ErrNotExist
	}
	return apiKeyFromFields(fields, now)
}
func (s *Store) listAPIKeys(ctx context.Context, cursor string, limit int, now time.Time) ([]APIKey, string, error) {
	min := "-"
	if cursor != "" {
		min = "(" + cursor
	}
	ids, err := s.rdb.ZRangeByLex(ctx, apiKeysIndex, &redis.ZRangeBy{Min: min, Max: "+", Count: int64(limit + 1)}).Result()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	pipe := s.rdb.Pipeline()
	commands := make([]*redis.SliceCmd, len(ids))
	for i, id := range ids {
		commands[i] = pipe.HMGet(ctx, apiKeyRecord(id), apiKeyFields...)
	}
	if len(ids) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, "", err
		}
	}
	keys := make([]APIKey, 0, len(ids))
	for _, command := range commands {
		key, err := apiKeyFromFields(command.Val(), now)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		keys = append(keys, key)
	}
	return keys, next, nil
}
