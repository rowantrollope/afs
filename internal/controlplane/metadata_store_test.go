package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func metadataTestStore(t *testing.T) *MetadataStore {
	t.Helper()
	store, err := OpenMetadataStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestMetadataProfilesPersistencePrivacyAndOwnership(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "private", "catalog.sqlite")
	store, err := OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	profiles, defaultID, initialized, err := store.LoadProfiles(ctx)
	if err != nil || len(profiles) != 0 || defaultID != "" || initialized {
		t.Fatalf("fresh catalog: profiles=%v default=%q initialized=%v err=%v", profiles, defaultID, initialized, err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenMetadataStore(filename); err == nil {
		other.Close()
		t.Fatal("second owner was accepted")
	}
	want := []databaseProfile{
		{ID: "cloud", Name: "Cloud", Description: "First connection", Revision: "r1", RedisAddr: "redis.example:6379", RedisUsername: "operator", RedisPassword: "test-secret", RedisDB: 3, RedisTLS: true, RedisURL: "rediss://redis.example:6379/3"},
		{ID: "local", Name: "Local", Revision: "r2", RedisAddr: "127.0.0.1:6379"},
	}
	if err := store.SaveProfiles(ctx, want, "cloud"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".lock", "-wal", "-shm"} {
		info, err := os.Stat(filename + suffix)
		if err != nil {
			t.Fatalf("missing metadata file %q: %v", suffix, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("metadata file %q permissions: %v", suffix, info.Mode())
		}
	}
	info, err := os.Stat(filepath.Dir(filename))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("new metadata directory is not private: %v %v", info, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	profiles, defaultID, initialized, err = store.LoadProfiles(ctx)
	if err != nil || !reflect.DeepEqual(profiles, want) || defaultID != "cloud" || !initialized {
		t.Fatalf("reopened catalog: profiles=%v default=%q initialized=%v err=%v", profiles, defaultID, initialized, err)
	}
	if err := store.SaveProfiles(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	profiles, defaultID, initialized, err = store.LoadProfiles(ctx)
	if err != nil || len(profiles) != 0 || defaultID != "" || !initialized {
		t.Fatalf("intentionally empty catalog: profiles=%v default=%q initialized=%v err=%v", profiles, defaultID, initialized, err)
	}
}

func TestMetadataProfileFailureRollsBackRegistryAndDefault(t *testing.T) {
	store := metadataTestStore(t)
	ctx := context.Background()
	original := []databaseProfile{{ID: "original", Name: "Original", RedisAddr: "localhost:6379", RedisPassword: "private-credential"}}
	if err := store.SaveProfiles(ctx, original, "original"); err != nil {
		t.Fatal(err)
	}
	// Fail after DELETE and the first INSERT, proving rollback of partial work.
	replacement := []databaseProfile{{ID: "new", Name: "One"}, {ID: "new", Name: "Duplicate"}}
	if err := store.SaveProfiles(ctx, replacement, "new"); err == nil || strings.Contains(err.Error(), "private-credential") {
		t.Fatalf("duplicate write should fail without private details: %v", err)
	}
	profiles, defaultID, initialized, err := store.LoadProfiles(ctx)
	if err != nil || !reflect.DeepEqual(profiles, original) || defaultID != "original" || !initialized {
		t.Fatalf("failed save changed catalog: %v %q %v %v", profiles, defaultID, initialized, err)
	}
	if err := store.SaveProfiles(ctx, original, "missing"); err == nil {
		t.Fatal("missing default accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.SaveProfiles(canceled, nil, ""); err == nil {
		t.Fatal("canceled save succeeded")
	}
	profiles, defaultID, _, err = store.LoadProfiles(ctx)
	if err != nil || !reflect.DeepEqual(profiles, original) || defaultID != "original" {
		t.Fatal("canceled save changed persisted state")
	}
}

func TestMetadataRejectsSymlinksAndCorruptProfiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "catalog.sqlite")
	if err := os.Symlink(target, filename); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenMetadataStore(filename); err == nil {
		store.Close()
		t.Fatal("symlink was followed")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "untouched" {
		t.Fatal("symlink target changed")
	}
	store := metadataTestStore(t)
	if _, err := store.db.Exec("INSERT INTO database_profiles(id, position, profile) VALUES ('one', 0, ?)", `{"id":"wrong","redis_password":"private-credential"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.LoadProfiles(context.Background()); err == nil || strings.Contains(err.Error(), "private-credential") {
		t.Fatalf("corrupt profile accepted or exposed: %v", err)
	}
}

func TestMetadataRevisionRejectsStaleWritesAndRollsBackFailedClaim(t *testing.T) {
	store := metadataTestStore(t)
	ctx := context.Background()
	if store.IsShared() {
		t.Fatal("SQLite unexpectedly reported shared ownership")
	}
	profiles, _, initialized, revision, err := store.LoadProfilesSnapshot(ctx)
	if err != nil || initialized || revision != 0 || len(profiles) != 0 {
		t.Fatalf("initial revision: %d initialized=%v err=%v", revision, initialized, err)
	}
	original := []databaseProfile{{ID: "first", Name: "First", RedisAddr: "localhost:6379"}}
	revision, err = store.SaveProfilesRevision(ctx, original, "first", revision)
	if err != nil || revision != 1 {
		t.Fatalf("initial save: revision=%d err=%v", revision, err)
	}
	if _, err := store.SaveProfilesRevision(ctx, nil, "", 0); !errors.Is(err, ErrMetadataConflict) {
		t.Fatalf("stale save accepted: %v", err)
	}
	duplicate := []databaseProfile{{ID: "second"}, {ID: "second"}}
	if _, err := store.SaveProfilesRevision(ctx, duplicate, "second", revision); err == nil {
		t.Fatal("invalid save unexpectedly succeeded")
	}
	profiles, defaultID, _, afterRevision, err := store.LoadProfilesSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(profiles, original) || defaultID != "first" || afterRevision != revision {
		t.Fatalf("failed write changed snapshot/revision: %+v %q %d %v", profiles, defaultID, afterRevision, err)
	}
	if next, err := store.SaveProfilesRevision(ctx, nil, "", revision); err != nil || next != 2 {
		t.Fatalf("failed claim consumed revision: %d %v", next, err)
	}
}

func TestMetadataAddsRevisionToExistingSQLiteCatalog(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	original := []databaseProfile{{ID: "original", Name: "Original", RedisAddr: "localhost:6379"}}
	if err := store.SaveProfiles(ctx, original, "original"); err != nil {
		t.Fatal(err)
	}
	// Catalogs created before shared-store support have no revision setting.
	if _, err := store.db.Exec("DELETE FROM metadata_settings WHERE name = 'registry_revision'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profiles, defaultID, initialized, revision, err := store.LoadProfilesSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(profiles, original) || defaultID != "original" || !initialized || revision != 0 {
		t.Fatalf("legacy catalog changed: %+v %q %v %d %v", profiles, defaultID, initialized, revision, err)
	}
}
