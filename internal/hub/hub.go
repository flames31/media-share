// Package hub implements a WebSocket fan-out for pushing state and control
// events to connected player and admin clients.
package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Role identifies what a connected client is.
type Role string

const (
	RolePlayer Role = "player"
	RoleAdmin  Role = "admin"
)

// Message is the envelope sent to clients over the socket.
type Message struct {
	Type    string `json:"type"`              // e.g. "state"
	Payload any    `json:"payload,omitempty"` // e.g. a queue.Snapshot
}

type client struct {
	conn *websocket.Conn
	room string
	role Role
	send chan []byte
}

// Hub tracks connected clients, grouped into rooms, and broadcasts messages to a
// room's members. Each streamer is a room, so tenants are isolated.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}

	upgrader websocket.Upgrader

	// OnConnect returns the initial messages to send a client for its room/role.
	OnConnect func(room, role string) []Message
}

// New creates a Hub. checkOrigin controls the WebSocket origin policy; pass nil
// to allow all origins (fine for a local OBS/browser tool).
func New(checkOrigin func(*http.Request) bool) *Hub {
	if checkOrigin == nil {
		checkOrigin = func(*http.Request) bool { return true }
	}
	return &Hub{
		clients:  make(map[*client]struct{}),
		upgrader: websocket.Upgrader{CheckOrigin: checkOrigin},
	}
}

// BroadcastTo sends a message to every client in the given room.
func (h *Hub) BroadcastTo(room, msgType string, payload any) {
	data, err := json.Marshal(Message{Type: msgType, Payload: payload})
	if err != nil {
		slog.Error("hub: marshal broadcast failed", "type", msgType, "err", err)
		return
	}
	h.mu.Lock()
	for c := range h.clients {
		if c.room != room {
			continue
		}
		select {
		case c.send <- data:
		default:
			// Slow client; drop it to avoid blocking the whole broadcast.
			close(c.send)
			delete(h.clients, c)
		}
	}
	h.mu.Unlock()
}

// ServeWS upgrades an HTTP request to a WebSocket connection and registers it in
// the given room with the given role. The caller is responsible for resolving
// (and authorizing) room/role — they are never trusted from the client.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, room string, role Role) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote an error response
	}
	c := &client{conn: conn, room: room, role: role, send: make(chan []byte, 16)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	// Send the current state immediately so the new client renders without waiting.
	if h.OnConnect != nil {
		for _, m := range h.OnConnect(room, string(role)) {
			if data, err := json.Marshal(m); err == nil {
				c.send <- data
			}
		}
	}

	go h.writePump(c)
	go h.readPump(c)
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// readPump drains inbound messages (we mostly push server->client) and keeps the
// connection alive via pong handling. It exits on error, unregistering the client.
func (h *Hub) readPump(c *client) {
	defer func() {
		h.remove(c)
		c.conn.Close()
	}()
	c.conn.SetReadLimit(4096)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) writePump(c *client) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
