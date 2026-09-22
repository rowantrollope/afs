package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sqlTestCreateKey(t *testing.T, store *MetadataStore, name string, now time.Time, noExpiry bool) (APIKey, string) {
	t.Helper()
	input := APIKeyCreateInput{Name: name}
	if noExpiry {
		empty := ""
		input.ExpiresAt = &empty
	}
	key, err := validateAPIKeyInput(input, now)
	if err != nil {
		t.Fatal(err)
	}
	key, token, err := store.createAPIKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return key, token
}

func TestSQLAPIKeyLifecyclePersistsWithoutRedis(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	filename := filepath.Join(t.TempDir(), "catalog.sqlite")
	store, err := OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	created, token := sqlTestCreateKey(t, store, "Terminal", now, false)
	var storedHash string
	if err := store.db.QueryRow("SELECT hash FROM api_keys WHERE id = ?", created.ID).Scan(&storedHash); err != nil || storedHash != apiKeyHash(token) {
		t.Fatalf("key digest: %q %v", storedHash, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filename)
	if err != nil || strings.Contains(string(raw), token) || strings.Contains(string(raw), strings.Split(token, ".")[1]) {
		t.Fatal("catalog contained the original API token")
	}
	store, err = OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	authed, valid, err := store.authenticateAPIKey(ctx, token, now.Add(time.Second))
	if err != nil || !valid || authed.ID != created.ID || authed.LastUsedAt == "" {
		t.Fatalf("reopened API key auth failed: %+v %v %v", authed, valid, err)
	}
	badSecret := strings.Split(token, ".")[1]
	if badSecret[0] == 'A' {
		badSecret = "B" + badSecret[1:]
	} else {
		badSecret = "A" + badSecret[1:]
	}
	for _, bad := range []string{"", "invalid", token + "x", token[:len(token)-1] + "!", "afs_" + created.ID + "." + badSecret} {
		if _, valid, err := store.authenticateAPIKey(ctx, bad, now); err != nil || valid {
			t.Fatalf("tampered key accepted: valid=%v err=%v", valid, err)
		}
	}
	revoked, err := store.revokeAPIKey(ctx, created.ID, now.Add(2*time.Second))
	if err != nil || revoked.Status != "revoked" || revoked.RevokedAt == "" {
		t.Fatalf("revocation failed: %+v %v", revoked, err)
	}
	again, err := store.revokeAPIKey(ctx, created.ID, now.Add(3*time.Second))
	if err != nil || again.RevokedAt != revoked.RevokedAt {
		t.Fatal("repeat revocation changed timestamp")
	}
	if _, valid, err := store.authenticateAPIKey(ctx, token, now.Add(4*time.Second)); err != nil || valid {
		t.Fatalf("revoked key authenticated: valid=%v err=%v", valid, err)
	}
	if _, err := store.revokeAPIKey(ctx, "key_00000000000000000000000000000000", now); !os.IsNotExist(err) {
		t.Fatalf("missing key revocation: %v", err)
	}
}

func TestSQLAPIKeyExpiryPagingAndConcurrentRevocation(t *testing.T) {
	store := metadataTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expiring, expiringToken := sqlTestCreateKey(t, store, "Expires", now, false)
	never, neverToken := sqlTestCreateKey(t, store, "Permanent", now, true)
	expires, _ := time.Parse(time.RFC3339Nano, expiring.ExpiresAt)
	if _, valid, err := store.authenticateAPIKey(ctx, expiringToken, expires); err != nil || valid {
		t.Fatalf("key accepted at expiry: valid=%v err=%v", valid, err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, neverToken, expires.Add(time.Hour)); err != nil || !valid {
		t.Fatalf("non-expiring key rejected: valid=%v err=%v", valid, err)
	}
	for i := 0; i < 3; i++ {
		sqlTestCreateKey(t, store, "Pagination", now, true)
	}
	seen := map[string]APIKey{}
	cursor := ""
	for {
		keys, next, err := store.listAPIKeys(ctx, cursor, 2, expires)
		if err != nil || len(keys) > 2 {
			t.Fatalf("key page: %v %v", keys, err)
		}
		for _, key := range keys {
			if _, exists := seen[key.ID]; exists || key.ID <= cursor {
				t.Fatal("unstable key pagination")
			}
			seen[key.ID] = key
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 || seen[expiring.ID].Status != "expired" || seen[never.ID].Status != "active" {
		t.Fatalf("missing keys or incorrect status: %+v", seen)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := store.authenticateAPIKey(ctx, neverToken, now); err != nil {
				t.Errorf("concurrent authentication: %v", err)
			}
		}()
	}
	if _, err := store.revokeAPIKey(ctx, never.ID, now); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, valid, err := store.authenticateAPIKey(ctx, neverToken, now.Add(time.Second)); err != nil || valid {
		t.Fatalf("revocation was overwritten: valid=%v err=%v", valid, err)
	}
	if _, err := store.db.Exec("UPDATE api_keys SET expires_at = 'invalid', expires_ms = 0 WHERE id = ?", expiring.ID); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, expiringToken, now); err == nil || valid {
		t.Fatal("corrupt expiry did not fail closed")
	}
}

func TestSQLAPIKeyImportAtomicAndNeverReactivates(t *testing.T) {
	store := metadataTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	key := APIKey{ID: "key_00000000000000000000000000000001", Name: "Legacy", CreatedAt: serverTime(now), Status: "active"}
	token := "afs_" + key.ID + "." + strings.Repeat("A", 43)
	record := LegacyAPIKeyRecord{Key: key, Hash: apiKeyHash(token)}
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, token, now); err != nil || !valid {
		t.Fatalf("legacy key did not survive migration: valid=%v err=%v", valid, err)
	}
	if _, err := store.revokeAPIKey(ctx, key.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, token, now); err != nil || valid {
		t.Fatal("repeated migration reactivated key")
	}
	second := record
	second.Key.ID = "key_00000000000000000000000000000002"
	conflict := record
	conflict.Hash = strings.Repeat("b", 64)
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{second, conflict}); err == nil {
		t.Fatal("conflicting legacy key accepted")
	}
	keys, _, err := store.listAPIKeys(ctx, "", 100, now)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID || keys[0].Status != "revoked" {
		t.Fatalf("failed migration was not atomic: %+v %v", keys, err)
	}
	invalid := second
	invalid.Hash = "not-a-token-digest"
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{invalid}); err == nil {
		t.Fatal("invalid migration digest accepted")
	}
}

func TestSQLAPIKeyImportMergesSourceRevocation(t *testing.T) {
	store := metadataTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	key := APIKey{ID: "key_00000000000000000000000000000001", Name: "Legacy", CreatedAt: serverTime(now)}
	token := "afs_" + key.ID + "." + strings.Repeat("A", 43)
	record := LegacyAPIKeyRecord{Key: key, Hash: apiKeyHash(token)}
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, token, now); err != nil || !valid {
		t.Fatalf("initial imported key rejected: valid=%v err=%v", valid, err)
	}
	record.Key.RevokedAt = serverTime(now.Add(time.Second))
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.authenticateAPIKey(ctx, token, now.Add(2*time.Second)); err != nil || valid {
		t.Fatal("repeat migration ignored source revocation")
	}
	record.Key.RevokedAt = serverTime(now.Add(3 * time.Second))
	if err := store.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	keys, _, err := store.listAPIKeys(ctx, "", 100, now)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt != serverTime(now.Add(time.Second)) || keys[0].LastUsedAt != serverTime(now) {
		t.Fatalf("repeat migration overwrote local state: %+v %v", keys, err)
	}
}
