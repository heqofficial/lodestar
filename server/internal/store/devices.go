package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Device is a registered app installation.
type Device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Ed25519Pub string `json:"ed25519_pub"`
	X25519Pub  string `json:"x25519_pub"`
	TokenHash  string `json:"-"`          // never serialized
	CreatedAt  int64  `json:"created_at"` // unix ms
}

var (
	// ErrNotFound is returned when a row does not exist.
	// ErrDuplicate is returned on unique-constraint violations.
	ErrDuplicate = errors.New("duplicate")
)

// CreateDevice inserts a new device.
func (s *Store) CreateDevice(d Device) error {
	_, err := s.db.Exec(`INSERT INTO devices (id, name, ed25519_pub, x25519_pub, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, d.ID, d.Name, d.Ed25519Pub, d.X25519Pub, d.TokenHash, d.CreatedAt)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	return nil
}

// DeviceByTokenHash looks up a device by the SHA-256 hash of its bearer token.
func (s *Store) DeviceByTokenHash(hash string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRow(`SELECT id, name, ed25519_pub, x25519_pub, token_hash, created_at
		FROM devices WHERE token_hash = ?`, hash).
		Scan(&d.ID, &d.Name, &d.Ed25519Pub, &d.X25519Pub, &d.TokenHash, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("device by token hash: %w", err)
	}
	return d, nil
}

// DeviceByID looks up a device by ID.
func (s *Store) DeviceByID(id string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRow(`SELECT id, name, ed25519_pub, x25519_pub, token_hash, created_at
		FROM devices WHERE id = ?`, id).
		Scan(&d.ID, &d.Name, &d.Ed25519Pub, &d.X25519Pub, &d.TokenHash, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("device by id: %w", err)
	}
	return d, nil
}
