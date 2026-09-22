package controlplane

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// MigrateLegacyAPIKeys reads the explicitly selected legacy authentication
// Redis. It preserves bearer tokens and never deletes or updates source records.
// Stop the old control plane before migration so revocations cannot race the copy.
func MigrateLegacyAPIKeys(ctx context.Context, metadata *MetadataStore, rdb *redis.Client) error {
	records := []LegacyAPIKeyRecord{}
	min := "-"
	fields := append(append([]string(nil), apiKeyFields...), "hash", "expires_ms")
	for {
		ids, err := rdb.ZRangeByLex(ctx, apiKeysIndex, &redis.ZRangeBy{Min: min, Max: "+", Count: 200}).Result()
		if err != nil {
			return errors.New("cannot read legacy API key registry")
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if !validAPIKeyID(id) {
				return errors.New("invalid legacy API key record")
			}
			values, err := rdb.HMGet(ctx, apiKeyRecord(id), fields...).Result()
			if err != nil {
				return errors.New("cannot read legacy API key registry")
			}
			if len(values) != len(fields) {
				return errors.New("invalid legacy API key record")
			}
			// Redis authentication requires a revocation field and numeric expiry.
			// Do not turn a missing field into the SQL defaults for an active key.
			if _, ok := values[5].(string); !ok {
				return errors.New("invalid legacy API key revocation")
			}
			if _, ok := values[4].(string); !ok {
				return errors.New("invalid legacy API key expiry")
			}
			key, err := apiKeyFromFields(values[:6], time.Now())
			if err != nil || key.ID != id {
				return errors.New("invalid legacy API key record")
			}
			hash, ok := values[6].(string)
			decoded, err := hex.DecodeString(hash)
			if !ok || err != nil || len(decoded) != 32 {
				return errors.New("invalid legacy API key hash")
			}
			expiresText, ok := values[7].(string)
			expiresMillis, err := strconv.ParseInt(expiresText, 10, 64)
			if !ok || err != nil {
				return errors.New("invalid legacy API key expiry")
			}
			expectedExpiry, err := sqlAPIKeyExpiry(key)
			if err != nil || expiresMillis != expectedExpiry {
				return errors.New("inconsistent legacy API key expiry")
			}
			records = append(records, LegacyAPIKeyRecord{Key: key, Hash: hash})
		}
		min = "(" + ids[len(ids)-1]
	}
	return metadata.ImportAPIKeys(ctx, records)
}
