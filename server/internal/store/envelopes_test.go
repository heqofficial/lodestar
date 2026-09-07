package store

import (
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEnvelopeLimitClamp(t *testing.T) {
	s := openTestStore(t)
	for i := 0; i < 105; i++ {
		_, inserted, err := s.AddEnvelope(Envelope{
			ID:         "env-" + string(rune('a'+i%26)) + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			CircleID:   "c1",
			DeviceID:   "d1",
			Kind:       "location",
			TS:         int64(1000 + i),
			Nonce:      "n-" + string(rune('a'+i%26)) + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			Ciphertext: "c",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !inserted {
			t.Fatalf("envelope %d reported as duplicate", i)
		}
	}
	// limit > 1000 is clamped to 1000, not to 100.
	envs, err := s.Envelopes("c1", 0, "", "", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 105 {
		t.Errorf("got %d envelopes, want 105 (limit must not shrink results below stored count)", len(envs))
	}
	// Explicit small limit works.
	envs, err = s.Envelopes("c1", 0, "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 5 {
		t.Errorf("got %d envelopes, want 5", len(envs))
	}
	// since cursor excludes entries at or before the cursor (strict >).
	envs, err = s.Envelopes("c1", 1050, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 54 {
		t.Errorf("since cursor: got %d envelopes, want 54", len(envs))
	}
}

func TestEnvelopeDedup(t *testing.T) {
	s := openTestStore(t)
	base := Envelope{
		ID:         "e1",
		CircleID:   "c1",
		DeviceID:   "d1",
		Kind:       "location",
		TS:         1000,
		Nonce:      "same-nonce",
		Ciphertext: "same-cipher",
	}
	if _, inserted, err := s.AddEnvelope(base); err != nil || !inserted {
		t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
	}
	// Exact replay (same device+nonce, different id — a retry after a lost
	// response) must not duplicate the row.
	base.ID = "e2"
	if _, inserted, err := s.AddEnvelope(base); err != nil || inserted {
		t.Fatalf("duplicate insert: inserted=%v err=%v (want ignored)", inserted, err)
	}
	envs, err := s.Envelopes("c1", 0, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Errorf("got %d rows, want 1 (replay must be idempotent)", len(envs))
	}
	// Same nonce from a different device is NOT a duplicate.
	base.ID, base.DeviceID = "e3", "d2"
	if _, inserted, err := s.AddEnvelope(base); err != nil || !inserted {
		t.Fatalf("same nonce, other device: inserted=%v err=%v", inserted, err)
	}
}

func TestPruneRetention(t *testing.T) {
	s := openTestStore(t)
	old := int64(1_000_000)
	now := int64(9_000_000_000)
	envs := []Envelope{
		{ID: "a", CircleID: "c1", DeviceID: "d1", Kind: "location", TS: old, Nonce: "n1", Ciphertext: "c"},
		{ID: "b", CircleID: "c1", DeviceID: "d1", Kind: "message", TS: old, Nonce: "n2", Ciphertext: "c"},
		{ID: "c", CircleID: "c1", DeviceID: "d1", Kind: "sos", TS: old, Nonce: "n3", Ciphertext: "c"},
		{ID: "d", CircleID: "c1", DeviceID: "d1", Kind: "crash", TS: old, Nonce: "n4", Ciphertext: "c"},
		{ID: "e", CircleID: "c1", DeviceID: "d1", Kind: "location", TS: now, Nonce: "n5", Ciphertext: "c"},
	}
	for _, e := range envs {
		if _, _, err := s.AddEnvelope(e); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneEnvelopes(old + 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want 2 (location+message; sos/crash kept)", n)
	}
	left, err := s.Envelopes("c1", 0, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 3 {
		t.Errorf("remaining %d, want 3 (sos, crash, fresh location)", len(left))
	}
	for _, e := range left {
		if e.ID == "a" || e.ID == "b" {
			t.Errorf("old %s envelope survived pruning", e.Kind)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	s := openTestStore(t)
	want := Envelope{
		ID:         "abc123",
		CircleID:   "c1",
		DeviceID:   "d1",
		Kind:       "message",
		TS:         123456,
		Nonce:      "nonce-value",
		Ciphertext: "cipher-value",
	}
	if _, _, err := s.AddEnvelope(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Envelopes("c1", 0, "message", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d envelopes", len(got))
	}
	if got[0].Ciphertext != want.Ciphertext || got[0].Nonce != want.Nonce || got[0].Kind != want.Kind {
		t.Errorf("round trip mismatch: %+v", got[0])
	}
}

func TestEnvelopeDeviceFilter(t *testing.T) {
	s := openTestStore(t)
	for _, d := range []string{"d1", "d2"} {
		for i := 0; i < 3; i++ {
			if _, _, err := s.AddEnvelope(Envelope{
				ID:         "e-" + d + "-" + string(rune('0'+i)),
				CircleID:   "c1",
				DeviceID:   d,
				Kind:       "location",
				TS:         int64(100 + i),
				Nonce:      "n-" + d + string(rune('0'+i)),
				Ciphertext: "c",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	envs, err := s.Envelopes("c1", 0, "", "d2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 3 {
		t.Fatalf("got %d envelopes, want 3 (device filter)", len(envs))
	}
	for _, e := range envs {
		if e.DeviceID != "d2" {
			t.Errorf("device filter leaked envelope from %s", e.DeviceID)
		}
	}
}

func TestOwnershipTransfer(t *testing.T) {
	s := openTestStore(t)
	c := Circle{ID: "c1", Name: "t", Color: "#fff", OwnerDeviceID: "owner", InviteCode: "AAAAAA", CreatedAt: 1}
	if err := s.CreateCircle(c, ""); err != nil {
		t.Fatal(err)
	}
	// Join order defines the successor: d1 joins before d2.
	if err := s.AddMember("c1", "d1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember("c1", "d2", ""); err != nil {
		t.Fatal(err)
	}
	// Owner leaves, then the oldest remaining member inherits.
	if err := s.RemoveMember("c1", "owner"); err != nil {
		t.Fatal(err)
	}
	next, err := s.OldestMember("c1")
	if err != nil || next != "d1" {
		t.Fatalf("oldest member = %q, err %v", next, err)
	}
	if err := s.TransferOwnership("c1", next); err != nil {
		t.Fatal(err)
	}
	role, err := s.MemberRole("c1", "d1")
	if err != nil || role != "owner" {
		t.Errorf("role = %q, err %v; want owner", role, err)
	}
	// Empty circle: no successor, no error.
	if err := s.RemoveMember("c1", "d1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember("c1", "d2"); err != nil {
		t.Fatal(err)
	}
	if next, err := s.OldestMember("c1"); err != nil || next != "" {
		t.Errorf("empty circle: next = %q, err %v", next, err)
	}
}

func TestCircleKeyBlobRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.PutKeyBlob("c1", "d1", "sealed-key"); err != nil {
		t.Fatal(err)
	}
	got, err := s.KeyBlob("c1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sealed-key" {
		t.Errorf("got %q", got)
	}
	// Overwrite works.
	if err := s.PutKeyBlob("c1", "d1", "rotated-key"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KeyBlob("c1", "d1")
	if got != "rotated-key" {
		t.Errorf("got %q, want rotated-key", got)
	}
	if _, err := s.KeyBlob("c1", "nobody"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAddMemberCap(t *testing.T) {
	s := openTestStore(t)
	c := Circle{ID: "c1", Name: "full", Color: "#fff", OwnerDeviceID: "owner", InviteCode: "AAAAAA", CreatedAt: 1}
	if err := s.CreateCircle(c, ""); err != nil {
		t.Fatal(err)
	}
	// Owner already counts toward the cap, so fill up to MaxMembers total.
	for i := 0; i < MaxMembers-1; i++ {
		if err := s.AddMember("c1", "d"+string(rune('a'+i%26))+string(rune('0'+i/10))+string(rune('0'+i%10)), ""); err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
	}
	// Cap reached: new member rejected.
	if err := s.AddMember("c1", "outsider", ""); err != ErrCircleFull {
		t.Errorf("got %v, want ErrCircleFull", err)
	}
	// Existing members may still re-join (idempotent).
	if err := s.AddMember("c1", "da00", ""); err != nil {
		t.Errorf("rejoin at cap: %v", err)
	}
}
