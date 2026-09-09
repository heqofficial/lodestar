package store

import (
	"fmt"
	"testing"
)

// BenchmarkAddEnvelope measures the envelope insert hot path — the write
// every location fix, message, and alert lands on. Run locally with
// `go test -bench=. -benchmem ./internal/store/`; CI smoke-runs it so the
// benchmark never silently rots.
func BenchmarkAddEnvelope(b *testing.B) {
	s := openTestStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.AddEnvelope(Envelope{
			ID:         fmt.Sprintf("env-%d", i),
			CircleID:   "c1",
			DeviceID:   "d1",
			Kind:       "location",
			TS:         int64(1000 + i),
			Nonce:      fmt.Sprintf("n-%d", i),
			Ciphertext: "c2lnaHQ=",
		}); err != nil {
			b.Fatal(err)
		}
	}
}
