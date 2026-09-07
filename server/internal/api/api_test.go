package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func newTestServer(t testing.TB) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, push.NewMulti(nil), "", []string{"sos", "geofence"})
	return s, st
}

func doJSON(t testing.TB, s *Server, method, path, token string, body any) (*http.Response, map[string]any) {
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

func register(t testing.TB, s *Server, name string) (deviceID, token string) {
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

func createCircle(t testing.TB, s *Server, token, name string) string {
	t.Helper()
	resp, out := doJSON(t, s, "POST", "/api/v1/circles", token, map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create circle: status %d", resp.StatusCode)
	}
	return out["id"].(string)
}

func postEnvelope(t testing.TB, s *Server, token, circleID, kind string) {
	t.Helper()
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", token, map[string]any{
		"kind": kind, "ts": time.Now().UnixMilli(),
		"nonce": fmt.Sprintf("n-%s-%d", kind, time.Now().UnixNano()%1e9), "ciphertext": "c2lnaHQ=",
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

func mustInvite(t testing.TB, s *Server, token, circleID string) string {
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

func TestTripAndCrashKindsAccepted(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "C")

	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
		"kind": "trip", "ts": time.Now().UnixMilli(), "nonce": "n", "ciphertext": "c2ln",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("trip kind: got %d, want 201", resp.StatusCode)
	}
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
		"kind": "crash", "ts": time.Now().UnixMilli(), "nonce": "n", "ciphertext": "c2ln",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("crash kind: got %d, want 201", resp.StatusCode)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	s, _ := newTestServer(t)
	limited := false
	for i := 0; i < 20; i++ {
		resp, _ := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
			"name": "Spam", "ed25519_pub": "e", "x25519_pub": "x",
		})
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("expected register endpoint to rate limit")
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

// --- hostile-input tests ---------------------------------------------------

func TestKeyBlobForNonMemberRejected(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")

	// Bob is not a member: he must not be able to write a key blob at all.
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", bobTok, map[string]any{
		"for_device": "alice-device-id", "ciphertext": "evil",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-member key write: got %d, want 403", resp.StatusCode)
	}

	// Alice is a member but may only address blobs to actual members.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", aliceTok, map[string]any{
		"for_device": "not-a-member", "ciphertext": "evil",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("key write to non-member: got %d, want 403", resp.StatusCode)
	}
	// ...but she may address the owner's own blob (herself).
	aliceID, _ := s.store.Members(circleID)
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", aliceTok, map[string]any{
		"for_device": aliceID[0].DeviceID, "ciphertext": "legit",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("legit key write: got %d, want 204", resp.StatusCode)
	}
}

func TestKeyWriteOwnerOnly(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")
	// Bob joins as a plain member.
	if resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d (%v)", resp.StatusCode, out)
	}
	// Bob (member, not owner) must not be able to write a key blob — even
	// for himself. The owner is the only holder of the circle key, so any
	// member-writable blob would be garbage or a poisoning vector.
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", bobTok, map[string]any{
		"for_device": bobID(t, s, circleID, "Bob"), "ciphertext": "evil",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("member key write: got %d, want 403", resp.StatusCode)
	}
	// The owner can still write a blob for Bob.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/keys", aliceTok, map[string]any{
		"for_device": bobID(t, s, circleID, "Bob"), "ciphertext": "legit",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("owner key write: got %d, want 204", resp.StatusCode)
	}
}

func TestOwnerLeaveTransfersOwnership(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	_, carolTok := register(t, s, "Carol")
	circleID := createCircle(t, s, aliceTok, "C")
	if resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d (%v)", resp.StatusCode, out)
	}
	if resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", carolTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("carol join: %d (%v)", resp.StatusCode, out)
	}

	// Alice (owner) leaves; Bob joined first and must inherit.
	resp, _ := doJSON(t, s, "DELETE", "/api/v1/circles/"+circleID+"/members/"+bobID(t, s, circleID, "Alice"), aliceTok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice leave: %d", resp.StatusCode)
	}

	// The circle must not be orphaned: Bob (new owner) can create invites.
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/invites", bobTok, map[string]any{"ttl_hours": 1})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("successor invite: got %d (%v), want 201 — circle is orphaned", resp.StatusCode, out)
	}
}

func TestLocationKindThrottle(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Spammer")
	circleID := createCircle(t, s, tok, "C")
	limited := false
	for i := 0; i < 40; i++ {
		resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
			"kind": "location", "ts": time.Now().UnixMilli(),
			"nonce": fmt.Sprintf("n-%d", i), "ciphertext": "c2lnaHQ=",
		})
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("expected location kind throttle to kick in")
	}
}

func TestEnvelopeReplayIdempotent(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "C")
	body := map[string]any{
		"kind": "checkin", "ts": time.Now().UnixMilli(),
		"nonce": "replay-nonce", "ciphertext": "c2lnaHQ=",
	}
	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first post: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("replay post: got %d, want 201 (idempotent)", resp.StatusCode)
	}
	resp, out := doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/envelopes", tok, nil)
	envs := out["envelopes"].([]any)
	if len(envs) != 1 {
		t.Errorf("after replay: %d rows, want 1", len(envs))
	}
	_ = resp
}

func TestRegisterSanitizesControlChars(t *testing.T) {
	s, _ := newTestServer(t)
	_, out := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
		"name": "Bad\nName\tDevice\r", "ed25519_pub": "e", "x25519_pub": "x",
	})
	if out["device"] == nil {
		t.Fatal("registration failed")
	}
	name := out["device"].(map[string]any)["name"].(string)
	if strings.ContainsAny(name, "\n\t\r") {
		t.Errorf("control characters survived sanitization: %q", name)
	}
	if name != "BadNameDevice" {
		t.Errorf("name = %q, want BadNameDevice", name)
	}
}

func TestPanicRecoveryKeepsServing(t *testing.T) {
	s, _ := newTestServer(t)
	// The recoverer wraps the real mux; prove a panicking handler is
	// converted to a 500 and the server survives.
	panicky := recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	panicky.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panic: got %d, want 500", rec.Code)
	}
	// Still serving normally afterwards.
	resp, _ := doJSON(t, s, "GET", "/healthz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz after panic: %d", resp.StatusCode)
	}
}

func TestAdminTokenRequired(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, push.NewMulti(nil), "s3cret", []string{"sos"})

	resp, _ := doJSON(t, s, "GET", "/admin", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", resp.StatusCode)
	}
	resp, _ = doJSON(t, s, "GET", "/admin?token=wrong", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", resp.StatusCode)
	}
	resp, _ = doJSON(t, s, "GET", "/admin?token=s3cret", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("right token: got %d, want 200", resp.StatusCode)
	}
}

func TestWebSocketKeepaliveArmsIdleDeadline(t *testing.T) {
	// Regression: coder/websocket's read deadline is fixed per Read call and
	// protocol pings do NOT reset it. Only a completed data message re-arms
	// the window, so the app must send a JSON keepalive. Prove it: with a
	// 2s window, a keepalive-sending socket stays alive while a silent one
	// is closed.
	old := wsIdleTimeout
	wsIdleTimeout = 2 * time.Second
	defer func() { wsIdleTimeout = old }()

	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	_, carolTok := register(t, s, "Carol")
	circleID := createCircle(t, s, aliceTok, "C")
	for _, tok := range []string{bobTok, carolTok} {
		if resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", tok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)}); resp.StatusCode != http.StatusOK {
			t.Fatalf("join: %d (%v)", resp.StatusCode, out)
		}
	}

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	dial := func(token string) *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID
		c, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
		})
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		return c
	}
	bob := dial(bobTok)
	defer bob.Close(websocket.StatusNormalClosure, "done")
	carol := dial(carolTok)
	defer carol.Close(websocket.StatusNormalClosure, "done")
	time.Sleep(100 * time.Millisecond) // let subscriptions land

	// Phase 1 — no traffic at all for ~2.5s (past the 2s idle window).
	// Bob sends data keepalives; Carol sends nothing. NB: keep the phase
	// traffic-free — broadcasts would complete Carol's reads too and
	// re-arm her deadline, hiding the bug this test guards against.
	for i := 0; i < 6; i++ {
		if err := wsjson.Write(context.Background(), bob, map[string]string{"type": "ping"}); err != nil {
			t.Fatalf("bob keepalive %d: %v", i, err)
		}
		time.Sleep(400 * time.Millisecond)
	}

	// Carol's silent socket must have been closed server-side.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got map[string]any
	if err := wsjson.Read(ctx, carol, &got); err == nil {
		t.Fatal("silent socket still alive after idle deadline")
	}

	// Bob's keepalives completed reads, so his deadline is still far out:
	// he must receive the post fine.
	postEnvelope(t, s, aliceTok, circleID, "sos")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := wsjson.Read(ctx2, bob, &got); err != nil {
		t.Fatalf("keepalive socket died despite pings: %v", err)
	}
	if got["kind"] != "sos" {
		t.Errorf("bob got %v, want sos", got["kind"])
	}
}

func TestRemoveMemberKicksWebSocket(t *testing.T) {
	s, _ := newTestServer(t)
	aliceID, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")
	if _, out := doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)}); out["id"] == nil {
		t.Fatal("bob join failed")
	}

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + bobTok}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	time.Sleep(100 * time.Millisecond) // let the subscription land

	// Alice (owner) removes Bob while his socket is open.
	resp, _ := doJSON(t, s, "DELETE", "/api/v1/circles/"+circleID+"/members/"+bobID(t, s, circleID, "Bob"), aliceTok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove bob: %d", resp.StatusCode)
	}
	_ = aliceID

	// Bob's socket must be closed server-side: reads now error out.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got map[string]any
	if err := wsjson.Read(ctx, conn, &got); err == nil {
		t.Error("removed member's socket still readable — revocation failed")
	}
}

func bobID(t *testing.T, s *Server, circleID, name string) string {
	t.Helper()
	members, err := s.store.Members(circleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.DisplayName == name {
			return m.DeviceID
		}
	}
	t.Fatalf("member %s not found", name)
	return "" // unreachable; Fatalf does not return
}

// Fuzz targets — run with `go test -fuzz=Fuzz -fuzztime=10s ./internal/api`.

func FuzzRegisterDevice(f *testing.F) {
	s, _ := newTestServer(f)
	f.Add("Alice", "edpub", "xpub")
	f.Fuzz(func(t *testing.T, name, edpub, xpub string) {
		resp, _ := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
			"name": name, "ed25519_pub": edpub, "x25519_pub": xpub,
		})
		// 201 (ok), 400 (invalid), 429 (rate limited) are all fine; 5xx is not.
		if resp.StatusCode >= 500 {
			t.Errorf("fuzz input %q → %d", name, resp.StatusCode)
		}
	})
}

func FuzzPostEnvelope(f *testing.F) {
	s, _ := newTestServer(f)
	_, tok := register(f, s, "Alice")
	circleID := createCircle(f, s, tok, "C")
	f.Add("location", "abc", int64(1234567890))
	f.Fuzz(func(t *testing.T, kind, nonce string, ts int64) {
		resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
			"kind": kind, "ts": ts, "nonce": nonce, "ciphertext": "c2lnaHQ=",
		})
		// Any of these is fine; 5xx is not.
		if resp.StatusCode >= 500 {
			t.Errorf("fuzz input %q %q %d → %d", kind, nonce, ts, resp.StatusCode)
		}
	})
}
