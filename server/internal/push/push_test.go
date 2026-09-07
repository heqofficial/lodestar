package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
}
