// Package store persists platform accounts (streamers) and their login sessions
// in SQLite. Runtime media-share state (queues, invite tokens) stays in memory;
// only durable identity data lives here.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Streamer is a platform account, identified by its Twitch user id.
type Streamer struct {
	ID          string // Twitch user id
	Login       string // Twitch login (lowercase)
	DisplayName string
	PlayerKey   string // stable capability key for the OBS player URL
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the
// schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite handles concurrency best with a single writer; the busy timeout and
	// WAL mode keep concurrent readers from erroring under light load.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS streamers (
    id            TEXT PRIMARY KEY,
    login         TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    player_key    TEXT NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_sessions (
    id          TEXT PRIMARY KEY,
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_streamer ON auth_sessions(streamer_id);
`)
	return err
}

// UpsertStreamer creates the streamer on first login or updates login/display
// name on subsequent logins, always refreshing last_login_at. The player_key is
// minted once and preserved across logins. Returns the current record.
func (s *Store) UpsertStreamer(id, login, displayName string) (*Streamer, error) {
	// Truncate to seconds so in-memory values match what reads (Unix seconds) return.
	now := time.Unix(time.Now().Unix(), 0)
	existing, err := s.GetStreamer(id)
	switch {
	case err == nil:
		_, err = s.db.Exec(
			`UPDATE streamers SET login=?, display_name=?, last_login_at=? WHERE id=?`,
			login, displayName, now.Unix(), id,
		)
		if err != nil {
			return nil, err
		}
		existing.Login = login
		existing.DisplayName = displayName
		existing.LastLoginAt = now
		return existing, nil
	case errors.Is(err, ErrNotFound):
		st := &Streamer{
			ID:          id,
			Login:       login,
			DisplayName: displayName,
			PlayerKey:   randToken(16),
			CreatedAt:   now,
			LastLoginAt: now,
		}
		_, err = s.db.Exec(
			`INSERT INTO streamers (id, login, display_name, player_key, created_at, last_login_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			st.ID, st.Login, st.DisplayName, st.PlayerKey, st.CreatedAt.Unix(), st.LastLoginAt.Unix(),
		)
		if err != nil {
			return nil, err
		}
		return st, nil
	default:
		return nil, err
	}
}

// GetStreamer looks up a streamer by id.
func (s *Store) GetStreamer(id string) (*Streamer, error) {
	return s.scanStreamer(s.db.QueryRow(
		`SELECT id, login, display_name, player_key, created_at, last_login_at FROM streamers WHERE id=?`, id))
}

// GetStreamerByPlayerKey looks up a streamer by their player key.
func (s *Store) GetStreamerByPlayerKey(key string) (*Streamer, error) {
	return s.scanStreamer(s.db.QueryRow(
		`SELECT id, login, display_name, player_key, created_at, last_login_at FROM streamers WHERE player_key=?`, key))
}

func (s *Store) scanStreamer(row *sql.Row) (*Streamer, error) {
	var st Streamer
	var created, lastLogin int64
	err := row.Scan(&st.ID, &st.Login, &st.DisplayName, &st.PlayerKey, &created, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st.CreatedAt = time.Unix(created, 0)
	st.LastLoginAt = time.Unix(lastLogin, 0)
	return &st, nil
}

// CreateAuthSession issues a login session for a streamer and returns its id
// (to be stored in the browser cookie).
func (s *Store) CreateAuthSession(streamerID string, ttl time.Duration) (string, error) {
	id := randToken(32)
	now := time.Now()
	_, err := s.db.Exec(
		`INSERT INTO auth_sessions (id, streamer_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		id, streamerID, now.Unix(), now.Add(ttl).Unix(),
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetValidAuthSession returns the streamer for a non-expired session id, or
// ErrNotFound. Expired sessions are deleted opportunistically.
func (s *Store) GetValidAuthSession(sessionID string) (*Streamer, error) {
	if sessionID == "" {
		return nil, ErrNotFound
	}
	var streamerID string
	var expires int64
	err := s.db.QueryRow(
		`SELECT streamer_id, expires_at FROM auth_sessions WHERE id=?`, sessionID,
	).Scan(&streamerID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= expires {
		_ = s.DeleteAuthSession(sessionID)
		return nil, ErrNotFound
	}
	return s.GetStreamer(streamerID)
}

// DeleteAuthSession removes a login session (logout).
func (s *Store) DeleteAuthSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE id=?`, sessionID)
	return err
}

func randToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; if it does, fail loudly rather than
		// return a predictable value.
		panic(fmt.Sprintf("store: rand: %v", err))
	}
	return hex.EncodeToString(buf)
}
