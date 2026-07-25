// Package tenant provides per-streamer isolation: each streamer gets their own
// in-memory media-share queue and session, keyed by streamer id. A Registry
// lazily creates tenants and resolves anonymous invite tokens to the right one.
package tenant

import (
	"sync"

	"media-share/internal/hub"
	"media-share/internal/queue"
	"media-share/internal/session"
)

// Tenant holds one streamer's runtime state. All broadcasts are scoped to the
// hub room named by the streamer id, so only that streamer's admin/player see them.
type Tenant struct {
	StreamerID string
	Queue      *queue.Manager
	Session    *session.Manager

	reg *Registry
}

// Registry maps streamer ids to tenants and invite tokens to streamers.
type Registry struct {
	hub *hub.Hub

	mu      sync.Mutex
	tenants map[string]*Tenant
	byToken map[string]string // session invite token -> streamer id
}

// NewRegistry creates a Registry that broadcasts through h.
func NewRegistry(h *hub.Hub) *Registry {
	return &Registry{hub: h, tenants: map[string]*Tenant{}, byToken: map[string]string{}}
}

// roomBroadcaster scopes session.Manager broadcasts to a hub room.
type roomBroadcaster struct {
	hub  *hub.Hub
	room string
}

func (b roomBroadcaster) Broadcast(msgType string, payload any) {
	b.hub.BroadcastTo(b.room, msgType, payload)
}

// Get returns the tenant for streamerID, creating it on first use.
func (r *Registry) Get(streamerID string) *Tenant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tenants[streamerID]; ok {
		return t
	}
	room := streamerID
	t := &Tenant{StreamerID: streamerID, reg: r}
	t.Queue = queue.NewManager(func(s queue.Snapshot) {
		r.hub.BroadcastTo(room, "state", s)
	})
	t.Session = session.New(roomBroadcaster{hub: r.hub, room: room})
	r.tenants[streamerID] = t
	return t
}

// ResolveSession returns the tenant an invite token currently belongs to, and
// whether the token is valid (active session, matching token).
func (r *Registry) ResolveSession(token string) (*Tenant, bool) {
	if token == "" {
		return nil, false
	}
	r.mu.Lock()
	sid, ok := r.byToken[token]
	t := r.tenants[sid]
	r.mu.Unlock()
	if !ok || t == nil || !t.Session.Valid(token) {
		return nil, false
	}
	return t, true
}

// InitialMessages returns the messages to send a newly connected WS client for a
// room (streamer id) and role.
func (r *Registry) InitialMessages(room, role string) []hub.Message {
	t := r.Get(room)
	msgs := []hub.Message{{Type: "state", Payload: t.Queue.Snapshot()}}
	if role == string(hub.RoleAdmin) {
		msgs = append(msgs, hub.Message{Type: "session", Payload: t.Session.Status()})
	}
	return msgs
}

func (r *Registry) reindex(streamerID, oldToken, newToken string) {
	r.mu.Lock()
	if oldToken != "" {
		delete(r.byToken, oldToken)
	}
	if newToken != "" {
		r.byToken[newToken] = streamerID
	}
	r.mu.Unlock()
}

// --- session control that keeps the token index in sync ---

// StartSession opens the tenant's session and returns the new invite token.
func (t *Tenant) StartSession() string {
	old := t.Session.Token()
	tok := t.Session.Start()
	t.reg.reindex(t.StreamerID, old, tok)
	return tok
}

// RegenerateSession mints a new invite token (invalidating the old link).
func (t *Tenant) RegenerateSession() string {
	old := t.Session.Token()
	tok := t.Session.Regenerate()
	t.reg.reindex(t.StreamerID, old, tok)
	return tok
}

// StopSession closes the tenant's session and drops its token.
func (t *Tenant) StopSession() {
	old := t.Session.Token()
	t.Session.Stop()
	t.reg.reindex(t.StreamerID, old, "")
}
