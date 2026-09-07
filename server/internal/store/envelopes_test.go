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
		_, err := s.AddEnvelope(Envelope{
			ID:         "env-" + string(rune('a'+i%26)) + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			CircleID:   "c1",
			DeviceID:   "d1",
			Kind:       "location",
			TS:         int64(1000 + i),
			Nonce:      "n",
			Ciphertext: "c",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// limit > 1000 is clamped to 1000, not to 100.
	envs, err := s.Envelopes("c1", 0, "", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 105 {
		t.Errorf("got %d envelopes, want 105 (limit must not shrink results below stored count)", len(envs))
	}
	// Explicit small limit works.
	envs, err = s.Envelopes("c1", 0, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 5 {
		t.Errorf("got %d envelopes, want 5", len(envs))
	}
	// since cursor excludes entries at or before the cursor (strict >).
	envs, err = s.Envelopes("c1", 1050, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 54 {
		t.Errorf("since cursor: got %d envelopes, want 54", len(envs))
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
	if _, err := s.AddEnvelope(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Envelopes("c1", 0, "message", 10)
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
