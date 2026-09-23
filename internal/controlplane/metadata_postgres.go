package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OpenPostgresMetadataStore opens a shared control-plane catalog. The server
// executable registers pgx; normal CLI builds do not carry a SQL driver.
func OpenPostgresMetadataStore(ctx context.Context, dsn string) (*MetadataStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("Postgres metadata URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("cannot open Postgres metadata database")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(15 * time.Minute)
	store := &MetadataStore{db: db, postgres: true}
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(ready); err != nil {
		_ = store.Close()
		return nil, errors.New("cannot connect to Postgres metadata database")
	}
	if err := store.initializePostgres(ready); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *MetadataStore) initializePostgres(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot initialize Postgres metadata database")
	}
	defer tx.Rollback()
	// A transaction-scoped lock is compatible with transaction-pooling proxies.
	// It also protects CREATE TABLE IF NOT EXISTS from concurrent cold starts.
	const schemaLockID int64 = 0x41465343504c414e // AFSCPLAN
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLockID); err != nil {
		return errors.New("cannot lock Postgres metadata initialization")
	}
	statements := []string{
		"CREATE SCHEMA IF NOT EXISTS afs_control_plane",
		`CREATE TABLE IF NOT EXISTS metadata_settings (
 name TEXT PRIMARY KEY NOT NULL,
 value TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS database_profiles (
 id TEXT PRIMARY KEY NOT NULL,
 position INTEGER NOT NULL,
 profile TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
 id TEXT PRIMARY KEY NOT NULL,
 name TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_used_at TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL DEFAULT '',
 expires_ms BIGINT NOT NULL DEFAULT 0,
 revoked_at TEXT NOT NULL DEFAULT '',
 hash TEXT NOT NULL
)`,
		browserSessionSchema,
		cliLoginSchema,
		"INSERT INTO metadata_settings (name, value) VALUES ('registry_revision', '0') ON CONFLICT(name) DO NOTHING",
		"INSERT INTO metadata_settings (name, value) VALUES ('schema_version', '1') ON CONFLICT(name) DO NOTHING",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, s.querySQL(statement)); err != nil {
			return errors.New("cannot initialize Postgres metadata schema")
		}
	}
	var version string
	if err := tx.QueryRowContext(ctx, s.querySQL("SELECT value FROM metadata_settings WHERE name = 'schema_version'")).Scan(&version); err != nil || version != "1" {
		return errors.New("unsupported Postgres metadata schema")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit Postgres metadata initialization")
	}
	return nil
}

// querySQL handles only our fixed SQL statements: data always uses parameters.
// Qualifying tables avoids relying on connection-local search_path settings,
// which are not retained reliably by hosted transaction-pooling proxies.
func (s *MetadataStore) querySQL(query string) string {
	if !s.postgres {
		return query
	}
	query = strings.NewReplacer(
		"metadata_settings", "afs_control_plane.metadata_settings",
		"database_profiles", "afs_control_plane.database_profiles",
		"api_keys", "afs_control_plane.api_keys",
		"browser_sessions", "afs_control_plane.browser_sessions",
		"cli_login_requests", "afs_control_plane.cli_login_requests",
	).Replace(query)
	var result strings.Builder
	result.Grow(len(query) + 16)
	parameter := 1
	for _, ch := range query {
		if ch == '?' {
			fmt.Fprintf(&result, "$%d", parameter)
			parameter++
		} else {
			result.WriteRune(ch)
		}
	}
	return result.String()
}
