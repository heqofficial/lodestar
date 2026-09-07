package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/heqofficial/lodestar/server/internal/push"
	"github.com/heqofficial/lodestar/server/internal/store"
)

const (
	maxEnvelopeSize  = 64 << 10 // 64 KiB of ciphertext per envelope
	maxNameLen       = 64
	maxCirclePerUser = 32
	maxMembers       = 64

	// Per-kind envelope throttles (per device, per circle): the app posts
	// at most ~4 location fixes/min, so generous headroom still stops a
	// hostile member from flooding the DB at the full API rate.
	locationKindPerMin = 30.0
	otherKindPerMin    = 6.0

	// Envelope timestamps must be within this skew of server time.
	maxFutureSkewMs = 5 * 60_000
	maxPastSkewMs   = 180 * 24 * 3600_000 // 180 days
)

func randID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

// --- health & admin --------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Real probe: a green healthz must mean the store answers too, so
	// orchestrators (Docker HEALTHCHECK) restart a wedged server.
	if err := s.pingDB(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "db unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if s.adminToken != "" {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.adminToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "missing or bad admin token")
			return
		}
	}
	fsys, err := dashboardFS()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "dashboard missing")
		return
	}
	devices, circles, members, _ := s.store.Counts()
	envelopes, _ := s.store.EnvelopeCount()
	stats := map[string]any{
		"uptime_s":  int(time.Since(s.startedAt).Seconds()),
		"devices":   devices,
		"circles":   circles,
		"members":   members,
		"envelopes": envelopes,
		"push": map[string]any{
			"ntfy": s.ntfyEnabled(),
			"apns": s.apnsEnabled(),
		},
	}
	// Serve the static page with a small stats footer injected.
	page, err := fs.ReadFile(fsys, "dashboard.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "dashboard read failed")
		return
	}
	statsJSON, _ := json.Marshal(stats)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "%s\n<script>window.__LODESTAR_STATS__=%s;</script>", page, statsJSON)
}

// --- devices ---------------------------------------------------------------

type registerRequest struct {
	Name       string `json:"name"`
	Ed25519Pub string `json:"ed25519_pub"`
	X25519Pub  string `json:"x25519_pub"`
}

func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if !s.regLimiter.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many registrations from this address")
		return
	}
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Name = sanitizeName(req.Name)
	if req.Name == "" {
		req.Name = "My Device"
	}
	if len(req.Name) > maxNameLen || req.Ed25519Pub == "" || req.X25519Pub == "" {
		writeErr(w, http.StatusBadRequest, "invalid fields")
		return
	}
	token := randID(32)
	sum := sha256Hex(token)
	dev := store.Device{
		ID:         randID(16),
		Name:       req.Name,
		Ed25519Pub: req.Ed25519Pub,
		X25519Pub:  req.X25519Pub,
		TokenHash:  sum,
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := s.store.CreateDevice(dev); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not register")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"device": dev,
		"token":  token, // shown once; only the hash is stored
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"device": deviceFrom(r.Context())})
}

// --- circles ---------------------------------------------------------------

type circleRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Server) handleCreateCircle(w http.ResponseWriter, r *http.Request) {
	var req circleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "My Family"
	}
	if len(req.Name) > maxNameLen {
		writeErr(w, http.StatusBadRequest, "name too long")
		return
	}
	dev := deviceFrom(r.Context())
	circles, err := s.store.CirclesForDevice(dev.ID)
	if err != nil || len(circles) >= maxCirclePerUser {
		writeErr(w, http.StatusBadRequest, "circle limit reached")
		return
	}
	c := store.Circle{
		ID:            randID(16),
		Name:          req.Name,
		Color:         firstNonEmpty(req.Color, "#4f7cff"),
		OwnerDeviceID: dev.ID,
		InviteCode:    newInviteCode(),
		CreatedAt:     time.Now().UnixMilli(),
	}
	if err := s.store.CreateCircle(c, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create circle")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleJoinCircle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}
	c, err := s.store.CircleByInvite(code)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid or expired code")
		return
	}
	dev := deviceFrom(r.Context())
	if err := s.store.AddMember(c.ID, dev.ID, ""); err != nil {
		if errors.Is(err, store.ErrCircleFull) {
			writeErr(w, http.StatusForbidden, "circle is full")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not join")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListCircles(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r.Context())
	circles, err := s.store.CirclesForDevice(dev.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"circles": circles})
}

func (s *Server) handleCircleDetail(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	member, err := s.mustBeMember(w, circleID, dev.ID)
	if err != nil {
		return
	}
	_ = member
	c, err := s.store.CircleByID(circleID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "circle not found")
		return
	}
	members, err := s.store.Members(circleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "members failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"circle": c, "members": members})
}

// --- envelopes -------------------------------------------------------------

type envelopeRequest struct {
	Kind       string `json:"kind"`
	TS         int64  `json:"ts"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func (s *Server) handlePostEnvelope(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	var req envelopeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnvelopeSize+1024)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if !store.IsEnvelopeKind(req.Kind) {
		writeErr(w, http.StatusBadRequest, "unknown kind")
		return
	}
	// Per-kind throttle: bounds DB growth even if the global limiter is
	// bypassed, and stops kind-label spam (e.g. fake SOS alerts).
	if !s.kindThrottle(req.Kind).allow(dev.ID) {
		writeErr(w, http.StatusTooManyRequests, "kind rate limit exceeded")
		return
	}
	now := time.Now().UnixMilli()
	if req.TS <= 0 || req.TS > now+maxFutureSkewMs || req.TS < now-maxPastSkewMs {
		writeErr(w, http.StatusBadRequest, "ts out of range")
		return
	}
	if req.Nonce == "" || len(req.Ciphertext) > maxEnvelopeSize {
		writeErr(w, http.StatusBadRequest, "bad envelope body")
		return
	}
	env := store.Envelope{
		ID:         randID(16),
		CircleID:   circleID,
		DeviceID:   dev.ID,
		Kind:       req.Kind,
		TS:         req.TS,
		Nonce:      req.Nonce,
		Ciphertext: req.Ciphertext,
	}
	stored, inserted, err := s.store.AddEnvelope(env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	// Fan out to circle members over WebSocket (idempotent retries that hit
	// the dedup index are not re-broadcast).
	payload, _ := json.Marshal(stored)
	if inserted {
		s.hub.Broadcast(circleID, payload)
	}
	// Push alerts for high-signal kinds.
	if s.pushKinds[req.Kind] && s.push != nil && s.push.Enabled() {
		title := map[string]string{
			"sos":      "🚨 SOS from " + dev.Name,
			"geofence": "📍 Geofence event",
			"checkin":  "✅ Check-in from " + dev.Name,
		}[req.Kind]
		go func() {
			// NB: use context.Background(), not r.Context() — the request
			// context is cancelled as soon as this handler returns.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.push.Send(ctx, push.Request{
				Title:    title,
				Priority: 5,
				Payload:  payload,
				Topic:    "lodestar-" + circleID,
			})
		}()
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (s *Server) handleGetEnvelopes(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	kind := q.Get("kind")
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	envs, err := s.store.Envelopes(circleID, since, kind, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envelopes": envs})
}

func (s *Server) handleLatestEnvelopes(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	envs, err := s.store.LatestPerDevice(circleID)
	if err != nil {
		slog.Warn("latest per device", "err", err)
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envelopes": envs})
}

// --- circle keys -----------------------------------------------------------

func (s *Server) handlePutKeyBlob(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	var req struct {
		ForDevice  string `json:"for_device"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.ForDevice == "" || req.Ciphertext == "" {
		writeErr(w, http.StatusBadRequest, "missing fields")
		return
	}
	// Key blobs are the only path that distributes the circle key: only
	// actual members may receive one, or any member could overwrite the
	// owner's blobs (key poisoning) or exfiltrate to arbitrary devices.
	if _, err := s.store.MemberRole(circleID, req.ForDevice); err != nil {
		writeErr(w, http.StatusForbidden, "for_device is not a member")
		return
	}
	if err := s.store.PutKeyBlob(circleID, req.ForDevice, req.Ciphertext); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetKeyBlob(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	blob, err := s.store.KeyBlob(circleID, dev.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no key blob yet")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ciphertext": blob})
}

// --- invites, sharing, membership ------------------------------------------

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	dev := deviceFrom(r.Context())
	m, err := s.mustBeMember(w, circleID, dev.ID)
	if err != nil {
		return
	}
	if m.Role != "owner" {
		writeErr(w, http.StatusForbidden, "only the owner invites")
		return
	}
	var req struct {
		TTLHours int `json:"ttl_hours"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req)
	if req.TTLHours <= 0 {
		req.TTLHours = 168 // one week
	}
	if req.TTLHours > 24*30 {
		req.TTLHours = 24 * 30
	}
	inv := store.Invite{
		Code:      newInviteCode(),
		CircleID:  circleID,
		CreatedBy: dev.ID,
		ExpiresAt: time.Now().Add(time.Duration(req.TTLHours) * time.Hour).UnixMilli(),
	}
	if err := s.store.CreateInvite(inv); err != nil {
		writeErr(w, http.StatusInternalServerError, "invite failed")
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleSetSharing(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	did := r.PathValue("did")
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	// Members may only change their own sharing state.
	if did != dev.ID {
		writeErr(w, http.StatusForbidden, "only your own sharing state")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.store.SetSharing(circleID, did, *req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	circleID := r.PathValue("id")
	did := r.PathValue("did")
	dev := deviceFrom(r.Context())
	m, err := s.mustBeMember(w, circleID, dev.ID)
	if err != nil {
		return
	}
	// Self-leave always allowed; kicking others requires owner.
	if did != dev.ID && m.Role != "owner" {
		writeErr(w, http.StatusForbidden, "owner only")
		return
	}
	if err := s.store.RemoveMember(circleID, did); err != nil {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	// Revoke live access immediately: a removed member's open socket must
	// not keep receiving the circle's location stream.
	s.hub.Kick(circleID, did)
	w.WriteHeader(http.StatusNoContent)
}

// --- shared helpers --------------------------------------------------------

// mustBeMember verifies membership (single query) and returns the role; on
// failure it writes the error response and returns an error so callers bail.
func (s *Server) mustBeMember(w http.ResponseWriter, circleID, deviceID string) (*store.Member, error) {
	role, err := s.store.MemberRole(circleID, deviceID)
	if err != nil {
		writeErr(w, http.StatusForbidden, "not a member")
		return nil, errors.New("not a member")
	}
	return &store.Member{CircleID: circleID, DeviceID: deviceID, Role: role}, nil
}

func (s *Server) ntfyEnabled() bool {
	return s.push != nil && s.push.Enabled()
}

func (s *Server) apnsEnabled() bool {
	return s.push != nil && s.push.Enabled()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sanitizeName trims and strips control characters (they would otherwise
// flow into push titles and logs).
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// newInviteCode generates a 6-char code from a small alphabet (no 0/O/1/I).
func newInviteCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(b)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
