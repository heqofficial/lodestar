package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hub fans out envelope messages to connected circle sockets.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*wsConn]struct{}
}

// wsConn is one live socket subscription.
type wsConn struct {
	send chan []byte
	done chan struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[string]map[*wsConn]struct{}{}}
}

// Subscribe registers a connection for a circle.
func (h *Hub) Subscribe(circleID string) *wsConn {
	c := &wsConn{send: make(chan []byte, 256), done: make(chan struct{})}
	h.mu.Lock()
	if h.subs[circleID] == nil {
		h.subs[circleID] = map[*wsConn]struct{}{}
	}
	h.subs[circleID][c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Unsubscribe removes a connection.
func (h *Hub) Unsubscribe(circleID string, c *wsConn) {
	h.mu.Lock()
	if m, ok := h.subs[circleID]; ok {
		delete(m, c)
		if len(m) == 0 {
			delete(h.subs, circleID)
		}
	}
	h.mu.Unlock()
	close(c.done)
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
	conn := s.hub.Subscribe(circleID)
	defer s.hub.Unsubscribe(circleID, conn)

	// Writer goroutine: drains the queue to the socket.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
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

	// Reader loop: consume client pings/pongs; detect dead sockets.
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
