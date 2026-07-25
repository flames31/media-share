// Package queue implements the media-share queue and its state machine.
//
// A single Manager instance owns all queue state and is safe for concurrent use.
// Every mutating operation, after applying its change, invokes the broadcast
// callback with a fresh Snapshot so subscribers (WebSocket clients) stay live.
package queue

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// ItemType distinguishes a YouTube clip from an uploaded media file.
type ItemType string

const (
	TypeYouTube ItemType = "youtube"
	TypeUpload  ItemType = "upload"
)

// Status is the lifecycle stage of a queue item.
type Status string

const (
	StatusPending  Status = "pending"  // awaiting moderator approval
	StatusApproved Status = "approved" // in the play queue
	StatusPlaying  Status = "playing"  // currently on the player
	StatusDone     Status = "done"     // finished playing
	StatusRejected Status = "rejected" // rejected by a moderator
	StatusSkipped  Status = "skipped"  // skipped before/while playing
)

// Item is a single submission in the queue.
type Item struct {
	ID            string    `json:"id"`
	Type          ItemType  `json:"type"`
	Title         string    `json:"title"`
	SubmitterName string    `json:"submitterName,omitempty"`
	Status        Status    `json:"status"`
	SubmittedAt   time.Time `json:"submittedAt"`

	// YouTube fields
	YouTubeID    string `json:"youtubeId,omitempty"`
	StartSeconds int    `json:"startSeconds,omitempty"`

	// Upload fields
	MediaURL string `json:"mediaUrl,omitempty"` // e.g. /media/<file>

	// How long to play, in seconds. Manual placeholder for the future
	// donation -> duration formula. 0 means "play to natural end".
	DurationSeconds int `json:"durationSeconds"`
}

// Snapshot is an immutable view of the queue for broadcasting to clients.
type Snapshot struct {
	Pending    []*Item `json:"pending"`
	Queue      []*Item `json:"queue"`      // approved, ordered
	NowPlaying *Item   `json:"nowPlaying"` // nil when idle
	Paused     bool    `json:"paused"`
	Bypass     bool    `json:"bypass"`
}

// Manager owns the queue state.
type Manager struct {
	mu sync.Mutex

	pending    []*Item
	queue      []*Item
	nowPlaying *Item
	paused     bool
	bypass     bool

	// broadcast is called (without the lock held) after every change.
	broadcast func(Snapshot)
}

// NewManager returns a Manager. broadcast may be nil (set later via SetBroadcast).
func NewManager(broadcast func(Snapshot)) *Manager {
	if broadcast == nil {
		broadcast = func(Snapshot) {}
	}
	return &Manager{broadcast: broadcast}
}

// SetBroadcast replaces the broadcast callback.
func (m *Manager) SetBroadcast(fn func(Snapshot)) {
	m.mu.Lock()
	if fn == nil {
		fn = func(Snapshot) {}
	}
	m.broadcast = fn
	m.mu.Unlock()
}

// snapshotLocked builds a Snapshot; caller must hold m.mu.
func (m *Manager) snapshotLocked() Snapshot {
	return Snapshot{
		Pending:    cloneSlice(m.pending),
		Queue:      cloneSlice(m.queue),
		NowPlaying: cloneItem(m.nowPlaying),
		Paused:     m.paused,
		Bypass:     m.bypass,
	}
}

// Snapshot returns the current state.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// emit publishes a snapshot. Caller must NOT hold the lock.
func (m *Manager) emit(s Snapshot, fn func(Snapshot)) {
	fn(s)
}

// Submit adds a new item. If bypass is on, it goes straight to the approved
// queue; otherwise it lands in pending. Returns the created item.
func (m *Manager) Submit(it *Item) *Item {
	m.mu.Lock()
	if it.ID == "" {
		it.ID = uuid.NewString()
	}
	if it.SubmittedAt.IsZero() {
		it.SubmittedAt = time.Now()
	}
	if m.bypass {
		it.Status = StatusApproved
		m.queue = append(m.queue, it)
	} else {
		it.Status = StatusPending
		m.pending = append(m.pending, it)
	}
	m.startIfIdleLocked()
	snap := m.snapshotLocked()
	fn := m.broadcast
	created := cloneItem(it)
	m.mu.Unlock()

	m.emit(snap, fn)
	return created
}

// Approve moves a pending item into the approved queue.
func (m *Manager) Approve(id string) bool {
	m.mu.Lock()
	it, idx := findByID(m.pending, id)
	if it == nil {
		m.mu.Unlock()
		return false
	}
	m.pending = removeAt(m.pending, idx)
	it.Status = StatusApproved
	m.queue = append(m.queue, it)
	m.startIfIdleLocked()
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
	return true
}

// Reject drops a pending item.
func (m *Manager) Reject(id string) bool {
	m.mu.Lock()
	it, idx := findByID(m.pending, id)
	if it == nil {
		m.mu.Unlock()
		return false
	}
	m.pending = removeAt(m.pending, idx)
	it.Status = StatusRejected
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
	return true
}

// Remove deletes an item from the approved queue (not the one now playing).
func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	it, idx := findByID(m.queue, id)
	if it == nil {
		m.mu.Unlock()
		return false
	}
	m.queue = removeAt(m.queue, idx)
	it.Status = StatusSkipped
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
	return true
}

// Ended marks the current item done and advances. The optional id guards
// against a stale player reporting the end of a video that already advanced;
// pass "" to force-advance regardless.
func (m *Manager) Ended(id string) {
	m.mu.Lock()
	if m.nowPlaying != nil && (id == "" || m.nowPlaying.ID == id) {
		m.nowPlaying.Status = StatusDone
		m.nowPlaying = nil
	}
	m.paused = false
	m.startIfIdleLocked()
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
}

// Skip ends the current item immediately and advances to the next.
func (m *Manager) Skip() {
	m.mu.Lock()
	if m.nowPlaying != nil {
		m.nowPlaying.Status = StatusSkipped
		m.nowPlaying = nil
	}
	m.paused = false
	m.startIfIdleLocked()
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
}

// Pause pauses playback (player halts, queue does not advance).
func (m *Manager) Pause() { m.setPaused(true) }

// Resume resumes playback.
func (m *Manager) Resume() { m.setPaused(false) }

func (m *Manager) setPaused(p bool) {
	m.mu.Lock()
	m.paused = p
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
}

// Clear empties the approved queue. If all is true it also clears pending and
// the now-playing item.
func (m *Manager) Clear(all bool) {
	m.mu.Lock()
	for _, it := range m.queue {
		it.Status = StatusSkipped
	}
	m.queue = nil
	if all {
		for _, it := range m.pending {
			it.Status = StatusRejected
		}
		m.pending = nil
		if m.nowPlaying != nil {
			m.nowPlaying.Status = StatusSkipped
			m.nowPlaying = nil
		}
		m.paused = false
	}
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
}

// SetBypass toggles verification bypass (auto-approve submissions).
func (m *Manager) SetBypass(enabled bool) {
	m.mu.Lock()
	m.bypass = enabled
	snap := m.snapshotLocked()
	fn := m.broadcast
	m.mu.Unlock()

	m.emit(snap, fn)
}

// NowPlaying returns a copy of the currently playing item, or nil.
func (m *Manager) NowPlaying() *Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneItem(m.nowPlaying)
}

// startIfIdleLocked promotes the front of the queue to nowPlaying when nothing
// is playing. Caller must hold m.mu. Pausing does not block starting the next
// item — pause only affects an already-playing video on the client.
func (m *Manager) startIfIdleLocked() {
	if m.nowPlaying != nil || len(m.queue) == 0 {
		return
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	next.Status = StatusPlaying
	m.nowPlaying = next
}

// --- helpers ---

func findByID(items []*Item, id string) (*Item, int) {
	for i, it := range items {
		if it.ID == id {
			return it, i
		}
	}
	return nil, -1
}

func removeAt(items []*Item, idx int) []*Item {
	return append(items[:idx:idx], items[idx+1:]...)
}

func cloneItem(it *Item) *Item {
	if it == nil {
		return nil
	}
	c := *it
	return &c
}

func cloneSlice(items []*Item) []*Item {
	out := make([]*Item, 0, len(items))
	for _, it := range items {
		out = append(out, cloneItem(it))
	}
	return out
}
