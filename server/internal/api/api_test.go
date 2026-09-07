package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/heqofficial/lodestar/server/internal/push"
	"github.com/heqofficial/lodestar/server/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, push.NewMulti(nil), "", []string{"sos", "geofence"})
	return s, st
}

func doJSON(t *testing.T, s *Server, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	out := map[string]any{}
	if resp.Body != nil {
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &out)
	}
	return resp, out
}

func register(t *testing.T, s *Server, name string) (deviceID, token string) {
	t.Helper()
	resp, out := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
		"name":        name,
		"ed25519_pub": "edpub-" + name,
		"x25519_pub":  "xpub-" + name,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", name, resp.StatusCode)
	}
	dev := out["device"].(map[string]any)
	return dev["id"].(string), out["token"].(string)
}

func createCircle(t *testing.T, s *Server, token, name string) string {
	t.Helper()
	resp, out := doJSON(t, s, "POST", "/api/v1/circles", token, map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create circle: status %d", resp.StatusCode)
	}
	return out["id"].(string)
}

func postEnvelope(t *testing.T, s *Server, token, circleID, kind string) {
	t.Helper()
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", token, map[string]any{
		"kind": kind, "ts": time.Now().UnixMilli(),
		"nonce": "AB12", "ciphertext": "c2lnaHQ=",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post envelope: status %d", resp.StatusCode)
	}
}

func TestFullCircleLifecycle(t *testing.T) {
	s, _ := newTestServer(t)

	// Register two devices.
	aliceID, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")

	// Alice creates a circle and gets an invite code.
	resp, out := doJSON(t, s, "POST", "/api/v1/circles", aliceTok, map[string]any{"name": "The Nelsons"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	circleID := out["id"].(string)
	code := out["invite_code"].(string)

	// Bob joins with the code.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join: %d", resp.StatusCode)
	}

	// Membership checks.
	resp, out = doJSON(t, s, "GET", "/api/v1/circles/"+circleID, aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d", resp.StatusCode)
	}
	members := out["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	if aliceID == "" {
		t.Fatal("alice id empty")
	}

	// Bob posts envelopes of several kinds.
	postEnvelope(t, s, bobTok, circleID, "location")
	postEnvelope(t, s, bobTok, circleID, "checkin")
	postEnvelope(t, s, bobTok, circleID, "sos")

	resp, out = doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/envelopes?limit=10", aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list envelopes: %d", resp.StatusCode)
	}
	envs := out["envelopes"].([]any)
	if len(envs) != 3 {
		t.Fatalf("envelopes = %d, want 3", len(envs))
	}
	first := envs[0].(map[string]any)
	if first["ciphertext"] != "c2lnaHQ=" {
		t.Errorf("ciphertext mangled: %v", first["ciphertext"])
	}
	if first["kind"] != "sos" {
		t.Errorf("expected sos newest, got %v", first["kind"])
	}

	// Latest-per-device endpoint.
	resp, out = doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/envelopes/latest", aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("latest: %d", resp.StatusCode)
	}
	if n := len(out["envelopes"].([]any)); n != 3 {
		t.Errorf("latest envelopes = %d, want 3", n)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	circleID := createCircle(t, s, aliceTok, "C")

	// Unknown kind rejected.
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", aliceTok, map[string]any{
		"kind": "hax", "ts": time.Now().UnixMilli(), "nonce": "n", "ciphertext": "c",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown kind: got %d, want 400", resp.StatusCode)
	}
	// Future timestamp rejected.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", aliceTok, map[string]any{
		"kind": "location", "ts": time.Now().Add(2 * time.Hour).UnixMilli(), "nonce": "n", "ciphertext": "c",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("future ts: got %d, want 400", resp.StatusCode)
	}
	// Oversized ciphertext rejected.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", aliceTok, map[string]any{
		"kind": "location", "ts": time.Now().UnixMilli(), "nonce": "n",
		"ciphertext": string(bytes.Repeat([]byte("x"), maxEnvelopeSize+1)),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversize: got %d, want 400", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	s, _ := newTestServer(t)
	paths := []string{
		"GET /api/v1/circles",
		"POST /api/v1/circles",
		"GET /api/v1/circles/abc/envelopes",
		"POST /api/v1/circles/abc/envelopes",
		"GET /api/v1/ws?circle=abc",
	}
	for _, p := range paths {
		method, path, _ := strings.Cut(p, " ")
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", p, rec.Code)
		}
	}
}

func TestNonMemberForbidden(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")
	// Bob never joins — must be forbidden everywhere.
	resp, _ := doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/envelopes", bobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-member read: got %d, want 403", resp.StatusCode)
	}
	postEnvelopeReq := map[string]any{"kind": "location", "ts": time.Now().UnixMilli(), "nonce": "n", "ciphertext": "c"}
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", bobTok, postEnvelopeReq)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-member write: got %d, want 403", resp.StatusCode)
	}
}

func TestKeyBlobs(t *testing.T) {
	s, _ := newTestServer(t)
	aliceID, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")

	// Alice stores a key blob addressed to herself.
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", aliceTok, map[string]any{
		"for_device": aliceID, "ciphertext": "blob1",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put key blob: %d", resp.StatusCode)
	}
	// Alice reads it back.
	resp, out := doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/keys/mine", aliceTok, nil)
	if resp.StatusCode != http.StatusOK || out["ciphertext"] != "blob1" {
		t.Fatalf("get key blob: %d %v", resp.StatusCode, out)
	}
	// Bob is not a member: forbidden.
	resp, _ = doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/keys/mine", bobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob key read: got %d, want 403", resp.StatusCode)
	}
}

func TestInviteAndSharing(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	circleID := createCircle(t, s, aliceTok, "C")

	// Owner creates an invite.
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/invites", aliceTok, map[string]any{"ttl_hours": 1})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: %d", resp.StatusCode)
	}
	if out["code"] == "" {
		t.Fatal("empty invite code")
	}

	// Changing someone else's sharing state is forbidden (id "someone" != alice).
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/members/someone/sharing", aliceTok, map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other's sharing: got %d, want 403", resp.StatusCode)
	}
}

func TestWebSocketFanout(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")
	// Bob joins.
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d (%v)", resp.StatusCode, out)
	}

	// Run a real HTTP server so the WebSocket dial works.
	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID

	conn, httpResp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + bobTok}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v (http %d)", err, httpResp.StatusCode)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	time.Sleep(100 * time.Millisecond) // let the subscription land
	postEnvelope(t, s, aliceTok, circleID, "sos")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got map[string]any
	if err := wsjson.Read(ctx, conn, &got); err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if got["kind"] != "sos" {
		t.Errorf("ws envelope kind = %v, want sos", got["kind"])
	}
}

func mustInvite(t *testing.T, s *Server, token, circleID string) string {
	t.Helper()
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/invites", token, map[string]any{"ttl_hours": 24})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invite: %d", resp.StatusCode)
	}
	return out["code"].(string)
}

func TestAdminPageAndHealthz(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/admin", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Lodestar")) {
		t.Error("admin page missing content")
	}
}

func TestRateLimiting(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Spammer")
	status := http.StatusOK
	for i := 0; i < 300; i++ {
		resp, _ := doJSON(t, s, "GET", "/api/v1/me", tok, nil)
		status = resp.StatusCode
		if status != http.StatusOK {
			break
		}
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("expected rate limit, last status %d", status)
	}
}
