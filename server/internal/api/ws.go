package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hub fans out envelope messages to connected circle sockets.
type Hub struct {
	mu     sync.RWMutex
	subs   map[string]map[*wsConn]struct{}
	device map[*wsConn]string // conn -> device id (for revocation kicks)
}

// maxConnsPerDevice caps how many live sockets one device may hold per
// circle. Without a cap, a hostile member could open hundreds of sockets
// and amplify every broadcast into hundreds of queued copies (each up to
// 64 KiB), exhausting server memory.
const maxConnsPerDevice = 2

// wsConn is one live socket subscription.
type wsConn struct {
	send chan []byte
	done chan struct{}
	conn *websocket.Conn // underlying socket (closed on Kick)
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		subs:   map[string]map[*wsConn]struct{}{},
		device: map[*wsConn]string{},
	}
}

// Subscribe registers a connection for a circle. Returns nil when the
// device already holds the per-device socket cap (the caller should reject
// the upgrade).
func (h *Hub) Subscribe(circleID, deviceID string, conn *websocket.Conn) *wsConn {
	c := &wsConn{send: make(chan []byte, 256), done: make(chan struct{}), conn: conn}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for existing := range h.subs[circleID] {
		if h.device[existing] == deviceID {
			n++
		}
	}
	if n >= maxConnsPerDevice {
		return nil
	}
	if h.subs[circleID] == nil {
		h.subs[circleID] = map[*wsConn]struct{}{}
	}
	h.subs[circleID][c] = struct{}{}
	h.device[c] = deviceID
	return c
}

// Unsubscribe removes a connection. Idempotent: only the code that removes
// a conn from the map closes its done channel, so Kick + deferred
// Unsubscribe cannot double-close.
func (h *Hub) Unsubscribe(circleID string, c *wsConn) {
	h.mu.Lock()
	if _, ok := h.subs[circleID][c]; ok {
		delete(h.subs[circleID], c)
		if len(h.subs[circleID]) == 0 {
			delete(h.subs, circleID)
		}
		delete(h.device, c)
		close(c.done)
	}
	h.mu.Unlock()
}

// Kick closes every live socket of deviceID in circleID. Called when a
// member is removed or leaves, so a revoked member cannot keep receiving
// the circle's location stream.
//
// The socket itself is closed, not just unsubscribed: the reader loop is
// blocked in wsjson.Read (up to 90s) and would otherwise hold the
// connection — and the kicked client would never learn it lost access.
func (h *Hub) Kick(circleID, deviceID string) {
	h.mu.Lock()
	var victims []*wsConn
	for c := range h.subs[circleID] {
		if h.device[c] == deviceID {
			delete(h.subs[circleID], c)
			delete(h.device, c)
			close(c.done)
			victims = append(victims, c)
		}
	}
	if len(h.subs[circleID]) == 0 {
		delete(h.subs, circleID)
	}
	h.mu.Unlock()
	// Close outside the lock: it's a network op, and holding the hub lock
	// would block every broadcast while a slow peer drains its close frame.
	for _, c := range victims {
		// Close unblocks the reader and writer goroutines; both treat
		// the error as "leave". Safe to call concurrently with the
		// handler's deferred Unsubscribe (map membership is already gone).
		_ = c.conn.Close(websocket.StatusPolicyViolation, "removed from circle")
	}
}

// Broadcast queues msg to every socket in the circle, dropping full queues.
func (h *Hub) Broadcast(circleID string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.subs[circleID] {
		select {
		case c.send <- msg:
		default:
			// Slow subscriber: drop rather than block the server.
		}
	}
}

// handleWebSocket upgrades a circle member's request to a live stream.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	circleID := r.URL.Query().Get("circle")
	if circleID == "" {
		writeErr(w, http.StatusBadRequest, "missing circle param")
		return
	}
	dev := deviceFrom(r.Context())
	if _, err := s.mustBeMember(w, circleID, dev.ID); err != nil {
		return
	}
	// Loopback/LAN self-hosting: skip the origin check (native clients
	// don't send an Origin; browser tools may connect from anywhere).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn := s.hub.Subscribe(circleID, dev.ID, c)
	if conn == nil {
		_ = c.Close(websocket.StatusPolicyViolation, "too many connections")
		return
	}
	defer s.hub.Unsubscribe(circleID, conn)

	// Writer goroutine: drains the queue to the socket. Recover is cheap
	// insurance here: a panic in this goroutine would escape the request
	// handler's recoverer (it runs on a different goroutine) and take the
	// whole server process down with it.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("ws writer panic", "err", rec, "circle", circleID, "device", dev.ID)
			}
		}()
		for {
			select {
			case msg := <-conn.send:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := wsjson.Write(ctx, c, jsonRaw(msg)); err != nil {
					cancel()
					return
				}
				cancel()
			case <-conn.done:
				return
			}
		}
	}()

	// Reader loop: consumes the app's keepalive messages and detects dead
	// sockets. NB: in coder/websocket the read deadline is fixed per Read
	// call and protocol pings do NOT reset it — only a completed Read does.
	// The app therefore sends a small JSON keepalive every ~25s, so a
	// healthy socket always re-arms the 90s window.
	for {
		ctx, cancel := context.WithTimeout(context.Background(), wsIdleTimeout)
		var msg any
		err := wsjson.Read(ctx, c, &msg)
		cancel()
		if err != nil {
			break
		}
	}
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	<-writerDone
}

// jsonRaw wraps already-serialized JSON for wsjson (it re-marshals to the
// same bytes since json.RawMessage passes through verbatim).
func jsonRaw(b []byte) json.RawMessage { return json.RawMessage(b) }

// wsIdleTimeout is how long a socket may go without a data message before
// the server closes it. A var so tests can shrink the window; the app sends
// a small JSON keepalive every ~25s, which completes a Read and re-arms it.
var wsIdleTimeout = 90 * time.Second
