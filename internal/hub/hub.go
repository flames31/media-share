// Package hub implements a WebSocket fan-out for pushing state and control
// events to connected player and admin clients.
package hub

import (
	"encoding/json"
	"log"
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
	role Role
	send chan []byte
}

// Hub tracks connected clients and broadcasts messages to them.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}

	upgrader websocket.Upgrader

	// StateProvider returns the current state, sent to a client on connect.
	StateProvider func() any
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

// Broadcast sends a message of the given type with the given payload to every
// connected client.
func (h *Hub) Broadcast(msgType string, payload any) {
	data, err := json.Marshal(Message{Type: msgType, Payload: payload})
	if err != nil {
		log.Printf("hub: marshal %s: %v", msgType, err)
		return
	}
	h.mu.Lock()
	for c := range h.clients {
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

// ServeWS upgrades an HTTP request to a WebSocket connection and registers it.
// The client role is read from the ?role= query parameter.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote an error response
	}
	role := Role(r.URL.Query().Get("role"))
	if role != RolePlayer && role != RoleAdmin {
		role = RolePlayer
	}
	c := &client{conn: conn, role: role, send: make(chan []byte, 16)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	// Send the current state immediately so the new client renders without waiting.
	if h.StateProvider != nil {
		if data, err := json.Marshal(Message{Type: "state", Payload: h.StateProvider()}); err == nil {
			c.send <- data
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
