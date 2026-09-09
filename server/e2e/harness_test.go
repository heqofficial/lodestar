// Package e2e boots the real lodestard binary and drives it over the
// network exactly like the mobile app does: register, circles, invites,
// envelopes, WebSocket relay, key blobs, membership, admin.
//
// This is the integration layer unit tests cannot reach: main.go flag/env
// wiring, migrations on a real DB file, graceful shutdown, and the live
// HTTP + WS stack.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/heqofficial/lodestar/server/internal/run"
)

// binPath is the compiled server binary, built once in TestMain.
var binPath string

// testPubKey is canonical base64 of 32 zero bytes — a structurally valid
// Ed25519/X25519 public key for registration fixtures.
const testPubKey = "MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA="

func TestMain(m *testing.M) {
	// Build into the package dir, not the OS temp dir: Windows Application
	// Control policies commonly block executing binaries from %%TEMP%%.
	// The "./" prefix matters: a bare name would be resolved via PATH.
	binPath = "./lodestard-e2e"
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	// Build the real binary: catches wiring bugs in cmd/lodestard (flags,
	// store open, migrations) that in-process tests cannot.
	build := exec.Command("go", "build", "-o", binPath, "../cmd/lodestard")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build lodestard:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.Remove(binPath)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// server lifecycle
// ---------------------------------------------------------------------------

// server is one live lodestard instance with a temp SQLite DB.
//
// It runs the real compiled binary; on machines whose OS refuses to
// execute freshly built binaries (e.g. Windows Smart App Control), it
// falls back to booting the exact same wiring in-process via run.Server.
type server struct {
	t      *testing.T
	url    string
	dbPath string

	cmd *exec.Cmd // exec mode only

	cancel context.CancelFunc // in-process mode only
	done   chan error         // in-process mode only

	stopOnce sync.Once
	waitErr  error
}

// startServer boots a fresh server with its own temp DB.
func startServer(t *testing.T, extraFlags ...string) *server {
	t.Helper()
	return startServerAt(t, filepath.Join(t.TempDir(), "lodestar.db"), extraFlags...)
}

// startServerAt boots a server on an existing DB file (restart tests).
func startServerAt(t *testing.T, dbPath string, extraFlags ...string) *server {
	t.Helper()
	port := freePort(t)
	args := serverArgs(port, dbPath, extraFlags)
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = os.Stderr // keep server logs visible under -v
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		if !policyBlocked(err) {
			t.Fatalf("start lodestard: %v", err)
		}
		t.Logf("OS refuses to execute the test binary (%v); running in-process", err)
		return startInProcess(t, port, dbPath, args)
	}
	s := &server{t: t, cmd: cmd, url: fmt.Sprintf("http://127.0.0.1:%d", port), dbPath: dbPath}
	t.Cleanup(func() { _ = s.stop() })
	s.waitHealth()
	return s
}

// startInProcess boots the production wiring (run.Server) in the test
// process. Only used where the OS blocks executing the built binary.
func startInProcess(t *testing.T, port int, dbPath string, args []string) *server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run.Server(ctx, args) }()
	s := &server{
		t:      t,
		url:    fmt.Sprintf("http://127.0.0.1:%d", port),
		dbPath: dbPath,
		cancel: cancel,
		done:   done,
	}
	t.Cleanup(func() { _ = s.stop() })
	s.waitHealth()
	return s
}

func serverArgs(port int, dbPath string, extraFlags []string) []string {
	return append([]string{"-addr", fmt.Sprintf("127.0.0.1:%d", port), "-data", dbPath}, extraFlags...)
}

// policyBlocked reports whether the OS refused to execute the binary for
// policy reasons (Windows Application Control / Smart App Control).
func policyBlocked(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "application control policy") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "blocked this file")
}

// freePort finds a free loopback port for the server to bind.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitHealth polls /healthz until the server answers (it also probes the DB).
func (s *server) waitHealth() {
	s.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.url + "/healthz")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("server did not become healthy")
}

// stop shuts the server down: SIGTERM on the real binary (exercises the
// graceful shutdown path), context cancel in-process. Idempotent.
func (s *server) stop() error {
	s.stopOnce.Do(func() {
		if s.cmd != nil {
			s.stopProcess()
			return
		}
		s.cancel()
		select {
		case s.waitErr = <-s.done:
		case <-time.After(10 * time.Second):
			s.waitErr = fmt.Errorf("in-process server did not stop within 10s")
		}
	})
	return s.waitErr
}

func (s *server) stopProcess() {
	if runtime.GOOS == "windows" {
		s.waitErr = s.cmd.Process.Kill()
		s.cmd.Wait()
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case s.waitErr = <-done:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		s.waitErr = <-done
	}
}

// ---------------------------------------------------------------------------
// API types (mirrors of the server's JSON)
// ---------------------------------------------------------------------------

type device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Ed25519Pub string `json:"ed25519_pub"`
	X25519Pub  string `json:"x25519_pub"`
	CreatedAt  int64  `json:"created_at"`
}

type circle struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Color         string `json:"color"`
	OwnerDeviceID string `json:"owner_device_id"`
	InviteCode    string `json:"invite_code"`
	CreatedAt     int64  `json:"created_at"`
}

type invite struct {
	Code      string `json:"code"`
	CircleID  string `json:"circle_id"`
	CreatedBy string `json:"created_by"`
	ExpiresAt int64  `json:"expires_at"`
}

type member struct {
	CircleID       string `json:"circle_id"`
	DeviceID       string `json:"device_id"`
	Role           string `json:"role"`
	DisplayName    string `json:"display_name"`
	AvatarColor    string `json:"avatar_color"`
	SharingEnabled bool   `json:"sharing_enabled"`
}

type envelope struct {
	ID         string `json:"id"`
	CircleID   string `json:"circle_id"`
	DeviceID   string `json:"device_id"`
	Kind       string `json:"kind"`
	TS         int64  `json:"ts"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	CreatedAt  int64  `json:"created_at"`
}

// ---------------------------------------------------------------------------
// HTTP client helpers
// ---------------------------------------------------------------------------

type client struct {
	t     *testing.T
	base  string
	token string
}

func newClient(t *testing.T, base string) *client {
	return &client{t: t, base: base}
}

func (c *client) withToken(token string) *client {
	return &client{t: c.t, base: c.base, token: token}
}

// do performs a request and returns (status, body).
func (c *client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatalf("request %s %s: %v", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b
}

// expect performs a request and fails the test unless the status matches.
func (c *client) expect(method, path string, body any, want int) []byte {
	c.t.Helper()
	status, b := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s: status %d, want %d (%s)", method, path, status, want, b)
	}
	return b
}

// decode performs a request, checks the status, and decodes the JSON body.
func (c *client) decode(method, path string, body any, want int, out any) {
	c.t.Helper()
	b := c.expect(method, path, body, want)
	if err := json.Unmarshal(b, out); err != nil {
		c.t.Fatalf("decode %s: %v (%s)", path, err, b)
	}
}

func (c *client) register(name string) (device, string) {
	c.t.Helper()
	var out struct {
		Device device `json:"device"`
		Token  string `json:"token"`
	}
	c.decode("POST", "/api/v1/devices", map[string]string{
		"name": name, "ed25519_pub": testPubKey, "x25519_pub": testPubKey,
	}, http.StatusCreated, &out)
	if out.Token == "" {
		c.t.Fatal("register returned an empty token")
	}
	return out.Device, out.Token
}

func (c *client) createCircle(name string) circle {
	var out circle
	c.decode("POST", "/api/v1/circles", map[string]string{"name": name}, http.StatusCreated, &out)
	return out
}

func (c *client) createInvite(circleID string) string {
	var out invite
	c.decode("POST", "/api/v1/circles/"+circleID+"/invites", map[string]int{"ttl_hours": 24}, http.StatusCreated, &out)
	return out.Code
}

func (c *client) join(code string) circle {
	var out circle
	c.decode("POST", "/api/v1/circles/join", map[string]string{"code": code}, http.StatusOK, &out)
	return out
}

func (c *client) circles() []circle {
	var out struct {
		Circles []circle `json:"circles"`
	}
	c.decode("GET", "/api/v1/circles", nil, http.StatusOK, &out)
	return out.Circles
}

func (c *client) circleDetail(circleID string) (circle, []member) {
	var out struct {
		Circle  circle   `json:"circle"`
		Members []member `json:"members"`
	}
	c.decode("GET", "/api/v1/circles/"+circleID, nil, http.StatusOK, &out)
	return out.Circle, out.Members
}

func (c *client) postEnvelope(circleID, kind, nonce string, ts int64) envelope {
	var out envelope
	c.decode("POST", "/api/v1/circles/"+circleID+"/envelopes", map[string]any{
		"kind": kind, "ts": ts, "nonce": nonce, "ciphertext": "ct-" + nonce,
	}, http.StatusCreated, &out)
	return out
}

func (c *client) envelopes(circleID, query string) []envelope {
	var out struct {
		Envelopes []envelope `json:"envelopes"`
	}
	c.decode("GET", "/api/v1/circles/"+circleID+"/envelopes"+query, nil, http.StatusOK, &out)
	return out.Envelopes
}

func (c *client) putKey(circleID, forDevice, ciphertext string) int {
	status, _ := c.do("POST", "/api/v1/circles/"+circleID+"/keys", map[string]string{
		"for_device": forDevice, "ciphertext": ciphertext,
	})
	return status
}

func (c *client) getKey(circleID string) (string, int) {
	status, b := c.do("GET", "/api/v1/circles/"+circleID+"/keys/mine", nil)
	if status != http.StatusOK {
		return "", status
	}
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		c.t.Fatalf("decode key blob: %v", err)
	}
	return out.Ciphertext, status
}

func (c *client) removeMember(circleID, deviceID string) int {
	status, _ := c.do("DELETE", "/api/v1/circles/"+circleID+"/members/"+deviceID, nil)
	return status
}

// ---------------------------------------------------------------------------
// WebSocket helpers
// ---------------------------------------------------------------------------

type wsConn struct {
	t    *testing.T
	conn *websocket.Conn
	msgs chan envelope
	errs chan error

	// pending buffers envelopes that arrived before their expected match:
	// the test posts envelopes back-to-back, so an earlier broadcast can
	// still be in flight on a socket when the next envelope is posted.
	// Per-socket order is preserved by the queue (TCP + FIFO drain).
	pending []envelope
}

// connectWS opens a live socket as the given device. The server broadcasts
// every envelope to the socket; the read loop decodes them into envelopes.
func connectWS(t *testing.T, base, token, circleID string) *wsConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(ctx, base+"/api/v1/ws?circle="+circleID,
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	w := &wsConn{t: t, conn: conn, msgs: make(chan envelope, 64), errs: make(chan error, 1)}
	go w.readLoop()
	t.Cleanup(func() { _ = conn.CloseNow() })
	return w
}

func (w *wsConn) readLoop() {
	for {
		var env envelope
		if err := wsjson.Read(context.Background(), w.conn, &env); err != nil {
			select {
			case w.errs <- err:
			default:
			}
			return
		}
		select {
		case w.msgs <- env:
		default:
		}
	}
}

// next waits for the next broadcast (or socket closure) within the timeout.
func (w *wsConn) next(within time.Duration) (envelope, error) {
	select {
	case env := <-w.msgs:
		return env, nil
	case err := <-w.errs:
		return envelope{}, err
	case <-time.After(within):
		return envelope{}, fmt.Errorf("no ws message within %s", within)
	}
}

// expect waits for the specific envelope (id+nonce+kind), buffering any
// that arrive first. Matching by identity, not position, mirrors how the
// app consumes broadcasts (it dedupes by nonce).
func (w *wsConn) expect(env envelope, within time.Duration) envelope {
	w.t.Helper()
	deadline := time.Now().Add(within)
	for {
		for i, got := range w.pending {
			if got.ID == env.ID && got.Nonce == env.Nonce && got.Kind == env.Kind {
				w.pending = append(w.pending[:i], w.pending[i+1:]...)
				return got
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.t.Fatalf("envelope %s/%s not received within %s (pending: %d)",
				env.Kind, env.Nonce, within, len(w.pending))
		}
		got, err := w.next(remaining)
		if err != nil {
			w.t.Fatalf("expect envelope: %v", err)
		}
		w.pending = append(w.pending, got)
	}
}

// expectNone asserts nothing new arrives; a stale buffered message counts.
func (w *wsConn) expectNone(within time.Duration) {
	w.t.Helper()
	if len(w.pending) > 0 {
		w.t.Fatalf("unexpected buffered ws message: %+v", w.pending[0])
	}
	if env, err := w.next(within); err == nil {
		w.t.Fatalf("unexpected ws message: %+v", env)
	}
}

// closed waits for the socket to close and returns the read error.
func (w *wsConn) closed(within time.Duration) error {
	select {
	case err := <-w.errs:
		return err
	case <-time.After(within):
		return fmt.Errorf("socket still open after %s", within)
	}
}

// ---------------------------------------------------------------------------
// ntfy stub
// ---------------------------------------------------------------------------

type pushHit struct {
	path     string
	title    string
	priority string
	body     []byte
}

// ntfyStub stands in for a real ntfy server: it records every POST so the
// test can assert the binary's push relay fired with the right shape.
func ntfyStub(t *testing.T) (*httptest.Server, func() []pushHit) {
	t.Helper()
	var mu sync.Mutex
	var hits []pushHit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mu.Lock()
		hits = append(hits, pushHit{
			path:     r.URL.Path,
			title:    r.Header.Get("Title"),
			priority: r.Header.Get("Priority"),
			body:     b,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []pushHit {
		mu.Lock()
		defer mu.Unlock()
		return append([]pushHit(nil), hits...)
	}
}
