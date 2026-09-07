package store

import (
	"database/sql"
	"errors"
	"fmt"
) // EnvelopeKinds are the only envelope kinds the server accepts.
var EnvelopeKinds = map[string]bool{
	"location":  true,
	"message":   true,
	"checkin":   true,
	"sos":       true,
	"geofence":  true,
	"place":     true,
	"place_del": true,
	"circlekey": true,
	"presence":  true,
	"trip":      true, // end-of-drive summary (encrypted)
	"crash":     true, // possible crash alert (encrypted)
}

// IsEnvelopeKind reports whether kind is known.
func IsEnvelopeKind(kind string) bool { return EnvelopeKinds[kind] }

// Envelope is an opaque, client-encrypted payload plus routing metadata.
// The server MUST NOT parse nonce/ciphertext.
type Envelope struct {
	ID         string `json:"id"`
	CircleID   string `json:"circle_id"`
	DeviceID   string `json:"device_id"`
	Kind       string `json:"kind"`
	TS         int64  `json:"ts"`         // client-supplied unix ms
	Nonce      string `json:"nonce"`      // base64url, opaque
	Ciphertext string `json:"ciphertext"` // base64url, opaque
	CreatedAt  int64  `json:"created_at"` // server receive time, unix ms
}

// AddEnvelope stores an envelope and returns the stored copy. Duplicates
// (same circle + device + nonce — e.g. a client retry after a lost response)
// are ignored: returns (stored, false) WITHOUT inserting, and the returned
// row is the one that was stored the first time — a retry must observe the
// same id as the original broadcast, or client-side dedup would treat the
// retry as a brand-new envelope.
func (s *Store) AddEnvelope(e Envelope) (Envelope, bool, error) {
	if e.CreatedAt == 0 {
		e.CreatedAt = nowMS()
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO envelopes (id, circle_id, device_id, kind, ts, nonce, ciphertext, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.CircleID, e.DeviceID, e.Kind, e.TS, e.Nonce, e.Ciphertext, e.CreatedAt)
	if err != nil {
		return Envelope{}, false, fmt.Errorf("add envelope: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Duplicate: return the stored row (original id, original ts).
		stored, err := s.envelopeByNonce(e.CircleID, e.DeviceID, e.Nonce)
		if err != nil {
			return Envelope{}, false, err
		}
		return stored, false, nil
	}
	return e, true, nil
}

func (s *Store) envelopeByNonce(circleID, deviceID, nonce string) (Envelope, error) {
	var e Envelope
	err := s.db.QueryRow(`SELECT id, circle_id, device_id, kind, ts, nonce, ciphertext, created_at
		FROM envelopes WHERE circle_id = ? AND device_id = ? AND nonce = ?`,
		circleID, deviceID, nonce).
		Scan(&e.ID, &e.CircleID, &e.DeviceID, &e.Kind, &e.TS, &e.Nonce, &e.Ciphertext, &e.CreatedAt)
	if err != nil {
		return Envelope{}, fmt.Errorf("dedup lookup: %w", err)
	}
	return e, nil
}

// PruneEnvelopes deletes envelopes older than beforeMS, except emergency
// alerts (sos/crash) which are kept for the lifetime of the deployment.
// Returns the number of rows deleted.
func (s *Store) PruneEnvelopes(beforeMS int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM envelopes WHERE ts < ? AND kind NOT IN ('sos', 'crash')`, beforeMS)
	if err != nil {
		return 0, fmt.Errorf("prune envelopes: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Envelopes fetches envelopes for a circle, newest first, with optional
// kind and device filters and a "since" cursor (exclusive).
func (s *Store) Envelopes(circleID string, sinceTS int64, kind, deviceID string, limit int) ([]Envelope, error) {
	if limit > 1000 {
		limit = 1000
	}
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, circle_id, device_id, kind, ts, nonce, ciphertext, created_at
		FROM envelopes WHERE circle_id = ?`
	args := []any{circleID}
	if sinceTS > 0 {
		q += ` AND ts > ?`
		args = append(args, sinceTS)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if deviceID != "" {
		q += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	q += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("envelopes: %w", err)
	}
	defer rows.Close()

	var out []Envelope
	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.ID, &e.CircleID, &e.DeviceID, &e.Kind, &e.TS, &e.Nonce, &e.Ciphertext, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan envelope: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestPerDevice returns the most recent envelope of each kind for every
// member of a circle — what the map screen needs to render instantly.
// ROW_NUMBER keeps it tie-safe: two envelopes with the same ts for one
// (device, kind) must not produce duplicate rows.
func (s *Store) LatestPerDevice(circleID string) ([]Envelope, error) {
	rows, err := s.db.Query(`SELECT id, circle_id, device_id, kind, ts, nonce, ciphertext, created_at
		FROM (
			SELECT e.*, ROW_NUMBER() OVER (PARTITION BY device_id, kind ORDER BY ts DESC, id) AS rn
			FROM envelopes e WHERE circle_id = ?
		) WHERE rn = 1`, circleID)
	if err != nil {
		return nil, fmt.Errorf("latest per device: %w", err)
	}
	defer rows.Close()

	var out []Envelope
	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.ID, &e.CircleID, &e.DeviceID, &e.Kind, &e.TS, &e.Nonce, &e.Ciphertext, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan latest envelope: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnvelopeCount returns the total number of stored envelopes (admin stats).
func (s *Store) EnvelopeCount() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM envelopes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("envelope count: %w", err)
	}
	return n, nil
}

// PutKeyBlob stores a circle-key distribution blob addressed to one member.
// Content is opaque (sealed to that member's public key).
func (s *Store) PutKeyBlob(circleID, deviceID, ciphertext string) error {
	_, err := s.db.Exec(`INSERT INTO key_blobs (circle_id, device_id, ciphertext, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(circle_id, device_id) DO UPDATE SET ciphertext = excluded.ciphertext, updated_at = excluded.updated_at`,
		circleID, deviceID, ciphertext, nowMS())
	if err != nil {
		return fmt.Errorf("put key blob: %w", err)
	}
	return nil
}

// KeyBlob fetches the key blob addressed to deviceID in circleID.
func (s *Store) KeyBlob(circleID, deviceID string) (string, error) {
	var ct string
	err := s.db.QueryRow(`SELECT ciphertext FROM key_blobs WHERE circle_id = ? AND device_id = ?`,
		circleID, deviceID).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("key blob: %w", err)
	}
	return ct, nil
}

// Counts returns quick admin stats.
func (s *Store) Counts() (devices, circles, members int64, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&devices); err != nil {
		return 0, 0, 0, fmt.Errorf("count devices: %w", err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM circles`).Scan(&circles); err != nil {
		return 0, 0, 0, fmt.Errorf("count circles: %w", err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM circle_members`).Scan(&members); err != nil {
		return 0, 0, 0, fmt.Errorf("count members: %w", err)
	}
	return devices, circles, members, nil
}
