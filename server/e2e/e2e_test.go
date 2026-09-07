package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// waitFor polls fn until it returns nil or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = fn(); lastErr == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %v", timeout, lastErr)
}

// TestFamilyLoop is the core story: two devices, one circle, live relay.
func TestFamilyLoop(t *testing.T) {
	s := startServer(t)
	c := newClient(t, s.url)

	aliceDev, aliceTok := c.register("Alice")
	bobDev, bobTok := c.register("Bob")

	alice := c.withToken(aliceTok)
	cr := alice.createCircle("The Crew")
	if cr.OwnerDeviceID != aliceDev.ID {
		t.Fatalf("owner = %s, want %s", cr.OwnerDeviceID, aliceDev.ID)
	}

	// Bob joins with the circle's auto-generated invite code.
	joined := c.withToken(bobTok).join(cr.InviteCode)
	if joined.ID != cr.ID {
		t.Fatalf("joined circle %s, want %s", joined.ID, cr.ID)
	}

	aws := connectWS(t, s.url, aliceTok, cr.ID)
	bws := connectWS(t, s.url, bobTok, cr.ID)

	// Alice posts a location fix; Bob sees it on the live socket.
	loc := alice.postEnvelope(cr.ID, "location", "loc-1", time.Now().UnixMilli())
	bws.expect(loc, 5*time.Second)

	// A chat message reaches both sockets (echo to the sender included).
	msg := alice.postEnvelope(cr.ID, "message", "msg-1", time.Now().UnixMilli())
	aws.expect(msg, 5*time.Second)
	bws.expect(msg, 5*time.Second)

	// History returns both, newest first.
	envs := alice.envelopes(cr.ID, "?limit=10")
	if len(envs) != 2 {
		t.Fatalf("history has %d envelopes, want 2", len(envs))
	}
	if envs[0].ID != msg.ID || envs[1].ID != loc.ID {
		t.Fatalf("history order wrong: %+v", envs)
	}

	// Membership: both devices present, Alice the owner.
	_, members := alice.circleDetail(cr.ID)
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	roles := map[string]string{}
	for _, m := range members {
		roles[m.DeviceID] = m.Role
	}
	if roles[aliceDev.ID] != "owner" || roles[bobDev.ID] != "member" {
		t.Fatalf("roles = %+v", roles)
	}

	// Device-filtered history (the screen that pages one member's fixes).
	onlyBob := c.withToken(bobTok).envelopes(cr.ID, "?device="+bobDev.ID)
	if len(onlyBob) != 0 {
		t.Fatalf("bob history = %d, want 0", len(onlyBob))
	}
	onlyAlice := alice.envelopes(cr.ID, "?device="+aliceDev.ID)
	if len(onlyAlice) != 2 {
		t.Fatalf("alice history = %d, want 2", len(onlyAlice))
	}

	// Sharing can be toggled off and back on by the member themselves.
	status, _ := c.withToken(aliceTok).do("POST", "/api/v1/circles/"+cr.ID+"/members/"+aliceDev.ID+"/sharing",
		map[string]bool{"enabled": false})
	if status != http.StatusOK {
		t.Fatalf("sharing off: status %d", status)
	}
	_, members = alice.circleDetail(cr.ID)
	for _, m := range members {
		if m.DeviceID == aliceDev.ID && m.SharingEnabled {
			t.Fatal("sharing should be off for alice")
		}
	}
}

// TestDedupAndEcho: a retry with the same nonce must not duplicate the row
// or re-broadcast, and the original envelope id is returned.
func TestDedupAndEcho(t *testing.T) {
	s := startServer(t)
	c := newClient(t, s.url)

	_, aliceTok := c.register("Alice")
	_, bobTok := c.register("Bob")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	c.withToken(bobTok).join(cr.InviteCode)

	bws := connectWS(t, s.url, bobTok, cr.ID)

	ts := time.Now().UnixMilli()
	env := alice.postEnvelope(cr.ID, "message", "dedup-nonce", ts)
	bws.expect(env, 5*time.Second)

	// Retry the exact same envelope (simulated lost ack): same stored id,
	// no duplicate row, no second broadcast.
	var retried envelope
	alice.decode("POST", "/api/v1/circles/"+cr.ID+"/envelopes", map[string]any{
		"kind": "message", "ts": ts, "nonce": "dedup-nonce", "ciphertext": "ct-dedup-nonce",
	}, http.StatusCreated, &retried)
	if retried.ID != env.ID {
		t.Fatalf("retry id = %s, want %s (dedup must return the stored row)", retried.ID, env.ID)
	}
	if got := alice.envelopes(cr.ID, "?limit=10"); len(got) != 1 {
		t.Fatalf("history has %d rows, want 1", len(got))
	}
	bws.expectNone(500 * time.Millisecond)
}

// TestSOSPushFanout: a push-kind envelope must reach the circle over the
// socket AND the configured push provider (via a local ntfy stub).
func TestSOSPushFanout(t *testing.T) {
	stub, hits := ntfyStub(t)
	s := startServer(t, "-ntfy-url", stub.URL)
	c := newClient(t, s.url)

	_, aliceTok := c.register("Alice")
	_, bobTok := c.register("Bob")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	c.withToken(bobTok).join(cr.InviteCode)

	// Exercise the explicit invite endpoint too.
	if code := alice.createInvite(cr.ID); code == "" {
		t.Fatal("empty invite code")
	}

	bws := connectWS(t, s.url, bobTok, cr.ID)
	env := alice.postEnvelope(cr.ID, "sos", "sos-1", time.Now().UnixMilli())
	bws.expect(env, 5*time.Second)

	// The binary must have relayed it to the ntfy stub with the right shape.
	wantPath := "/lodestar-" + cr.ID
	wantTitle := "🚨 SOS from Alice"
	waitFor(t, 5*time.Second, func() error {
		for _, h := range hits() {
			if h.path == wantPath && h.title == wantTitle && h.priority == "5" {
				var body envelope
				if err := json.Unmarshal(h.body, &body); err != nil {
					return fmt.Errorf("stub body not an envelope: %v", err)
				}
				if body.ID != env.ID {
					return fmt.Errorf("stub envelope %s, want %s", body.ID, env.ID)
				}
				return nil
			}
		}
		return fmt.Errorf("no push hit for %s (hits: %d)", wantPath, len(hits()))
	})

	// Non-push kinds (location) must NOT hit the provider.
	alice.postEnvelope(cr.ID, "location", "loc-1", time.Now().UnixMilli())
	time.Sleep(500 * time.Millisecond)
	for _, h := range hits() {
		if strings.Contains(string(h.body), "loc-1") {
			t.Fatalf("location envelope leaked to push provider: %+v", h)
		}
	}
}

// TestKickClosesSocket: removing a member revokes live access immediately.
func TestKickClosesSocket(t *testing.T) {
	s := startServer(t)
	c := newClient(t, s.url)

	_, aliceTok := c.register("Alice")
	bobDev, bobTok := c.register("Bob")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	c.withToken(bobTok).join(cr.InviteCode)

	aws := connectWS(t, s.url, aliceTok, cr.ID)
	bws := connectWS(t, s.url, bobTok, cr.ID)

	if status := alice.removeMember(cr.ID, bobDev.ID); status != http.StatusNoContent {
		t.Fatalf("kick: status %d", status)
	}

	// Bob's socket closes with a policy violation within moments.
	if err := bws.closed(5 * time.Second); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("bob close: %v", err)
	}

	// Bob's API access is gone; Alice's socket still works.
	status, _ := c.withToken(bobTok).do("GET", "/api/v1/circles/"+cr.ID+"/envelopes", nil)
	if status != http.StatusForbidden {
		t.Fatalf("kicked member GET = %d, want 403", status)
	}
	env := alice.postEnvelope(cr.ID, "location", "loc-1", time.Now().UnixMilli())
	aws.expect(env, 5*time.Second)
}

// TestRestartPersistence: data survives a graceful restart on the same DB.
func TestRestartPersistence(t *testing.T) {
	s := startServer(t)
	c := newClient(t, s.url)

	_, aliceTok := c.register("Alice")
	_, bobTok := c.register("Bob")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	c.withToken(bobTok).join(cr.InviteCode)
	env := alice.postEnvelope(cr.ID, "location", "loc-1", time.Now().UnixMilli())

	// Graceful SIGTERM: the process must exit 0 on unix (kill on Windows).
	if err := s.stop(); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}

	s2 := startServerAt(t, s.dbPath)
	c2 := newClient(t, s2.url)

	// The old bearer tokens still work: devices and data persisted.
	if circles := c2.withToken(aliceTok).circles(); len(circles) != 1 || circles[0].ID != cr.ID {
		t.Fatalf("circles after restart = %+v", circles)
	}
	envs := c2.withToken(aliceTok).envelopes(cr.ID, "?limit=10")
	if len(envs) != 1 || envs[0].ID != env.ID {
		t.Fatalf("envelopes after restart = %+v", envs)
	}
	_, members := c2.withToken(aliceTok).circleDetail(cr.ID)
	if len(members) != 2 {
		t.Fatalf("members after restart = %d, want 2", len(members))
	}
}

// TestAuthAndSkewGuards: the server rejects bad tokens, non-members,
// out-of-range timestamps, and unauthenticated admin access.
func TestAuthAndSkewGuards(t *testing.T) {
	s := startServer(t, "-admin-token", "adm-secret")
	c := newClient(t, s.url)

	// No token / garbage token.
	if status, _ := c.do("GET", "/api/v1/me", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous /me = %d, want 401", status)
	}
	if status, _ := c.withToken("garbage").do("GET", "/api/v1/me", nil); status != http.StatusUnauthorized {
		t.Fatalf("garbage token /me = %d, want 401", status)
	}

	_, aliceTok := c.register("Alice")
	_, bobTok := c.register("Bob")
	_, carolTok := c.register("Carol")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	c.withToken(bobTok).join(cr.InviteCode)

	// A non-member cannot post to the circle.
	status, _ := c.withToken(carolTok).do("POST", "/api/v1/circles/"+cr.ID+"/envelopes", map[string]any{
		"kind": "location", "ts": time.Now().UnixMilli(), "nonce": "n1", "ciphertext": "c",
	})
	if status != http.StatusForbidden {
		t.Fatalf("non-member POST = %d, want 403", status)
	}

	// Timestamps outside the skew window are rejected.
	post := func(ts int64) int {
		status, _ := alice.do("POST", "/api/v1/circles/"+cr.ID+"/envelopes", map[string]any{
			"kind": "location", "ts": ts, "nonce": fmt.Sprintf("n-%d", ts), "ciphertext": "c",
		})
		return status
	}
	now := time.Now().UnixMilli()
	if status := post(now + 10*60*1000); status != http.StatusBadRequest {
		t.Fatalf("future ts = %d, want 400", status)
	}
	if status := post(now - 200*24*3600*1000); status != http.StatusBadRequest {
		t.Fatalf("ancient ts = %d, want 400", status)
	}

	// Empty ciphertext is rejected (envelope shape validation).
	status, _ = alice.do("POST", "/api/v1/circles/"+cr.ID+"/envelopes", map[string]any{
		"kind": "message", "ts": now, "nonce": "n-empty", "ciphertext": "",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("empty ciphertext = %d, want 400", status)
	}

	// Admin requires the configured token.
	if status, _ := c.do("GET", "/admin", nil); status != http.StatusUnauthorized {
		t.Fatalf("admin without token = %d, want 401", status)
	}
	if status, _ := c.do("GET", "/admin?token=adm-secret", nil); status != http.StatusOK {
		t.Fatalf("admin with token = %d, want 200", status)
	}
}

// TestKeyBlobs: only the owner writes key blobs, only for actual members,
// and only members read their own.
func TestKeyBlobs(t *testing.T) {
	s := startServer(t)
	c := newClient(t, s.url)

	_, aliceTok := c.register("Alice")
	bobDev, bobTok := c.register("Bob")
	carolDev, carolTok := c.register("Carol")
	alice := c.withToken(aliceTok)
	cr := alice.createCircle("C")
	bob := c.withToken(bobTok)
	bob.join(cr.InviteCode)

	// Owner grants Bob his key blob; Bob reads it back.
	if status := alice.putKey(cr.ID, bobDev.ID, "blob-for-bob"); status != http.StatusNoContent {
		t.Fatalf("owner put key: %d", status)
	}
	if status := alice.putKey(cr.ID, bobDev.ID, "blob-for-bob"); status != http.StatusNoContent {
		t.Fatalf("owner put key (retry): %d", status)
	}
	ct, status := bob.getKey(cr.ID)
	if status != http.StatusOK || ct != "blob-for-bob" {
		t.Fatalf("bob key = (%q, %d), want blob-for-bob", ct, status)
	}

	// A non-owner member cannot write (key poisoning guard).
	if status := bob.putKey(cr.ID, bobDev.ID, "forged"); status != http.StatusForbidden {
		t.Fatalf("member put key: %d, want 403", status)
	}

	// The owner cannot grant a blob to a non-member.
	if status := alice.putKey(cr.ID, carolDev.ID, "leak"); status != http.StatusForbidden {
		t.Fatalf("owner put key for non-member: %d, want 403", status)
	}

	// A non-member cannot read a blob.
	if _, status := c.withToken(carolTok).getKey(cr.ID); status != http.StatusForbidden {
		t.Fatalf("non-member get key: %d, want 403", status)
	}
}
