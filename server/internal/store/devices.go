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

// DeleteDevice removes the device row and, in one transaction, every
// membership, key blob, and push token attached to it. Used by
// DELETE /api/v1/devices/self: the bearer token's hash row disappears, so
// every token for this install stops working immediately.
func (s *Store) DeleteDevice(deviceID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete device: begin: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	// Leave circles cleanly: an owner's departure hands the role to the
	// longest-standing member so no circle is orphaned.
	rows, err := tx.Query(`SELECT circle_id, role FROM circle_members WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("delete device: memberships: %w", err)
	}
	var owned []string
	for rows.Next() {
		var circleID, role string
		if err := rows.Scan(&circleID, &role); err != nil {
			rows.Close()
			return fmt.Errorf("delete device: memberships scan: %w", err)
		}
		if role == "owner" {
			owned = append(owned, circleID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("delete device: memberships: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM devices WHERE id = ?`, deviceID); err != nil {
		return fmt.Errorf("delete device: row: %w", err)
	}
	// FKs keep referential integrity elsewhere, but memberships/key blobs
	// are deleted explicitly so ownership transfer sees the post-leave state.
	if _, err := tx.Exec(`DELETE FROM circle_members WHERE device_id = ?`, deviceID); err != nil {
		return fmt.Errorf("delete device: memberships: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM key_blobs WHERE device_id = ?`, deviceID); err != nil {
		return fmt.Errorf("delete device: key blobs: %w", err)
	}
	for _, circleID := range owned {
		next, err := oldestMemberTx(tx, circleID)
		if err != nil {
			return fmt.Errorf("delete device: successor: %w", err)
		}
		if next != "" {
			if _, err := tx.Exec(`UPDATE circle_members SET role = 'owner' WHERE circle_id = ? AND device_id = ?`, circleID, next); err != nil {
				return fmt.Errorf("delete device: promote: %w", err)
			}
			if _, err := tx.Exec(`UPDATE circles SET owner_device_id = ? WHERE id = ?`, next, circleID); err != nil {
				return fmt.Errorf("delete device: transfer: %w", err)
			}
		}
	}
	return tx.Commit()
}

// oldestMemberTx mirrors Store.OldestMember for an open transaction.
func oldestMemberTx(tx *sql.Tx, circleID string) (string, error) {
	var id string
	err := tx.QueryRow(`SELECT device_id FROM circle_members WHERE circle_id = ?
		ORDER BY joined_at ASC LIMIT 1`, circleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// PruneDevices deletes abandoned device rows: registrations with no
// circle memberships at all, created before cutoffMS. Every app reinstall
// registers a fresh device, so without this the table grows forever with
// rows that can never be used again (their tokens are lost). Devices that
// belong to any circle are always kept.
func (s *Store) PruneDevices(cutoffMS int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM devices
		WHERE created_at < ? AND id NOT IN (SELECT device_id FROM circle_members)`, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("prune devices: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
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
