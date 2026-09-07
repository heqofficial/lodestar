// Package api implements the Lodestar HTTP + WebSocket API.
//
// Design invariants:
//   - Envelope payloads are opaque: the server validates shape, never content.
//   - Bearer tokens identify devices; only SHA-256 hashes are stored.
//   - All circle data access is membership-checked.
package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/heqofficial/lodestar/server/internal/push"
	"github.com/heqofficial/lodestar/server/internal/store"
)

//go:embed static
var staticFS embed.FS

// Server holds the API's dependencies.
type Server struct {
	store      *store.Store
	push       *push.Multi
	hub        *Hub
	adminToken string
	limiter    *rateLimiter
	regLimiter *rateLimiter // per-IP, for the unauthenticated register endpoint
	startedAt  time.Time
	pushKinds  map[string]bool

	kindLimMu   sync.Mutex
	kindLimiter map[string]*rateLimiter // per-kind throttles for envelope POSTs
}

// New constructs the API server. pushKinds lists envelope kinds that trigger
// a push notification (e.g. "sos,geofence").
func New(st *store.Store, sender *push.Multi, adminToken string, pushKinds []string) *Server {
	kinds := map[string]bool{}
	for _, k := range pushKinds {
		kinds[strings.TrimSpace(k)] = true
	}
	s := &Server{
		store:       st,
		push:        sender,
		hub:         NewHub(),
		adminToken:  adminToken,
		limiter:     newRateLimiter(120, 240), // 120 req/min per device, burst 240
		regLimiter:  newRateLimiter(6, 12),    // 6 registrations/min per IP
		startedAt:   time.Now(),
		pushKinds:   kinds,
		kindLimiter: map[string]*rateLimiter{},
	}
	// Janitor: bound the memory of the token buckets (per-device entries
	// for limiter/regLimiter, per-device-per-kind entries for kindLimiters).
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.limiter.prune()
			s.regLimiter.prune()
			s.kindLimMu.Lock()
			for _, l := range s.kindLimiter {
				l.prune()
			}
			s.kindLimMu.Unlock()
		}
	}()
	return s
}

// kindThrottle returns the throttle for an envelope kind, creating it with
// the kind-appropriate rate on first use.
func (s *Server) kindThrottle(kind string) *rateLimiter {
	rate := otherKindPerMin
	if kind == "location" {
		rate = locationKindPerMin
	}
	s.kindLimMu.Lock()
	defer s.kindLimMu.Unlock()
	l, ok := s.kindLimiter[kind]
	if !ok {
		l = newRateLimiter(rate, rate)
		s.kindLimiter[kind] = l
	}
	return l
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /admin", s.handleAdmin)

	mux.HandleFunc("POST /api/v1/devices", s.handleRegisterDevice)
	mux.HandleFunc("PUT /api/v1/devices/push", s.auth(s.handleSetPushToken))
	mux.HandleFunc("GET /api/v1/me", s.auth(s.handleMe))
	mux.HandleFunc("GET /api/v1/circles", s.auth(s.handleListCircles))
	mux.HandleFunc("POST /api/v1/circles", s.auth(s.handleCreateCircle))
	mux.HandleFunc("POST /api/v1/circles/join", s.auth(s.handleJoinCircle))
	mux.HandleFunc("GET /api/v1/circles/{id}", s.auth(s.handleCircleDetail))
	mux.HandleFunc("GET /api/v1/circles/{id}/envelopes", s.auth(s.handleGetEnvelopes))
	mux.HandleFunc("GET /api/v1/circles/{id}/envelopes/latest", s.auth(s.handleLatestEnvelopes))
	mux.HandleFunc("POST /api/v1/circles/{id}/envelopes", s.auth(s.handlePostEnvelope))
	mux.HandleFunc("POST /api/v1/circles/{id}/keys", s.auth(s.handlePutKeyBlob))
	mux.HandleFunc("GET /api/v1/circles/{id}/keys/mine", s.auth(s.handleGetKeyBlob))
	mux.HandleFunc("POST /api/v1/circles/{id}/invites", s.auth(s.handleCreateInvite))
	mux.HandleFunc("POST /api/v1/circles/{id}/members/{did}/sharing", s.auth(s.handleSetSharing))
	mux.HandleFunc("DELETE /api/v1/circles/{id}/members/{did}", s.auth(s.handleRemoveMember))
	mux.HandleFunc("GET /api/v1/ws", s.auth(s.handleWebSocket))

	return recoverer(logRequests(corsHeaders(mux)))
}

// pingDB is the health-check probe: verifies the store answers within 2s.
func (s *Server) pingDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.store.Ping(ctx)
}

// ---------------------------------------------------------------------------
// middleware
// ---------------------------------------------------------------------------

type ctxKey int

const ctxDeviceKey ctxKey = 0

func deviceFrom(ctx context.Context) *store.Device {
	d, _ := ctx.Value(ctxDeviceKey).(*store.Device)
	return d
}

// recoverer converts handler panics into 500s so one bad request cannot
// take down the whole server.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic",
					"method", r.Method,
					"path", r.URL.Path,
					"err", rec,
					"stack", string(debug.Stack()),
				)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// auth resolves the Bearer token to a device and applies rate limiting.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		sum := sha256.Sum256([]byte(token))
		dev, err := s.store.DeviceByTokenHash(hex.EncodeToString(sum[:]))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if !s.limiter.allow(dev.ID) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxDeviceKey, dev)))
	}
}

// clientIP extracts the peer IP (no proxy headers — self-hosted).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// corsHeaders keeps web tools (and the future dashboard) working with the API
// and applies baseline security headers to every response.
func corsHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		// API responses carry identity-adjacent data: never let proxies or
		// browsers cache them, and never let them sniff types or frame us.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for structured request logs.
// It forwards Hijacker (needed by the WebSocket upgrade) and Flusher to the
// underlying writer.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return sr.ResponseWriter.(http.Hijacker).Hijack()
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)
		// Keep the log quiet for high-frequency location traffic (it would
		// otherwise dominate every log line); errors still surface.
		if !strings.Contains(r.URL.Path, "/envelopes") || sr.status >= 400 {
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sr.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// rateLimiter is a small in-memory token bucket per key.
type rateLimiter struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	toks  map[string]float64
	last  map[string]time.Time
}

// prune drops entries idle for over maxIdle, bounding memory when keys
// churn (rotating client IPs, many devices).
func (rl *rateLimiter) prune() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	const maxIdle = 30 * time.Minute
	now := time.Now()
	for k, t := range rl.last {
		if now.Sub(t) > maxIdle {
			delete(rl.toks, k)
			delete(rl.last, k)
		}
	}
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{rate: rate, burst: burst, toks: map[string]float64{}, last: map[string]time.Time{}}
}
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	last, ok := rl.last[key]
	if !ok {
		rl.toks[key] = rl.burst
	} else {
		rl.toks[key] += now.Sub(last).Seconds() * rl.rate
		if rl.toks[key] > rl.burst {
			rl.toks[key] = rl.burst
		}
	}
	rl.last[key] = now
	if rl.toks[key] >= 1 {
		rl.toks[key]--
		return true
	}
	return false
}

// dashboardFS exposes the embedded admin page.
func dashboardFS() (fs.FS, error) { return fs.Sub(staticFS, "static") }
