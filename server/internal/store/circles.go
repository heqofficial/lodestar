package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// MaxMembers caps how many devices may share one circle.
const MaxMembers = 64

// Circle is a family circle.
type Circle struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Color         string `json:"color"`
	OwnerDeviceID string `json:"owner_device_id"`
	InviteCode    string `json:"invite_code"`
	CreatedAt     int64  `json:"created_at"` // unix ms
}

// Member is a device's membership in a circle, joined with device info.
type Member struct {
	CircleID       string `json:"circle_id"`
	DeviceID       string `json:"device_id"`
	Role           string `json:"role"` // "owner" | "member"
	DisplayName    string `json:"display_name"`
	AvatarColor    string `json:"avatar_color"`
	Ed25519Pub     string `json:"ed25519_pub"`
	X25519Pub      string `json:"x25519_pub"`
	SharingEnabled bool   `json:"sharing_enabled"`
	JoinedAt       int64  `json:"joined_at"` // unix ms
}

// Invite is a join code for a circle.
type Invite struct {
	Code      string `json:"code"`
	CircleID  string `json:"circle_id"`
	CreatedBy string `json:"created_by"`
	ExpiresAt int64  `json:"expires_at"` // unix ms
}

// CreateCircle inserts a circle and makes the creator its owner/member.
func (s *Store) CreateCircle(c Circle, avatarColor string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("create circle begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO circles (id, name, color, owner_device_id, invite_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.ID, c.Name, c.Color, c.OwnerDeviceID, c.InviteCode, c.CreatedAt); err != nil {
		return fmt.Errorf("create circle: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO circle_members (circle_id, device_id, role, avatar_color, sharing_enabled, joined_at)
		VALUES (?, ?, 'owner', ?, 1, ?)`, c.ID, c.OwnerDeviceID, avatarColor, c.CreatedAt); err != nil {
		return fmt.Errorf("create circle member: %w", err)
	}
	// Seed the circle's initial invite code so the creator can share it right away.
	if _, err := tx.Exec(`INSERT INTO invites (code, circle_id, created_by, expires_at)
		VALUES (?, ?, ?, ?)`, c.InviteCode, c.ID, c.OwnerDeviceID, c.CreatedAt+30*24*3600_000); err != nil {
		return fmt.Errorf("create circle invite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create circle commit: %w", err)
	}
	return nil
}

// CircleByID fetches a circle.
func (s *Store) CircleByID(id string) (*Circle, error) {
	c := &Circle{}
	err := s.db.QueryRow(`SELECT id, name, color, owner_device_id, invite_code, created_at
		FROM circles WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Color, &c.OwnerDeviceID, &c.InviteCode, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("circle by id: %w", err)
	}
	return c, nil
}

// CircleByInvite fetches a circle by invite code.
func (s *Store) CircleByInvite(code string) (*Circle, error) {
	c := &Circle{}
	err := s.db.QueryRow(`SELECT c.id, c.name, c.color, c.owner_device_id, c.invite_code, c.created_at
		FROM circles c JOIN invites i ON i.circle_id = c.id
		WHERE i.code = ? AND i.expires_at > ?`, code, nowMS()).
		Scan(&c.ID, &c.Name, &c.Color, &c.OwnerDeviceID, &c.InviteCode, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("circle by invite: %w", err)
	}
	return c, nil
}

// CirclesForDevice lists circles the device belongs to.
func (s *Store) CirclesForDevice(deviceID string) ([]Circle, error) {
	rows, err := s.db.Query(`SELECT c.id, c.name, c.color, c.owner_device_id, c.invite_code, c.created_at
		FROM circles c JOIN circle_members m ON m.circle_id = c.id
		WHERE m.device_id = ? ORDER BY c.created_at`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("circles for device: %w", err)
	}
	defer rows.Close()

	var out []Circle
	for rows.Next() {
		var c Circle
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &c.OwnerDeviceID, &c.InviteCode, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan circle: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddMember joins a device to a circle. Idempotent for existing members;
// rejects joins beyond the circle's member cap. The cap check is part of
// the INSERT itself, so two concurrent joins cannot both pass a count-then-
// insert race and overflow the circle.
func (s *Store) AddMember(circleID, deviceID, avatarColor string) error {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO circle_members (circle_id, device_id, role, avatar_color, sharing_enabled, joined_at)
		SELECT ?, ?, 'member', ?, 1, ?
		WHERE (SELECT COUNT(*) FROM circle_members WHERE circle_id = ? AND device_id != ?) < ?`,
		circleID, deviceID, avatarColor, nowMS(), circleID, deviceID, MaxMembers)
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either the member already exists (idempotent re-join) or the
		// circle is full — distinguish before rejecting.
		var one int
		if err := s.db.QueryRow(`SELECT 1 FROM circle_members WHERE circle_id = ? AND device_id = ?`,
			circleID, deviceID).Scan(&one); err == nil {
			return nil
		}
		return ErrCircleFull
	}
	return nil
}

// OldestMember returns the device id of the longest-standing member of a
// circle (first joined), or "" if the circle has no members left.
func (s *Store) OldestMember(circleID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT device_id FROM circle_members WHERE circle_id = ? ORDER BY joined_at LIMIT 1`,
		circleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("oldest member: %w", err)
	}
	return id, nil
}

// TransferOwnership promotes a member to owner.
func (s *Store) TransferOwnership(circleID, deviceID string) error {
	_, err := s.db.Exec(`UPDATE circle_members SET role = 'owner' WHERE circle_id = ? AND device_id = ?`,
		circleID, deviceID)
	if err != nil {
		return fmt.Errorf("transfer ownership: %w", err)
	}
	return nil
}

// RemoveMember removes a device from a circle. Their circle-key blob is
// deleted too: a removed member must not have a blob sitting in the DB that
// a later re-join (or a DB compromise) could resurrect.
func (s *Store) RemoveMember(circleID, deviceID string) error {
	res, err := s.db.Exec(`DELETE FROM circle_members WHERE circle_id = ? AND device_id = ?`, circleID, deviceID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(`DELETE FROM key_blobs WHERE circle_id = ? AND device_id = ?`, circleID, deviceID)
	if err != nil {
		return fmt.Errorf("remove key blob: %w", err)
	}
	return nil
}

// IsMember reports whether deviceID belongs to circleID.
func (s *Store) IsMember(circleID, deviceID string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM circle_members WHERE circle_id = ? AND device_id = ?`, circleID, deviceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is member: %w", err)
	}
	return true, nil
}

// MemberRole returns the member's role ("owner"/"member") or an error if
// they are not in the circle. Single-query replacement for IsMember+Members.
func (s *Store) MemberRole(circleID, deviceID string) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM circle_members WHERE circle_id = ? AND device_id = ?`,
		circleID, deviceID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}

// Members lists all members of a circle with device public keys.
func (s *Store) Members(circleID string) ([]Member, error) {
	rows, err := s.db.Query(`SELECT m.circle_id, m.device_id, m.role, d.name, m.avatar_color,
		d.ed25519_pub, d.x25519_pub, m.sharing_enabled, m.joined_at
		FROM circle_members m JOIN devices d ON d.id = m.device_id
		WHERE m.circle_id = ? ORDER BY m.joined_at`, circleID)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.CircleID, &m.DeviceID, &m.Role, &m.DisplayName, &m.AvatarColor,
			&m.Ed25519Pub, &m.X25519Pub, &m.SharingEnabled, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetSharing toggles whether a member shares location.
func (s *Store) SetSharing(circleID, deviceID string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE circle_members SET sharing_enabled = ? WHERE circle_id = ? AND device_id = ?`,
		boolToInt(enabled), circleID, deviceID)
	if err != nil {
		return fmt.Errorf("set sharing: %w", err)
	}
	return nil
}

// CreateInvite inserts an invite code.
func (s *Store) CreateInvite(i Invite) error {
	_, err := s.db.Exec(`INSERT INTO invites (code, circle_id, created_by, expires_at) VALUES (?, ?, ?, ?)`,
		i.Code, i.CircleID, i.CreatedBy, i.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create invite: %w", err)
	}
	return nil
}

// PruneInvites deletes expired invite codes.
func (s *Store) PruneInvites(nowMS int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM invites WHERE expires_at < ?`, nowMS)
	if err != nil {
		return 0, fmt.Errorf("prune invites: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
