package controlplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// postgresTestDatabase creates a random database using an explicitly supplied
// disposable Postgres administrator endpoint. It never changes existing tables
// or schemas in that endpoint's database. Store cleanups registered by callers
// run before this helper's database-drop cleanup.
func postgresTestDatabase(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv("AFS_TEST_POSTGRES_URL")
	if endpoint == "" {
		t.Skip("set AFS_TEST_POSTGRES_URL to a disposable Postgres administrator endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatal("AFS_TEST_POSTGRES_URL must be a Postgres URL")
	}
	admin, err := sql.Open("pgx", endpoint)
	if err != nil {
		t.Fatal("cannot open disposable Postgres administrator connection")
	}
	var nonce [10]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		admin.Close()
		t.Fatal("cannot generate isolated database name")
	}
	database := "afs_metadata_test_" + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+database+`"`); err != nil {
		admin.Close()
		t.Fatal("cannot create isolated Postgres test database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+database+`" WITH (FORCE)`); err != nil {
			t.Error("cannot remove isolated Postgres test database")
		}
		_ = admin.Close()
	})
	u.Path, u.RawPath = "/"+database, ""
	query := u.Query()
	query.Del("dbname")
	u.RawQuery = query.Encode()
	return u.String()
}

func postgresTestStore(t *testing.T, dsn string) *MetadataStore {
	t.Helper()
	store, err := OpenPostgresMetadataStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPostgresMetadataConcurrentInitializationAndRegistryCAS(t *testing.T) {
	dsn := postgresTestDatabase(t)
	ctx := context.Background()
	type opened struct {
		store *MetadataStore
		err   error
	}
	ready := make(chan opened, 2)
	for i := 0; i < 2; i++ {
		go func() {
			store, err := OpenPostgresMetadataStore(ctx, dsn)
			ready <- opened{store, err}
		}()
	}
	stores := make([]*MetadataStore, 0, 2)
	for i := 0; i < 2; i++ {
		result := <-ready
		if result.err != nil {
			t.Fatal(result.err)
		}
		t.Cleanup(func() { result.store.Close() })
		stores = append(stores, result.store)
	}
	first, second := stores[0], stores[1]
	if !first.IsShared() || !second.IsShared() {
		t.Fatal("Postgres stores did not report shared ownership")
	}
	_, _, initialized, revision, err := first.LoadProfilesSnapshot(ctx)
	if err != nil || initialized || revision != 0 {
		t.Fatalf("fresh shared registry: initialized=%v revision=%d err=%v", initialized, revision, err)
	}
	if err := first.SaveProfiles(ctx, nil, ""); err == nil {
		t.Fatal("shared catalog accepted a save without an expected revision")
	}
	firstProfile := []databaseProfile{{ID: "first", Name: "First", RedisAddr: "localhost:6379"}}
	secondProfile := []databaseProfile{{ID: "second", Name: "Second", RedisAddr: "localhost:6380"}}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i, store := range stores {
		profile := firstProfile
		if i == 1 {
			profile = secondProfile
		}
		go func(store *MetadataStore, profiles []databaseProfile) {
			<-start
			_, err := store.SaveProfilesRevision(ctx, profiles, profiles[0].ID, revision)
			results <- err
		}(store, profile)
	}
	close(start)
	conflicts, successes := 0, 0
	for i := 0; i < 2; i++ {
		if err := <-results; errors.Is(err, ErrMetadataConflict) {
			conflicts++
		} else if err == nil {
			successes++
		} else {
			t.Fatal(err)
		}
	}
	if conflicts != 1 || successes != 1 {
		t.Fatalf("simultaneous writers: %d conflicts, %d successes", conflicts, successes)
	}
	profiles, defaultID, initialized, revision, err := second.LoadProfilesSnapshot(ctx)
	if err != nil || len(profiles) != 1 || defaultID != profiles[0].ID || !initialized || revision != 1 {
		t.Fatalf("committed snapshot: %+v %q %v %d %v", profiles, defaultID, initialized, revision, err)
	}
	duplicate := []databaseProfile{{ID: "duplicate"}, {ID: "duplicate"}}
	if _, err := first.SaveProfilesRevision(ctx, duplicate, "duplicate", revision); err == nil {
		t.Fatal("duplicate profile save succeeded")
	}
	after, afterDefault, _, afterRevision, err := second.LoadProfilesSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(after, profiles) || afterDefault != defaultID || afterRevision != revision {
		t.Fatalf("failed replacement changed shared state: %+v %q %d %v", after, afterDefault, afterRevision, err)
	}
}

func TestPostgresMetadataSnapshotConsistency(t *testing.T) {
	dsn := postgresTestDatabase(t)
	writer, reader := postgresTestStore(t, dsn), postgresTestStore(t, dsn)
	ctx := context.Background()
	profiles := [][]databaseProfile{
		{{ID: "first", Name: "First", RedisAddr: "localhost:6379"}},
		{{ID: "second", Name: "Second", RedisAddr: "localhost:6380"}},
	}
	revision, err := writer.SaveProfilesRevision(ctx, profiles[0], "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			next := profiles[i%2]
			var err error
			revision, err = writer.SaveProfilesRevision(ctx, next, next[0].ID, revision)
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 40; i++ {
		rows, defaultID, initialized, _, err := reader.LoadProfilesSnapshot(ctx)
		if err != nil || !initialized || len(rows) != 1 || defaultID != rows[0].ID {
			t.Errorf("mixed registry snapshot: %+v %q %v %v", rows, defaultID, initialized, err)
			break
		}
	}
	wg.Wait()
}

func TestPostgresAPIKeysAcrossInstancesAndLegacyImport(t *testing.T) {
	dsn := postgresTestDatabase(t)
	first, second := postgresTestStore(t, dsn), postgresTestStore(t, dsn)
	ctx := context.Background()
	now := time.Now().UTC()
	created, token := sqlTestCreateKey(t, first, "Shared operator", now, false)
	if _, valid, err := second.authenticateAPIKey(ctx, token, now); err != nil || !valid {
		t.Fatalf("second instance rejected key: valid=%v err=%v", valid, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := first.authenticateAPIKey(ctx, token, now); err != nil {
				t.Error(err)
			}
		}()
	}
	if _, err := second.revokeAPIKey(ctx, created.ID, now); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, valid, err := first.authenticateAPIKey(ctx, token, now.Add(time.Second)); err != nil || valid {
		t.Fatal("another instance did not observe revocation")
	}
	record := LegacyAPIKeyRecord{Key: APIKey{ID: "key_00000000000000000000000000000001", Name: "Imported", CreatedAt: serverTime(now)}, Hash: strings.Repeat("a", 64)}
	if err := first.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	record.Key.RevokedAt = serverTime(now)
	if err := second.ImportAPIKeys(ctx, []LegacyAPIKeyRecord{record}); err != nil {
		t.Fatal(err)
	}
	keys, next, err := first.listAPIKeys(ctx, "", 1, now)
	if err != nil || len(keys) != 1 || keys[0].ID != record.Key.ID || keys[0].Status != "revoked" || next == "" {
		t.Fatalf("shared import and pagination: %+v %q %v", keys, next, err)
	}
	keys, next, err = second.listAPIKeys(ctx, next, 1, now)
	if err != nil || len(keys) != 1 || keys[0].ID != created.ID || keys[0].Status != "revoked" || next != "" {
		t.Fatalf("shared second page: %+v %q %v", keys, next, err)
	}
}
