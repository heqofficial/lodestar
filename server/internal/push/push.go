// Package push delivers alert notifications to circle members.
//
// Two providers exist:
//
//   - Ntfy: posts encrypted envelopes to a self-hosted ntfy topic. The
//     payload is ciphertext, so even an untrusted ntfy relay learns nothing.
//     Members can subscribe their phones' ntfy app to the circle topic to
//     get push while Lodestar is not running.
//   - APNs: delivers alert notifications to iOS devices (Apple Push
//     Notification service) using the HTTP/2 API. Requires an Apple
//     Developer account and a .p8 key. Devices register their token via
//     the API; the server clears tokens that Apple reports unregistered.
//
// Providers are mutually exclusive by request shape: a Request with a
// Topic is an ntfy topic broadcast (APNs skips it), and a Request with a
// DeviceToken is a per-device APNs delivery (ntfy skips it).
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// Request is a push notification attempt.
type Request struct {
	// Title is a short human-readable title (ntfy only; APNs builds its own).
	Title string
	// Priority: ntfy priorities map to 1-5; APNs uses alert with sound.
	Priority int
	// Payload is the (encrypted) envelope JSON.
	Payload []byte
	// Topic is the ntfy topic (ntfy only).
	Topic string
	// DeviceToken is the APNs device token (APNs only).
	DeviceToken string
}

// Sender sends push notifications.
type Sender interface {
	Send(ctx context.Context, req Request) error
}

// ---------------------------------------------------------------------------
// ntfy
// ---------------------------------------------------------------------------

// Ntfy posts to a self-hosted ntfy-compatible server.
type Ntfy struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewNtfy returns an ntfy sender. baseURL must be the ntfy root, e.g.
// "http://localhost:80". token may be empty if the server is open.
func NewNtfy(baseURL, token string) *Ntfy {
	return &Ntfy{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts the payload to the topic. Per-device requests (no Topic)
// are not for ntfy and are skipped.
func (n *Ntfy) Send(ctx context.Context, req Request) error {
	if req.Topic == "" {
		return nil
	}
	u := n.baseURL + "/" + url.PathEscape(req.Topic)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(req.Payload))
	if err != nil {
		return fmt.Errorf("ntfy request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if req.Title != "" {
		hreq.Header.Set("Title", req.Title)
	}
	p := req.Priority
	if p < 1 || p > 5 {
		p = 3
	}
	hreq.Header.Set("Priority", fmt.Sprint(p))
	if n.token != "" {
		hreq.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.client.Do(hreq)
	if err != nil {
		return fmt.Errorf("ntfy send: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// APNs
// ---------------------------------------------------------------------------

// APNsConfig configures the Apple Push Notification service provider.
type APNsConfig struct {
	KeyPath string // path to the .p8 signing key (ES256)
	TeamID  string
	KeyID   string
	Topic   string // app bundle id
	Env     string // "development" | "production"
}

// APNs posts to Apple's HTTP/2 push API.
type APNs struct {
	cfg      APNsConfig
	key      *ecdsa.PrivateKey
	client   *http.Client
	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewAPNs loads the .p8 key and prepares the provider.
func NewAPNs(cfg APNsConfig) (*APNs, error) {
	if cfg.KeyPath == "" || cfg.TeamID == "" || cfg.KeyID == "" || cfg.Topic == "" {
		return nil, errors.New("apns: key path, team id, key id and topic are required")
	}
	pemBytes, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("apns: read key: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("apns: no PEM block in key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: key is not EC")
	}
	return &APNs{
		cfg:    cfg,
		key:    ecKey,
		client: &http.Client{Timeout: 15 * time.Second, Transport: http2Transport()},
	}, nil
}

// ErrUnregistered reports an APNs device token that Apple no longer
// accepts; the server should drop the token instead of retrying it.
var ErrUnregistered = errors.New("apns: device token unregistered")

// Send delivers an alert notification to one device. Topic broadcasts
// (no DeviceToken) are not for APNs and are skipped.
//
// The push is a wake-up signal only: the alert carries a title, and the
// app fetches the encrypted envelope over the API when opened. Embedding
// ciphertext would exceed APNs' 4 KiB payload cap for alerts.
func (a *APNs) Send(ctx context.Context, req Request) error {
	if req.DeviceToken == "" {
		return nil
	}
	jwt, err := a.providerToken()
	if err != nil {
		return err
	}
	u := fmt.Sprintf("https://%s/3/device/%s", a.host(), req.DeviceToken)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(apnsPayload(req.Title)))
	if err != nil {
		return fmt.Errorf("apns request: %w", err)
	}
	hreq.Header.Set("Authorization", "bearer "+jwt)
	hreq.Header.Set("apns-topic", a.cfg.Topic)
	hreq.Header.Set("apns-push-type", "alert")
	hreq.Header.Set("apns-priority", "10")
	resp, err := a.client.Do(hreq)
	if err != nil {
		return fmt.Errorf("apns send: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return apnsError(resp.StatusCode, body)
}

// apnsPayload builds the APNs notification JSON. The title is the only
// human-readable content; the envelope itself stays on the server.
func apnsPayload(title string) []byte {
	b, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{
				"title": title,
				"body":  "Open Lodestar",
			},
			"sound": "default",
		},
	})
	if err != nil {
		// Cannot happen: the map is static.
		return []byte(`{"aps":{}}`)
	}
	return b
}

// apnsError maps an APNs response to an error. Only 410 (Unregistered)
// and BadDeviceToken 400/403 clear the token; anything else is likely a
// transient server-side problem and must be retried, not forgotten.
func apnsError(status int, body []byte) error {
	if status == http.StatusOK {
		return nil
	}
	var r struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &r)
	if status == http.StatusGone ||
		(status == http.StatusBadRequest && r.Reason == "BadDeviceToken") ||
		(status == http.StatusForbidden && r.Reason == "BadDeviceToken") {
		return ErrUnregistered
	}
	return fmt.Errorf("apns status %d: %s", status, string(body))
}

func (a *APNs) host() string {
	if a.cfg.Env == "production" {
		return "api.push.apple.com"
	}
	return "api.development.push.apple.com"
}

// providerToken returns a cached ES256 JWT (valid ~1h, refreshed when near expiry).
func (a *APNs) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.tokenExp.Add(-5*time.Minute)) {
		return a.token, nil
	}
	now := time.Now()
	header := map[string]any{"alg": "ES256", "kid": a.cfg.KeyID}
	claims := map[string]any{
		"iss": a.cfg.TeamID,
		"iat": now.Unix(),
	}
	seg, err := jwtEncode(header, claims, a.key)
	if err != nil {
		return "", fmt.Errorf("apns: token: %w", err)
	}
	a.token = seg
	a.tokenExp = now.Add(time.Hour)
	return a.token, nil
}

// jwtEncode builds and signs a compact JWS with ES256. The `exp` claim is
// added by the caller when present in claims.
func jwtEncode(header, claims map[string]any, key *ecdsa.PrivateKey) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	size := (key.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, size*2)
	r.FillBytes(sig[:size])
	s.FillBytes(sig[size:])
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// ---------------------------------------------------------------------------
// Multi-sender
// ---------------------------------------------------------------------------

// Multi sends to every configured provider.
type Multi struct {
	senders []Sender
}

// NewMulti collects senders; empty provider lists are fine (disabled).
func NewMulti(senders ...Sender) *Multi {
	var active []Sender
	for _, s := range senders {
		if s != nil {
			active = append(active, s)
		}
	}
	return &Multi{senders: active}
}

// Enabled reports whether any provider is configured.
func (m *Multi) Enabled() bool { return len(m.senders) > 0 }

// Send delivers to all providers, logging but not failing on individual errors.
func (m *Multi) Send(ctx context.Context, req Request) error {
	var firstErr error
	for _, s := range m.senders {
		if err := s.Send(ctx, req); err != nil {
			slog.Warn("push failed", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// http2Transport returns an HTTP/2 transport for the APNs endpoint.
func http2Transport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: false,
	}
}
