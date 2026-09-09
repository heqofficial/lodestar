package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
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

// testPubKey is canonical base64 of 32 zero bytes — a structurally valid
// Ed25519/X25519 public key for registration fixtures.
const testPubKey = "MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA="

func register(t testing.TB, s *Server, name string) (deviceID, token string) {
	t.Helper()
	resp, out := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
		"name":        name,
		"ed25519_pub": testPubKey,
		"x25519_pub":  testPubKey,
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
	// The version must be reported so deployments can verify the build.
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&health); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if health.Status != "ok" || health.Version == "" {
		t.Errorf("healthz = %+v, want status ok + non-empty version", health)
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
			"name": "Spam", "ed25519_pub": testPubKey, "x25519_pub": testPubKey,
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
	rejected := 0
	const posts = 50 // burst is 30; even with refill at the slowest realistic pace
	// (a few ms per request) the bucket empties well inside 50 posts.
	for i := 0; i < posts; i++ {
		resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
			"kind": "location", "ts": time.Now().UnixMilli(),
			"nonce": fmt.Sprintf("n-%d", i), "ciphertext": "c2lnaHQ=",
		})
		if resp.StatusCode == http.StatusTooManyRequests {
			rejected++
		}
	}
	if rejected == 0 {
		t.Error("expected location kind throttle to kick in (0/50 rejected)")
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
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first post: %d", resp.StatusCode)
	}
	firstID := out["id"].(string)
	resp, out = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("replay post: got %d, want 201 (idempotent)", resp.StatusCode)
	}
	// The retry must observe the SAME envelope id as the first post — a
	// fresh id would break client-side dedup (the retry would look like a
	// brand-new envelope) and diverge from the broadcast.
	if out["id"] != firstID {
		t.Errorf("replay returned id %v, want %v (must match the stored row)", out["id"], firstID)
	}
	resp, out = doJSON(t, s, "GET", "/api/v1/circles/"+circleID+"/envelopes", tok, nil)
	envs := out["envelopes"].([]any)
	if len(envs) != 1 {
		t.Errorf("after replay: %d rows, want 1", len(envs))
	}
	_ = resp
}

func TestEmptyCiphertextRejected(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "C")
	for _, body := range []map[string]any{
		{"kind": "location", "ts": time.Now().UnixMilli(), "nonce": "n1", "ciphertext": ""},
		{"kind": "location", "ts": time.Now().UnixMilli(), "nonce": "", "ciphertext": "c2lnaHQ="},
	} {
		resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("empty field: got %d, want 400", resp.StatusCode)
		}
	}
}

func TestWebSocketConnCap(t *testing.T) {
	s, _ := newTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	circleID := createCircle(t, s, aliceTok, "C")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID
	dial := func() (*websocket.Conn, int) {
		t.Helper()
		c, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + aliceTok}},
		})
		if err != nil {
			return nil, resp.StatusCode
		}
		return c, resp.StatusCode
	}

	c1, _ := dial()
	if c1 == nil {
		t.Fatal("first socket rejected")
	}
	defer c1.Close(websocket.StatusNormalClosure, "done")
	c2, _ := dial()
	if c2 == nil {
		t.Fatal("second socket rejected")
	}
	defer c2.Close(websocket.StatusNormalClosure, "done")

	// Third socket from the same device must be closed immediately by the
	// server: without this cap, one member could hold hundreds of sockets
	// and turn every broadcast into a memory-amplifying fan-out. The
	// upgrade itself succeeds (101), then the server closes with a policy
	// violation frame.
	c3, status := dial()
	if c3 == nil {
		t.Fatalf("third socket failed at handshake (status %d)", status)
	}
	defer c3.Close(websocket.StatusNormalClosure, "done")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var msg any
	err := wsjson.Read(ctx, c3, &msg)
	if err == nil {
		t.Fatal("third socket still open — per-device cap not enforced")
	}
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Errorf("third socket close status = %d, want 1008 (policy violation)", got)
	}
}

// recordedPush captures one push attempt for assertions.
type recordedPush struct {
	Title, Topic, DeviceToken string
}

// recordingSender captures push attempts so tests can assert what would
// have been sent to ntfy/APNs. Push fires from a server goroutine, so the
// recording must be synchronized (the race detector will flag it otherwise).
// unregister marks a device token that fails with ErrUnregistered, like a
// dead APNs token would.
type recordingSender struct {
	mu         sync.Mutex
	pushes     []recordedPush
	unregister string
}

func (r *recordingSender) Send(_ context.Context, req push.Request) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unregister != "" && req.DeviceToken == r.unregister {
		return push.ErrUnregistered
	}
	r.pushes = append(r.pushes, recordedPush{req.Title, req.Topic, req.DeviceToken})
	return nil
}

func (r *recordingSender) snapshot() []recordedPush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedPush(nil), r.pushes...)
}

// waitPushes polls until the recording sender has at least n attempts.
func waitPushes(t testing.TB, rec *recordingSender, n int) []recordedPush {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := rec.snapshot(); len(p) >= n {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pushes (got %d)", n, len(rec.snapshot()))
	return nil
}

func TestCrashPushTitle(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := &recordingSender{}
	// crash is in the default push kinds; the title map must cover it.
	s := New(st, push.NewMulti(rec), "", []string{"sos", "crash", "geofence"})
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "C")

	resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
		"kind": "crash", "ts": time.Now().UnixMilli(), "nonce": "n", "ciphertext": "c2ln",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("crash post: %d", resp.StatusCode)
	}
	pushes := waitPushes(t, rec, 1)
	if !strings.Contains(pushes[0].Title, "Alice") {
		t.Errorf("crash push title = %q, want it to name the member", pushes[0].Title)
	}
}

func TestRegisterStoresAPNsToken(t *testing.T) {
	s, st := newTestServer(t)
	resp, out := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
		"name": "Alice", "ed25519_pub": testPubKey, "x25519_pub": testPubKey, "apns_token": "tok-alice",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d", resp.StatusCode)
	}
	dev := out["device"].(map[string]any)
	id := dev["id"].(string)
	d, err := st.DeviceByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.APNsToken != "tok-alice" {
		t.Errorf("stored apns_token = %q, want tok-alice", d.APNsToken)
	}
	// The token must never be serialized to clients.
	if _, leaked := dev["apns_token"]; leaked {
		t.Error("apns_token leaked in register response")
	}
	resp, out = doJSON(t, s, "GET", "/api/v1/me", out["token"].(string), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: %d", resp.StatusCode)
	}
	if _, leaked := out["device"].(map[string]any)["apns_token"]; leaked {
		t.Error("apns_token leaked in /me response")
	}
}

func TestPushTokenEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	id, tok := register(t, s, "Alice")

	// Unauthenticated updates are rejected.
	resp, _ := doJSON(t, s, "PUT", "/api/v1/devices/push", "", map[string]any{"apns_token": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated push update = %d, want 401", resp.StatusCode)
	}
	// Set.
	resp, _ = doJSON(t, s, "PUT", "/api/v1/devices/push", tok, map[string]any{"apns_token": "tok-new"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set token: %d", resp.StatusCode)
	}
	d, _ := st.DeviceByID(id)
	if d.APNsToken != "tok-new" {
		t.Errorf("apns_token = %q after set", d.APNsToken)
	}
	// Clear with an empty token.
	resp, _ = doJSON(t, s, "PUT", "/api/v1/devices/push", tok, map[string]any{"apns_token": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear token: %d", resp.StatusCode)
	}
	d, _ = st.DeviceByID(id)
	if d.APNsToken != "" {
		t.Errorf("apns_token = %q after clear, want empty", d.APNsToken)
	}
	// Oversized tokens are rejected.
	resp, _ = doJSON(t, s, "PUT", "/api/v1/devices/push", tok, map[string]any{"apns_token": strings.Repeat("a", 513)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized token = %d, want 400", resp.StatusCode)
	}
}

// newPushTestServer builds a server whose push attempts are recorded.
func newPushTestServer(t testing.TB) (*Server, *store.Store, *recordingSender) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec := &recordingSender{}
	s := New(st, push.NewMulti(rec), "", []string{"sos", "geofence", "crash"})
	return s, st, rec
}

func TestAPNsFanout(t *testing.T) {
	s, st, rec := newPushTestServer(t)
	aliceID, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	_, carolTok := register(t, s, "Carol")
	// Bob registers an APNs token; Carol has none.
	resp, _ := doJSON(t, s, "PUT", "/api/v1/devices/push", bobTok, map[string]any{"apns_token": "tok-bob"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob token: %d", resp.StatusCode)
	}
	resp, out := doJSON(t, s, "POST", "/api/v1/circles", aliceTok, map[string]any{"name": "C"})
	circleID := out["id"].(string)
	code := out["invite_code"].(string)
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/join", carolTok, map[string]any{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("carol join: %d", resp.StatusCode)
	}

	// Alice (sender, tokenless) posts an SOS.
	resp, _ = doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", aliceTok, map[string]any{
		"kind": "sos", "ts": time.Now().UnixMilli(), "nonce": "n1", "ciphertext": "c2ln",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("sos post: %d", resp.StatusCode)
	}
	pushes := waitPushes(t, rec, 2)

	// One ntfy topic broadcast and one per-device APNs delivery to Bob —
	// never to the sender (Alice) and never to tokenless Carol.
	var topics, devices []recordedPush
	for _, p := range pushes {
		if p.Topic != "" {
			topics = append(topics, p)
		}
		if p.DeviceToken != "" {
			devices = append(devices, p)
		}
	}
	if len(topics) != 1 || topics[0].Topic != "lodestar-"+circleID {
		t.Errorf("topic pushes = %+v, want exactly one for %q", topics, "lodestar-"+circleID)
	}
	if len(devices) != 1 || devices[0].DeviceToken != "tok-bob" {
		t.Errorf("device pushes = %+v, want exactly one for tok-bob", devices)
	}
	if !strings.Contains(devices[0].Title, "Alice") {
		t.Errorf("apns title = %q, want it to name the sender", devices[0].Title)
	}
	if d, _ := st.DeviceByID(aliceID); d.APNsToken != "" {
		t.Error("sender's token should not have been created by the fanout")
	}
}

func TestAPNsUnregisteredClearsToken(t *testing.T) {
	s, st, rec := newPushTestServer(t)
	_, aliceTok := register(t, s, "Alice")
	bobID, bobTok := register(t, s, "Bob")
	resp, _ := doJSON(t, s, "PUT", "/api/v1/devices/push", bobTok, map[string]any{"apns_token": "tok-dead"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob token: %d", resp.StatusCode)
	}
	resp, out := doJSON(t, s, "POST", "/api/v1/circles", aliceTok, map[string]any{"name": "C"})
	circleID := out["id"].(string)
	resp, out = doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": out["invite_code"].(string)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d", resp.StatusCode)
	}
	rec.unregister = "tok-dead"

	postSOS := func(nonce string) {
		t.Helper()
		resp, _ := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", aliceTok, map[string]any{
			"kind": "sos", "ts": time.Now().UnixMilli(), "nonce": nonce, "ciphertext": "c2ln",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("sos post: %d", resp.StatusCode)
		}
	}

	postSOS("n1")
	pushes := waitPushes(t, rec, 1)
	if len(pushes) != 1 {
		t.Fatalf("pushes = %+v, want only the topic broadcast (dead token must not record)", pushes)
	}
	// Apple said the token is dead — the server must clear it. The clear
	// happens in the push goroutine after the failed send, so poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d, err := st.DeviceByID(bobID)
		if err != nil {
			t.Fatal(err)
		}
		if d.APNsToken == "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d, err := st.DeviceByID(bobID)
	if err != nil {
		t.Fatal(err)
	}
	if d.APNsToken != "" {
		t.Errorf("apns_token after unregister = %q, want cleared", d.APNsToken)
	}

	postSOS("n2")
	pushes = waitPushes(t, rec, 2)
	var devices []recordedPush
	for _, p := range pushes {
		if p.DeviceToken != "" {
			devices = append(devices, p)
		}
	}
	if len(devices) != 0 {
		t.Errorf("device pushes after unregister = %+v, want none", devices)
	}
}

func TestRegisterSanitizesControlChars(t *testing.T) {
	s, _ := newTestServer(t)
	_, out := doJSON(t, s, "POST", "/api/v1/devices", "", map[string]any{
		"name": "Bad\nName\tDevice\r", "ed25519_pub": testPubKey, "x25519_pub": testPubKey,
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

// TestCORSPreflight: browser/web tools must be able to call every method
// the API uses — in particular PUT /devices/push — or the preflight fails
// and push-token sync from a web client silently dies.
func TestCORSPreflight(t *testing.T) {
	s, _ := newTestServer(t)
	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	req, err := http.NewRequest(http.MethodOptions, httpSrv.URL+"/api/v1/devices/push", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	methods := resp.Header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "PUT") {
		t.Errorf("allow-methods = %q, missing PUT (web clients cannot sync push tokens)", methods)
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if !strings.Contains(methods, m) {
			t.Errorf("allow-methods = %q, missing %s", methods, m)
		}
	}
}

// TestWSReadLimitClosesOversizedFrame: the socket must declare an inbound
// limit; a frame larger than maxEnvelopeSize+1024 is closed by the server
// instead of being buffered.
func TestWSReadLimitClosesOversizedFrame(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "C")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID

	conn, httpResp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + tok}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v (http %d)", err, httpResp.StatusCode)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	time.Sleep(100 * time.Millisecond) // let the subscription land

	// One JSON frame just over the declared limit (64 KiB + 1024 slack).
	big := make([]byte, (64<<10)+2048)
	for i := range big {
		big[i] = 'x'
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, string(big)); err == nil {
		// Write succeeded locally; the server must now fail the connection.
		var msg any
		if rerr := wsjson.Read(ctx, conn, &msg); rerr == nil {
			t.Error("oversized frame was accepted; connection should have been closed")
		} else if websocket.CloseStatus(rerr) == -1 {
			t.Errorf("read error is not a close frame: %v", rerr)
		}
	}
	// If Write failed locally the socket already knew the size was over the
	// negotiated limit — either way the frame never gets through.
}

// TestPushTitleCoversKinds: every kind the server pushes must map to a
// non-empty title, including kinds added later that forget their entry —
// an empty alert title on a user's lock screen is a bug.
func TestPushTitleCoversKinds(t *testing.T) {
	for _, kind := range []string{"sos", "crash", "geofence", "checkin"} {
		if got := pushTitle(kind, "Alice"); got == "" || got == "Lodestar update" {
			t.Errorf("pushTitle(%q) = %q, want a dedicated title", kind, got)
		}
	}
	if got := pushTitle("future-kind", "Alice"); got != "Lodestar update" {
		t.Errorf("pushTitle(future-kind) = %q, want the fallback", got)
	}
}

// TestDeleteSelfRevokesEverything: DELETE /api/v1/devices/self kills the
// token, the memberships, the key blobs, and the live sockets. Other
// members keep working and the circle survives an owner's departure.
func TestDeleteSelfRevokesEverything(t *testing.T) {
	s, st := newTestServer(t)
	aliceID, aliceTok := register(t, s, "Alice")
	_, bobTok := register(t, s, "Bob")
	circleID := createCircle(t, s, aliceTok, "C")
	resp, out := doJSON(t, s, "POST", "/api/v1/circles/join", bobTok, map[string]any{"code": mustInvite(t, s, aliceTok, circleID)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: %d (%v)", resp.StatusCode, out)
	}
	if err := st.PutKeyBlob(circleID, aliceID, "blob-a"); err != nil {
		t.Fatal(err)
	}

	// Live socket before deletion: must be kicked.
	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws?circle=" + circleID
	conn, httpResp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + bobTok}},
	})
	_ = conn // bob's socket stays open; alice's revocation must not touch it
	if err != nil {
		t.Fatalf("ws dial: %v (http %d)", err, httpResp.StatusCode)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	time.Sleep(100 * time.Millisecond)

	resp, _ = doJSON(t, s, "DELETE", "/api/v1/devices/self", aliceTok, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete self: %d", resp.StatusCode)
	}

	// Token is dead.
	resp, _ = doJSON(t, s, "GET", "/api/v1/me", aliceTok, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me after revoke: %d, want 401", resp.StatusCode)
	}
	// Key blob gone.
	if _, err := st.KeyBlob(circleID, aliceID); err == nil {
		t.Error("key blob survived device deletion")
	}
	// Bob keeps working.
	resp, _ = doJSON(t, s, "GET", "/api/v1/me", bobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bob me after alice revoke: %d, want 200", resp.StatusCode)
	}
	// Circle ownership transferred to Bob.
	detail, out := doJSON(t, s, "GET", "/api/v1/circles/"+circleID, bobTok, nil)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("circle detail: %d (%v)", detail.StatusCode, out)
	}
	if out["owner_device_id"] == aliceID {
		t.Error("circle still owned by the deleted device")
	}
	// Bob's socket must NOT have been kicked by alice's deletion.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	postEnvelope(t, s, bobTok, circleID, "checkin")
	var msg map[string]any
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Errorf("bob socket died after alice's self-delete: %v", err)
	}
}

// TestEnvelopeCompositeCursor pages same-millisecond envelopes through the
// HTTP API: the (ts, id) cursor must return every row exactly once in the
// canonical (ts DESC, id DESC) order — a ts-only cursor would skip or
// re-fetch rows at the page boundary.
func TestEnvelopeCompositeCursor(t *testing.T) {
	s, _ := newTestServer(t)
	_, tok := register(t, s, "Alice")
	circleID := createCircle(t, s, tok, "Family")

	ts := time.Now().UnixMilli()
	var ids []string
	for i := 0; i < 3; i++ {
		resp, out := doJSON(t, s, "POST", "/api/v1/circles/"+circleID+"/envelopes", tok, map[string]any{
			"kind": "location", "ts": ts,
			"nonce": fmt.Sprintf("n-%d", i), "ciphertext": "c2lnaHQ=",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("post %d: status %d", i, resp.StatusCode)
		}
		ids = append(ids, out["id"].(string))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids))) // ts DESC, id DESC

	// Page backward one row at a time; the cursor must resume exactly at
	// the previous page's last row.
	var got []string
	before, beforeID := 0, ""
	for i := 0; i < 5; i++ {
		path := fmt.Sprintf("/api/v1/circles/%s/envelopes?limit=1", circleID)
		if before > 0 {
			path += fmt.Sprintf("&before=%d&before_id=%s", before, beforeID)
		}
		resp, out := doJSON(t, s, "GET", path, tok, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d: status %d", i, resp.StatusCode)
		}
		envs, _ := out["envelopes"].([]any)
		if len(envs) == 0 {
			break
		}
		env := envs[0].(map[string]any)
		got = append(got, env["id"].(string))
		before = int(env["ts"].(float64))
		beforeID = env["id"].(string)
	}
	if len(got) != len(ids) {
		t.Fatalf("paged %v, want all of %v", got, ids)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("page %d = %s, want %s (full: %v)", i, got[i], ids[i], got)
		}
	}

	// Forward catch-up: nothing is newer than the newest envelope.
	resp, out := doJSON(t, s, "GET", fmt.Sprintf(
		"/api/v1/circles/%s/envelopes?since=%d&since_id=%s", circleID, ts, ids[0]), tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("since query: status %d", resp.StatusCode)
	}
	if envs, _ := out["envelopes"].([]any); len(envs) != 0 {
		t.Errorf("since cursor returned %d rows, want 0", len(envs))
	}
}

// TestRegisterValidatesPubKeys: registration must reject anything that is
// not canonical base64 of a 32-byte public key, or the key exchange is
// bricked later.
func TestRegisterValidatesPubKeys(t *testing.T) {
	s, _ := newTestServer(t)
	ok := map[string]any{
		"name": "Alice", "ed25519_pub": testPubKey, "x25519_pub": testPubKey,
	}
	// Valid both keys → 201.
	if resp, _ := doJSON(t, s, "POST", "/api/v1/devices", "", ok); resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid keys: status %d, want 201", resp.StatusCode)
	}
	cases := map[string]map[string]any{
		"garbage ed25519": {"name": "A", "ed25519_pub": "not-a-key", "x25519_pub": testPubKey},
		"garbage x25519":  {"name": "A", "ed25519_pub": testPubKey, "x25519_pub": "x"},
		"short key":       {"name": "A", "ed25519_pub": "MDAwMDAwMDAwMDAwMDAwMDAwMDAwMA==", "x25519_pub": testPubKey},
		"missing":         {"name": "A", "x25519_pub": testPubKey},
		"empty":           {"name": "A", "ed25519_pub": "", "x25519_pub": testPubKey},
	}
	for label, body := range cases {
		if resp, _ := doJSON(t, s, "POST", "/api/v1/devices", "", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", label, resp.StatusCode)
		}
	}
}
