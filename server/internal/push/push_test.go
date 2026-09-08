package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNtfySend(t *testing.T) {
	var gotPath, gotTitle, gotAuth, gotBody string
	var gotPriority string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("Title")
		gotPriority = r.Header.Get("Priority")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL+"/", "secret-token")
	err := n.Send(context.Background(), Request{
		Title:    "🚨 SOS",
		Priority: 5,
		Payload:  []byte(`{"ciphertext":"abc"}`),
		Topic:    "circle-1",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/circle-1" {
		t.Errorf("path = %q, want /circle-1", gotPath)
	}
	if gotTitle != "🚨 SOS" {
		t.Errorf("title = %q", gotTitle)
	}
	if gotPriority != "5" {
		t.Errorf("priority = %q", gotPriority)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody != `{"ciphertext":"abc"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestNtfyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL, "")
	if err := n.Send(context.Background(), Request{Topic: "t", Payload: []byte("{}")}); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestJWTEncodeAndVerify(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwtEncode(
		map[string]any{"alg": "ES256", "kid": "KID1"},
		map[string]any{"iss": "TEAM", "iat": int64(1700000000)},
		key,
	)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	// Header decodes and is valid JSON.
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]string
	if err := json.Unmarshal(h, &header); err != nil {
		t.Fatal(err)
	}
	if header["kid"] != "KID1" {
		t.Errorf("kid = %q", header["kid"])
	}
	// Two tokens with different keys must differ.
	key2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok2, _ := jwtEncode(map[string]any{"alg": "ES256"}, map[string]any{"iss": "TEAM"}, key2)
	if tok == tok2 {
		t.Error("tokens should differ")
	}
}

func TestNewAPNsValidation(t *testing.T) {
	if _, err := NewAPNs(APNsConfig{}); err == nil {
		t.Fatal("expected error for empty config")
	}
}

func TestMultiDisabled(t *testing.T) {
	m := NewMulti(nil)
	if m.Enabled() {
		t.Fatal("empty multi should be disabled")
	}
	if m.NtfyEnabled() || m.APNsEnabled() {
		t.Fatal("empty multi reports providers as enabled")
	}
}

// TestMultiPerProviderFlags: the admin stats must distinguish which
// providers are actually configured — reporting APNs as live just because
// ntfy is configured (or vice versa) is a lie on the dashboard.
func TestMultiPerProviderFlags(t *testing.T) {
	ntfy := NewMulti(NewNtfy("http://localhost", ""))
	if !ntfy.Enabled() || !ntfy.NtfyEnabled() || ntfy.APNsEnabled() {
		t.Errorf("ntfy-only multi flags: enabled=%v ntfy=%v apns=%v",
			ntfy.Enabled(), ntfy.NtfyEnabled(), ntfy.APNsEnabled())
	}
	t.Run("nil entries are skipped", func(t *testing.T) {
		m := NewMulti(nil, nil)
		if m.Enabled() {
			t.Fatal("nil-only multi should be disabled")
		}
	})
}

func TestNtfySkipsPerDeviceRequests(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := NewNtfy(srv.URL, "")
	// A per-device APNs request (no topic) must not hit ntfy at all.
	if err := n.Send(context.Background(), Request{DeviceToken: "tok"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if hit {
		t.Fatal("ntfy was contacted for a per-device request")
	}
}

func TestAPNsPayload(t *testing.T) {
	p := apnsPayload("🚨 SOS from Alice")
	if len(p) > 4096 {
		t.Errorf("payload %d bytes exceeds APNs 4 KiB cap", len(p))
	}
	var j struct {
		Aps struct {
			Alert struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			} `json:"alert"`
			Sound string `json:"sound"`
		} `json:"aps"`
	}
	if err := json.Unmarshal(p, &j); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if j.Aps.Alert.Title != "🚨 SOS from Alice" {
		t.Errorf("alert title = %q", j.Aps.Alert.Title)
	}
	if j.Aps.Alert.Body == "" || j.Aps.Sound == "" {
		t.Errorf("alert body/sound missing: %s", p)
	}
	// The payload must never embed ciphertext — it is a wake-up signal only.
	if strings.Contains(string(p), "ciphertext") {
		t.Error("payload embeds envelope data; APNs cap would be exceeded for large envelopes")
	}
}

func TestAPNsErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{200, `{}`, nil},
		{410, `{"reason":"Unregistered"}`, ErrUnregistered},
		{400, `{"reason":"BadDeviceToken"}`, ErrUnregistered},
		{403, `{"reason":"BadDeviceToken"}`, ErrUnregistered},
		{400, `{"reason":"BadTopic"}`, errors.New("generic")}, // config error, not a dead token
		{503, `{}`, errors.New("generic")},
	}
	for _, c := range cases {
		err := apnsError(c.status, []byte(c.body))
		switch {
		case c.want == nil && err != nil:
			t.Errorf("status %d: err = %v, want nil", c.status, err)
		case c.want == ErrUnregistered && !errors.Is(err, ErrUnregistered):
			t.Errorf("status %d: err = %v, want ErrUnregistered", c.status, err)
		case c.want != nil && c.want != ErrUnregistered && err == nil:
			t.Errorf("status %d: err = nil, want generic error", c.status)
		}
	}
}
