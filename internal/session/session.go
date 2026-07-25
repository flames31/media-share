// Package session gates viewer submissions behind an explicit, streamer-
// controlled window. The streamer starts a session (which mints a shareable
// invite token), and stops it when done — after which the invite link and any
// submissions are rejected.
package session

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

// Broadcaster pushes status updates to connected clients. *hub.Hub satisfies it.
type Broadcaster interface {
	Broadcast(msgType string, payload any)
}

// Status is the public, non-secret view of the session (safe to broadcast).
type Status struct {
	Active    bool      `json:"active"`
	StartedAt time.Time `json:"startedAt,omitzero"`
}

// Manager holds the single active session's state. It is safe for concurrent use.
type Manager struct {
	mu          sync.Mutex
	active      bool
	token       string
	startedAt   time.Time
	broadcaster Broadcaster
}

// New creates a Manager. broadcaster may be nil.
func New(b Broadcaster) *Manager {
	return &Manager{broadcaster: b}
}

// Start opens a session with a fresh token, replacing any existing one, and
// returns the new token.
func (m *Manager) Start() string {
	m.mu.Lock()
	m.active = true
	m.token = newToken()
	m.startedAt = time.Now()
	tok := m.token
	m.mu.Unlock()

	m.broadcast()
	return tok
}

// Regenerate mints a new token for the active session (invalidating old links)
// and returns it. If no session is active, it behaves like Start.
func (m *Manager) Regenerate() string {
	m.mu.Lock()
	if !m.active {
		m.active = true
		m.startedAt = time.Now()
	}
	m.token = newToken()
	tok := m.token
	m.mu.Unlock()

	m.broadcast()
	return tok
}

// Stop closes the session and invalidates its token.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.active = false
	m.token = ""
	m.startedAt = time.Time{}
	m.mu.Unlock()

	m.broadcast()
}

// Valid reports whether the session is active and the given token matches.
func (m *Manager) Valid(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || m.token == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(m.token)) == 1
}

// Token returns the current token (admin-only; never broadcast).
func (m *Manager) Token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// Status returns the public status (no token).
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Active: m.active, StartedAt: m.startedAt}
}

func (m *Manager) broadcast() {
	if m.broadcaster != nil {
		m.broadcaster.Broadcast("session", m.Status())
	}
}

func newToken() string {
	buf := make([]byte, 18)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
