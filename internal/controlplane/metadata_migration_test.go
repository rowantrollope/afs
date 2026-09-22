package controlplane

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMetadataMigrationRejectsMalformedSourceWithoutChangingEitherStore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		remove   string
		field    string
		value    string
		noExpiry bool
	}{
		{name: "missing expiry milliseconds", remove: "expires_ms"},
		{name: "invalid expiry milliseconds", field: "expires_ms", value: "not-a-number"},
		{name: "expired numeric value with future display expiry", field: "expires_ms", value: "1"},
		{name: "zero numeric value with future display expiry", field: "expires_ms", value: "0"},
		{name: "missing display expiry", remove: "expires_at"},
		{name: "empty display expiry with numeric expiry", field: "expires_at", value: ""},
		{name: "numeric expiry on nonexpiring key", field: "expires_ms", value: "1", noExpiry: true},
		{name: "missing revocation field", remove: "revoked_at"},
		{name: "missing hash", remove: "hash"},
		{name: "invalid hash", field: "hash", value: "private-token-that-must-not-appear-in-errors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			service, rdb := serviceFixture(t)
			metadata := metadataTestStore(t)
			now := time.Now().UTC()
			input := APIKeyCreateInput{Name: "Source key"}
			if tc.noExpiry {
				empty := ""
				input.ExpiresAt = &empty
			}
			key, err := validateAPIKeyInput(input, now)
			if err != nil {
				t.Fatal(err)
			}
			key, _, err = service.store.createAPIKey(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if tc.remove != "" {
				err = rdb.HDel(ctx, apiKeyRecord(key.ID), tc.remove).Err()
			} else {
				err = rdb.HSet(ctx, apiKeyRecord(key.ID), tc.field, tc.value).Err()
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := rdb.HGetAll(ctx, apiKeyRecord(key.ID)).Result()
			if err != nil {
				t.Fatal(err)
			}
			local, _ := sqlTestCreateKey(t, metadata, "Keep local key", now, true)
			if err := MigrateLegacyAPIKeys(ctx, metadata, rdb); err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("malformed source accepted or secret leaked: %v", err)
			}
			after, err := rdb.HGetAll(ctx, apiKeyRecord(key.ID)).Result()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("migration changed source: %v", err)
			}
			keys, _, err := metadata.listAPIKeys(ctx, "", 100, now)
			if err != nil || len(keys) != 1 || keys[0].ID != local.ID {
				t.Fatalf("failed import changed destination: %+v %v", keys, err)
			}
		})
	}
}

func TestMetadataMigrationPreservesExpiringExpiredAndNonexpiringKeys(t *testing.T) {
	ctx := context.Background()
	service, rdb := serviceFixture(t)
	metadata := metadataTestStore(t)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		expiry time.Time
		active bool
	}{
		{name: "expiring", expiry: now.Add(time.Hour), active: true},
		{name: "expired", expiry: now.Add(-time.Hour)},
		{name: "nonexpiring", active: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := APIKey{Name: tc.name, CreatedAt: serverTime(now.Add(-2 * time.Hour))}
			if !tc.expiry.IsZero() {
				key.ExpiresAt = serverTime(tc.expiry)
			}
			key, token, err := service.store.createAPIKey(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateLegacyAPIKeys(ctx, metadata, rdb); err != nil {
				t.Fatal(err)
			}
			imported, active, err := metadata.authenticateAPIKey(ctx, token, now)
			if err != nil || active != tc.active || active && (imported.ID != key.ID || imported.ExpiresAt != key.ExpiresAt) {
				t.Fatalf("changed key expiry: %+v active=%v err=%v", imported, active, err)
			}
		})
	}
}
