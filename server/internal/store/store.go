// Package store persists devices, circles, members, invites, envelopes
// (opaque ciphertext only) and circle-key distribution blobs.
//
// The store is deliberately dumb: envelope payloads are never parsed.
// SQLite is the default backend (pure-Go driver, zero-config self-hosting);
// all queries go through this package so a Postgres backend can be added
// later without touching the API layer.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQL database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at dsn and migrates it.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS devices (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	ed25519_pub   TEXT NOT NULL,
	x25519_pub    TEXT NOT NULL,
	token_hash    TEXT NOT NULL UNIQUE,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS circles (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL,
	color            TEXT NOT NULL,
	owner_device_id  TEXT NOT NULL,
	invite_code      TEXT NOT NULL UNIQUE,
	created_at       INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS circle_members (
	circle_id       TEXT NOT NULL,
	device_id       TEXT NOT NULL,
	role            TEXT NOT NULL,
	avatar_color    TEXT NOT NULL DEFAULT '',
	sharing_enabled INTEGER NOT NULL DEFAULT 1,
	joined_at       INTEGER NOT NULL,
	PRIMARY KEY (circle_id, device_id)
);
CREATE TABLE IF NOT EXISTS invites (
	code       TEXT PRIMARY KEY,
	circle_id  TEXT NOT NULL,
	created_by TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS envelopes (
	id         TEXT PRIMARY KEY,
	circle_id  TEXT NOT NULL,
	device_id  TEXT NOT NULL,
	kind       TEXT NOT NULL,
	ts         INTEGER NOT NULL,
	nonce      TEXT NOT NULL,
	ciphertext TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_env_circle_ts   ON envelopes(circle_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_env_circle_kind ON envelopes(circle_id, kind, ts DESC);
CREATE TABLE IF NOT EXISTS key_blobs (
	circle_id  TEXT NOT NULL,
	device_id  TEXT NOT NULL,
	ciphertext TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (circle_id, device_id)
);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func nowMS() int64 { return time.Now().UnixMilli() }
