package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSetAPNsToken(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateDevice(Device{ID: "d1", Name: "A", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPNsToken("d1", "tok-1"); err != nil {
		t.Fatal(err)
	}
	d, err := s.DeviceByID("d1")
	if err != nil {
		t.Fatal(err)
	}
	if d.APNsToken != "tok-1" {
		t.Errorf("apns_token = %q, want tok-1", d.APNsToken)
	}
	// Empty clears.
	if err := s.SetAPNsToken("d1", ""); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DeviceByID("d1")
	if d.APNsToken != "" {
		t.Errorf("apns_token = %q after clear, want empty", d.APNsToken)
	}
}

func TestAPNsTargets(t *testing.T) {
	s := openTestStore(t)
	for _, d := range []Device{
		{ID: "d1", Name: "A", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h1", APNsToken: "tok-1"},
		{ID: "d2", Name: "B", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h2", APNsToken: "tok-2"},
		{ID: "d3", Name: "C", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h3"}, // no token
	} {
		if err := s.CreateDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	now := nowMS()
	if err := s.CreateCircle(Circle{ID: "c1", Name: "C", Color: "#fff", OwnerDeviceID: "d1", InviteCode: "inv1", CreatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, role string }{
		{"d1", "owner"},
		{"d2", "member"},
		{"d3", "member"},
	} {
		if err := s.AddMember("c1", m.id, ""); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := s.APNsTargets("c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2 (tokenless d3 excluded)", len(targets))
	}
	if targets[0].DeviceID == "d3" || targets[1].DeviceID == "d3" {
		t.Error("tokenless member appeared in targets")
	}
	// Members of other circles are not targeted.
	if err := s.CreateCircle(Circle{ID: "c2", Name: "D", Color: "#000", OwnerDeviceID: "d1", InviteCode: "inv2", CreatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember("c2", "d2", ""); err != nil {
		t.Fatal(err)
	}
	targets, _ = s.APNsTargets("c2")
	// c2 has d1 (owner) and d2 — but never d3, who belongs to c1 only.
	if len(targets) != 2 {
		t.Errorf("c2 targets = %+v, want d1 and d2", targets)
	}
	for _, tg := range targets {
		if tg.DeviceID == "d3" {
			t.Error("c1-only member leaked into c2 targets")
		}
	}
}

func TestPruneDevices(t *testing.T) {
	s := openTestStore(t)
	old := nowMS() - 200*24*3600_000 // ~200 days ago
	recent := nowMS()
	devices := []Device{
		{ID: "d1", Name: "abandoned", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h1", CreatedAt: old},
		{ID: "d2", Name: "member", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h2", CreatedAt: old},
		{ID: "d3", Name: "fresh", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h3", CreatedAt: recent},
	}
	for _, d := range devices {
		if err := s.CreateDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateCircle(Circle{ID: "c1", Name: "C", Color: "#fff", OwnerDeviceID: "d2", InviteCode: "inv1", CreatedAt: nowMS()}, ""); err != nil {
		t.Fatal(err)
	}
	// d2 has a membership; d1 and d3 have none.
	n, err := s.PruneDevices(nowMS() - 180*24*3600_000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d devices, want 1 (only the old membership-less d1)", n)
	}
	if _, err := s.DeviceByID("d1"); err == nil {
		t.Error("abandoned device d1 survived the prune")
	}
	for _, keep := range []string{"d2", "d3"} {
		if _, err := s.DeviceByID(keep); err != nil {
			t.Errorf("device %s should be kept: %v", keep, err)
		}
	}
}

// TestMigrationAddsAPNsColumn proves a database created before the apns_token
// column existed opens cleanly and gains the column.
func TestMigrationAddsAPNsColumn(t *testing.T) {
	dsn := t.TempDir() + "/old.db"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE devices (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL,
		ed25519_pub   TEXT NOT NULL,
		x25519_pub    TEXT NOT NULL,
		token_hash    TEXT NOT NULL UNIQUE,
		created_at    INTEGER NOT NULL
	);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("old database failed to migrate: %v", err)
	}
	defer s.Close()
	if err := s.CreateDevice(Device{ID: "d1", Name: "A", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h"}); err != nil {
		t.Fatalf("insert into migrated db: %v", err)
	}
	if err := s.SetAPNsToken("d1", "tok-1"); err != nil {
		t.Fatalf("set token on migrated db: %v", err)
	}
	d, err := s.DeviceByID("d1")
	if err != nil {
		t.Fatal(err)
	}
	if d.APNsToken != "tok-1" {
		t.Errorf("apns_token = %q after migration, want tok-1", d.APNsToken)
	}
}
