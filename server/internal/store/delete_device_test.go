package store

import (
	"testing"
)

func TestDeleteDeviceRemovesEverything(t *testing.T) {
	s := openTestStore(t)
	alice := Device{ID: "d-alice", Name: "Alice", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h-alice", CreatedAt: nowMS()}
	bob := Device{ID: "d-bob", Name: "Bob", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h-bob", CreatedAt: nowMS()}
	for _, d := range []Device{alice, bob} {
		if err := s.CreateDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	c := Circle{ID: "c1", Name: "Fam", Color: "#fff", OwnerDeviceID: alice.ID, InviteCode: "INV1", CreatedAt: nowMS()}
	if err := s.CreateCircle(c, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember("c1", bob.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKeyBlob("c1", alice.ID, "blob-a"); err != nil {
		t.Fatal(err)
	}

	// Alice (owner) deletes herself: Bob must be promoted and keep the circle.
	if err := s.DeleteDevice(alice.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.DeviceByID(alice.ID); err != ErrNotFound {
		t.Errorf("device row: err=%v, want ErrNotFound", err)
	}
	if _, err := s.DeviceByTokenHash("h-alice"); err != ErrNotFound {
		t.Errorf("token lookup: err=%v, want ErrNotFound", err)
	}
	isMember, err := s.IsMember("c1", alice.ID)
	if err != nil || isMember {
		t.Errorf("membership still present: %v (%v)", isMember, err)
	}
	got, err := s.CircleByID("c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerDeviceID != bob.ID {
		t.Errorf("owner = %q, want %q (successor promoted)", got.OwnerDeviceID, bob.ID)
	}
	role, err := s.MemberRole("c1", bob.ID)
	if err != nil || role != "owner" {
		t.Errorf("bob role = %q (%v), want owner", role, err)
	}
}

func TestDeleteDeviceLastMemberLeavesCircleOrphanless(t *testing.T) {
	s := openTestStore(t)
	alice := Device{ID: "d-alice", Name: "Alice", Ed25519Pub: "e", X25519Pub: "x", TokenHash: "h1", CreatedAt: nowMS()}
	if err := s.CreateDevice(alice); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCircle(Circle{ID: "c1", Name: "Fam", Color: "#fff", OwnerDeviceID: alice.ID, InviteCode: "INV1", CreatedAt: nowMS()}, ""); err != nil {
		t.Fatal(err)
	}
	// Sole member leaves: the circle row stays (harmless, prunable), no panic.
	if err := s.DeleteDevice(alice.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
