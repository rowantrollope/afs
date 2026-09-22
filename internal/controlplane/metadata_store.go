package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// MetadataStore owns the control plane's connection settings and administrator
// keys independently of the Redis databases it manages. The executable must
// register the sqlite database/sql driver; the CLI does not need that driver.
type MetadataStore struct {
	db        *sql.DB
	postgres  bool
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

// ErrMetadataConflict means another process committed a registry change after
// the caller loaded its snapshot. Callers must reload before retrying a change.
var ErrMetadataConflict = errors.New("database configuration changed; reload and try again")

// IsShared reports whether independent server instances share this catalog.
func (s *MetadataStore) IsShared() bool { return s.postgres }

// OpenMetadataStore opens a private SQLite database. A single control plane owns
// the catalog because its active Redis clients are an in-memory registry.
func OpenMetadataStore(filename string) (*MetadataStore, error) {
	if filename == "" {
		return nil, errors.New("metadata database path is required")
	}
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, errors.New("invalid metadata database path")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, errors.New("cannot create metadata database directory")
	}
	lock, err := openPrivateMetadataFile(filename + ".lock")
	if err != nil {
		return nil, errors.New("cannot open metadata database lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("metadata database is already in use by another control plane")
	}
	s := &MetadataStore{lock: lock}
	fail := func(message string) (*MetadataStore, error) {
		s.Close()
		return nil, errors.New(message)
	}
	file, err := openPrivateMetadataFile(filename)
	if err != nil {
		return fail("cannot open metadata database")
	}
	if err := file.Close(); err != nil {
		return fail("cannot close metadata database file")
	}
	// SQLite derives sidecar permissions from the main file. Tighten any existing
	// sidecars before opening, too, and refuse symlinks for all private files.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(filename + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		}
		file, err := openPrivateMetadataFile(filename + suffix)
		if err != nil {
			return fail("cannot secure metadata database sidecar")
		}
		if err := file.Close(); err != nil {
			return fail("cannot secure metadata database sidecar")
		}
	}
	dsn := &url.URL{Scheme: "file", Path: filename}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	s.db, err = sql.Open("sqlite", dsn.String())
	if err != nil {
		return fail("cannot open metadata database; SQLite driver must be registered")
	}
	// One connection also serializes API key changes and profile transactions.
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	var journalMode string
	if err := s.db.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil || journalMode != "wal" {
		return fail("cannot enable metadata database write-ahead log")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version > 1 {
		return fail("unsupported metadata database schema")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fail("cannot initialize metadata database")
	}
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS metadata_settings (
 name TEXT PRIMARY KEY NOT NULL,
 value TEXT NOT NULL
);
INSERT INTO metadata_settings (name, value) VALUES ('registry_revision', '0')
ON CONFLICT(name) DO NOTHING;
CREATE TABLE IF NOT EXISTS database_profiles (
 id TEXT PRIMARY KEY NOT NULL,
 position INTEGER NOT NULL,
 profile TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys (
 id TEXT PRIMARY KEY NOT NULL,
 name TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_used_at TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL DEFAULT '',
 expires_ms INTEGER NOT NULL DEFAULT 0,
 revoked_at TEXT NOT NULL DEFAULT '',
 hash TEXT NOT NULL
);
PRAGMA user_version = 1;
`)
	if err != nil {
		tx.Rollback()
		return fail("cannot initialize metadata database")
	}
	if err := tx.Commit(); err != nil {
		return fail("cannot initialize metadata database")
	}
	return s, nil
}

func openPrivateMetadataFile(filename string) (*os.File, error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("metadata path must be a regular file")
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (s *MetadataStore) Close() error {
	s.closeOnce.Do(func() {
		if s.db != nil {
			if err := s.db.Close(); err != nil {
				s.closeErr = errors.New("cannot close metadata database")
			}
		}
		if s.lock != nil {
			if err := s.lock.Close(); err != nil && s.closeErr == nil {
				s.closeErr = errors.New("cannot release metadata database lock")
			}
		}
	})
	return s.closeErr
}

// Ping checks the control-plane catalog without contacting managed Redis data.
func (s *MetadataStore) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errors.New("metadata database is unavailable")
	}
	return nil
}

// LoadProfiles distinguishes a fresh catalog from an intentionally empty one.
func (s *MetadataStore) LoadProfiles(ctx context.Context) ([]databaseProfile, string, bool, error) {
	profiles, defaultID, initialized, _, err := s.LoadProfilesSnapshot(ctx)
	return profiles, defaultID, initialized, err
}

// LoadProfilesSnapshot reads the registry and its revision from one consistent
// snapshot. PostgreSQL's default READ COMMITTED would allow its separate reads
// to observe different concurrent commits.
func (s *MetadataStore) LoadProfilesSnapshot(ctx context.Context) ([]databaseProfile, string, bool, int64, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if s.postgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return nil, "", false, 0, errors.New("cannot read metadata database")
	}
	defer tx.Rollback()
	var rawRevision string
	if err := tx.QueryRowContext(ctx, s.querySQL("SELECT value FROM metadata_settings WHERE name = 'registry_revision'")).Scan(&rawRevision); err != nil {
		return nil, "", false, 0, errors.New("cannot read database configuration revision")
	}
	revision, err := strconv.ParseInt(rawRevision, 10, 64)
	if err != nil || revision < 0 {
		return nil, "", false, 0, errors.New("invalid database configuration revision")
	}
	var defaultID string
	err = tx.QueryRowContext(ctx, s.querySQL("SELECT value FROM metadata_settings WHERE name = 'default_database_id'")).Scan(&defaultID)
	initialized := !errors.Is(err, sql.ErrNoRows)
	if err != nil && initialized {
		return nil, "", false, 0, errors.New("cannot read default database setting")
	}
	rows, err := tx.QueryContext(ctx, s.querySQL("SELECT id, profile FROM database_profiles ORDER BY position, id"))
	if err != nil {
		return nil, "", false, 0, errors.New("cannot read database profiles")
	}
	defer rows.Close()
	profiles := make([]databaseProfile, 0)
	for rows.Next() {
		var id, raw string
		var profile databaseProfile
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, "", false, 0, errors.New("cannot read database profile")
		}
		if err := json.Unmarshal([]byte(raw), &profile); err != nil || profile.ID != id || !validDatabaseID(id) {
			return nil, "", false, 0, errors.New("invalid database profile in metadata database")
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, 0, errors.New("cannot read database profiles")
	}
	if err := rows.Close(); err != nil {
		return nil, "", false, 0, errors.New("cannot read database profiles")
	}
	if !initialized && len(profiles) != 0 || !validProfileDefault(profiles, defaultID) {
		return nil, "", false, 0, errors.New("invalid default database setting")
	}
	if err := tx.Commit(); err != nil {
		return nil, "", false, 0, errors.New("cannot finish reading metadata database")
	}
	return profiles, defaultID, initialized, revision, nil
}

func validProfileDefault(profiles []databaseProfile, defaultID string) bool {
	if len(profiles) == 0 {
		return defaultID == ""
	}
	for _, profile := range profiles {
		if profile.ID == defaultID {
			return true
		}
	}
	return false
}

// SaveProfiles commits the registry and its default together. No partially
// updated connection list is visible if a write or transaction commit fails.
func (s *MetadataStore) SaveProfiles(ctx context.Context, profiles []databaseProfile, defaultID string) error {
	if s.postgres {
		return errors.New("shared metadata requires a revision-checked save")
	}
	_, _, _, revision, err := s.LoadProfilesSnapshot(ctx)
	if err != nil {
		return err
	}
	_, err = s.SaveProfilesRevision(ctx, profiles, defaultID, revision)
	return err
}

// SaveProfilesRevision replaces a registry only when its persisted revision
// still matches the caller's snapshot. The revision claim and replacement are
// atomic, preventing independently cached servers from losing one another's
// changes. A failed transaction never consumes the revision.
func (s *MetadataStore) SaveProfilesRevision(ctx context.Context, profiles []databaseProfile, defaultID string, expectedRevision int64) (int64, error) {
	if !validProfileDefault(profiles, defaultID) {
		return 0, errors.New("default database must refer to a saved connection")
	}
	if expectedRevision < 0 || expectedRevision == int64(^uint64(0)>>1) {
		return 0, errors.New("invalid database configuration revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New("cannot save database profiles")
	}
	defer tx.Rollback()
	nextRevision := expectedRevision + 1
	claim, err := tx.ExecContext(ctx, s.querySQL("UPDATE metadata_settings SET value = ? WHERE name = 'registry_revision' AND value = ?"), strconv.FormatInt(nextRevision, 10), strconv.FormatInt(expectedRevision, 10))
	if err != nil {
		return 0, errors.New("cannot save database configuration revision")
	}
	changed, err := claim.RowsAffected()
	if err != nil {
		return 0, errors.New("cannot verify database configuration revision")
	}
	if changed != 1 {
		return 0, ErrMetadataConflict
	}
	if _, err := tx.ExecContext(ctx, s.querySQL("DELETE FROM database_profiles")); err != nil {
		return 0, errors.New("cannot save database profiles")
	}
	for i, profile := range profiles {
		if !validDatabaseID(profile.ID) {
			return 0, errors.New("invalid database ID in configuration")
		}
		raw, err := json.Marshal(profile)
		if err != nil {
			return 0, errors.New("invalid database profile")
		}
		if _, err := tx.ExecContext(ctx, s.querySQL("INSERT INTO database_profiles (id, position, profile) VALUES (?, ?, ?)"), profile.ID, i, string(raw)); err != nil {
			return 0, errors.New("cannot save database profiles")
		}
	}
	if _, err := tx.ExecContext(ctx, s.querySQL("INSERT INTO metadata_settings (name, value) VALUES ('default_database_id', ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value"), defaultID); err != nil {
		return 0, errors.New("cannot save default database setting")
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.New("cannot commit database profiles")
	}
	return nextRevision, nil
}
