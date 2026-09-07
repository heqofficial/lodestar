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
	APNsToken  string `json:"-"`          // never serialized
	CreatedAt  int64  `json:"created_at"` // unix ms
}

// CreateDevice inserts a new device.
func (s *Store) CreateDevice(d Device) error {
	_, err := s.db.Exec(`INSERT INTO devices (id, name, ed25519_pub, x25519_pub, token_hash, apns_token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.ID, d.Name, d.Ed25519Pub, d.X25519Pub, d.TokenHash, d.APNsToken, d.CreatedAt)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	return nil
}

// SetAPNsToken stores (or, with an empty token, clears) a device's APNs
// registration token. Tokens rotate and expire; devices refresh them via
// PUT /devices/push, and the server clears them when Apple reports the
// device unregistered.
func (s *Store) SetAPNsToken(deviceID, token string) error {
	if _, err := s.db.Exec(`UPDATE devices SET apns_token = ? WHERE id = ?`, token, deviceID); err != nil {
		return fmt.Errorf("set apns token: %w", err)
	}
	return nil
}

// APNsTarget is a circle member reachable via APNs.
type APNsTarget struct {
	DeviceID  string
	APNsToken string
}

// APNsTargets lists devices in the circle that have registered an APNs
// token, for alert dispatch.
func (s *Store) APNsTargets(circleID string) ([]APNsTarget, error) {
	rows, err := s.db.Query(`SELECT d.id, d.apns_token
		FROM circle_members m JOIN devices d ON d.id = m.device_id
		WHERE m.circle_id = ? AND d.apns_token != ''`, circleID)
	if err != nil {
		return nil, fmt.Errorf("apns targets: %w", err)
	}
	defer rows.Close()
	var out []APNsTarget
	for rows.Next() {
		var t APNsTarget
		if err := rows.Scan(&t.DeviceID, &t.APNsToken); err != nil {
			return nil, fmt.Errorf("apns targets scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeviceByTokenHash looks up a device by the SHA-256 hash of its bearer token.
func (s *Store) DeviceByTokenHash(hash string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRow(`SELECT id, name, ed25519_pub, x25519_pub, token_hash, apns_token, created_at
		FROM devices WHERE token_hash = ?`, hash).
		Scan(&d.ID, &d.Name, &d.Ed25519Pub, &d.X25519Pub, &d.TokenHash, &d.APNsToken, &d.CreatedAt)
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
	err := s.db.QueryRow(`SELECT id, name, ed25519_pub, x25519_pub, token_hash, apns_token, created_at
		FROM devices WHERE id = ?`, id).
		Scan(&d.ID, &d.Name, &d.Ed25519Pub, &d.X25519Pub, &d.TokenHash, &d.APNsToken, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("device by id: %w", err)
	}
	return d, nil
}
