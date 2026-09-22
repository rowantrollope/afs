package controlplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
)

type apiKeyStore interface {
	createAPIKey(context.Context, APIKey) (APIKey, string, error)
	authenticateAPIKey(context.Context, string, time.Time) (APIKey, bool, error)
	revokeAPIKey(context.Context, string, time.Time) (APIKey, error)
	listAPIKeys(context.Context, string, int, time.Time) ([]APIKey, string, error)
}

var _ apiKeyStore = (*MetadataStore)(nil)
var _ apiKeyStore = (*Store)(nil)

const sqlAPIKeyColumns = "id, name, created_at, last_used_at, expires_at, revoked_at"

type apiKeyScanner interface {
	Scan(...any) error
}

func scanSQLAPIKey(row apiKeyScanner, now time.Time) (APIKey, error) {
	var id, name, created, lastUsed, expires, revoked string
	if err := row.Scan(&id, &name, &created, &lastUsed, &expires, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, os.ErrNotExist
		}
		return APIKey{}, errors.New("cannot read API key metadata")
	}
	return apiKeyFromFields([]interface{}{id, name, created, lastUsed, expires, revoked}, now)
}

func (s *MetadataStore) createAPIKey(ctx context.Context, key APIKey) (APIKey, string, error) {
	var id [16]byte
	var secret [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return APIKey{}, "", errors.New("cannot generate API key")
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return APIKey{}, "", errors.New("cannot generate API key")
	}
	key.ID = "key_" + hex.EncodeToString(id[:])
	key.LastUsedAt, key.RevokedAt, key.Status = "", "", "active"
	token := "afs_" + key.ID + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	expiresMillis, err := sqlAPIKeyExpiry(key)
	if err != nil {
		return APIKey{}, "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO api_keys
 (id, name, created_at, last_used_at, expires_at, expires_ms, revoked_at, hash)
 VALUES (?, ?, ?, '', ?, ?, '', ?)`, key.ID, key.Name, key.CreatedAt, key.ExpiresAt, expiresMillis, apiKeyHash(token))
	if err != nil {
		return APIKey{}, "", errors.New("cannot create API key")
	}
	return key, token, nil
}

func sqlAPIKeyExpiry(key APIKey) (int64, error) {
	if key.ExpiresAt == "" {
		return 0, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, key.ExpiresAt)
	if err != nil {
		return 0, errors.New("invalid API key expiry")
	}
	return expires.UnixMilli(), nil
}

// Validation and the last-used touch are one statement, so authenticating a key
// cannot overwrite a concurrent revocation with stale metadata.
func (s *MetadataStore) authenticateAPIKey(ctx context.Context, token string, now time.Time) (APIKey, bool, error) {
	prefix, secret, ok := strings.Cut(token, ".")
	id := strings.TrimPrefix(prefix, "afs_")
	if !ok || !strings.HasPrefix(prefix, "afs_") || !validAPIKeyID(id) || len(secret) != 43 {
		return APIKey{}, false, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		return APIKey{}, false, nil
	}
	key, err := scanSQLAPIKey(s.db.QueryRowContext(ctx, `UPDATE api_keys SET last_used_at = ?
 WHERE id = ? AND hash = ? AND revoked_at = '' AND (expires_ms = 0 OR expires_ms > ?)
 RETURNING `+sqlAPIKeyColumns, serverTime(now), id, apiKeyHash(token), now.UnixMilli()), now)
	if errors.Is(err, os.ErrNotExist) {
		return APIKey{}, false, nil
	}
	if err != nil {
		return APIKey{}, false, err
	}
	return key, key.ID == id && key.Status == "active", nil
}

func (s *MetadataStore) revokeAPIKey(ctx context.Context, id string, now time.Time) (APIKey, error) {
	return scanSQLAPIKey(s.db.QueryRowContext(ctx, `UPDATE api_keys
 SET revoked_at = CASE WHEN revoked_at = '' THEN ? ELSE revoked_at END
 WHERE id = ? RETURNING `+sqlAPIKeyColumns, serverTime(now), id), now)
}

func (s *MetadataStore) listAPIKeys(ctx context.Context, cursor string, limit int, now time.Time) ([]APIKey, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", errors.New("API key page size must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+sqlAPIKeyColumns+" FROM api_keys WHERE id > ? ORDER BY id LIMIT ?", cursor, limit+1)
	if err != nil {
		return nil, "", errors.New("cannot list API keys")
	}
	defer rows.Close()
	keys := make([]APIKey, 0, limit)
	for rows.Next() {
		key, err := scanSQLAPIKey(rows, now)
		if err != nil {
			return nil, "", err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", errors.New("cannot list API keys")
	}
	next := ""
	if len(keys) > limit {
		keys = keys[:limit]
		next = keys[len(keys)-1].ID
	}
	return keys, next, nil
}

// LegacyAPIKeyRecord carries only the public metadata and stored token digest;
// migration does not need or recover the original administrator token.
type LegacyAPIKeyRecord struct {
	Key  APIKey
	Hash string
}

// ImportAPIKeys atomically inserts legacy records. Repeating an import keeps
// current usage and expiry, and merges revocations without reactivating keys.
// An ID whose token digest differs leaves the entire import unapplied.
func (s *MetadataStore) ImportAPIKeys(ctx context.Context, records []LegacyAPIKeyRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot import API keys")
	}
	defer tx.Rollback()
	for _, record := range records {
		key := record.Key
		if !validAPIKeyID(key.ID) || strings.TrimSpace(key.Name) == "" {
			return errors.New("invalid legacy API key metadata")
		}
		for i, timestamp := range []string{key.CreatedAt, key.LastUsedAt, key.RevokedAt} {
			if timestamp == "" && i != 0 {
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
				return errors.New("invalid legacy API key timestamp")
			}
		}
		digest, err := hex.DecodeString(record.Hash)
		if err != nil || len(digest) != 32 || record.Hash != strings.ToLower(record.Hash) {
			return errors.New("invalid legacy API key digest")
		}
		expiresMillis, err := sqlAPIKeyExpiry(key)
		if err != nil {
			return err
		}
		var existingHash string
		err = tx.QueryRowContext(ctx, "SELECT hash FROM api_keys WHERE id = ?", key.ID).Scan(&existingHash)
		if err == nil {
			if existingHash != record.Hash {
				return errors.New("legacy API key conflicts with an existing key")
			}
			if key.RevokedAt != "" {
				if _, err := tx.ExecContext(ctx, "UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at = ''", key.RevokedAt, key.ID); err != nil {
					return errors.New("cannot import API key revocation")
				}
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return errors.New("cannot check existing API key")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO api_keys
 (id, name, created_at, last_used_at, expires_at, expires_ms, revoked_at, hash)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, key.ID, key.Name, key.CreatedAt, key.LastUsedAt, key.ExpiresAt, expiresMillis, key.RevokedAt, record.Hash)
		if err != nil {
			return errors.New("cannot import API key")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit imported API keys")
	}
	return nil
}
