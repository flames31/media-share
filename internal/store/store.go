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
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Login-session roles. A session's streamer_id is always the tenant owner; the
// role distinguishes the owner from a delegated moderator acting on that tenant.
const (
	RoleOwner     = "owner"
	RoleModerator = "moderator"
)

// Streamer is a platform account, identified by its Twitch user id.
type Streamer struct {
	ID          string // Twitch user id
	Login       string // Twitch login (lowercase)
	DisplayName string
	PlayerKey   string // stable capability key for the OBS player URL
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// Viewer is a Twitch user who submits clips and holds per-channel credits.
type Viewer struct {
	ID          string // Twitch user id
	Login       string // Twitch login (lowercase)
	DisplayName string
	CreatedAt   time.Time
	LastSeenAt  time.Time
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
	if _, err := s.db.Exec(`
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

-- Moderator links let a streamer delegate their console. Each is an unguessable
-- capability token that resolves to the owner's streamer id (one active link per
-- streamer). Persisted so mod access survives restarts.
CREATE TABLE IF NOT EXISTS moderator_links (
    token       TEXT PRIMARY KEY,
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_moderator_links_streamer ON moderator_links(streamer_id);

-- Viewers are Twitch users who submit clips. Identity is separate from streamers:
-- a viewer has no console, only a login session and per-channel credit balances.
CREATE TABLE IF NOT EXISTS viewers (
    id           TEXT PRIMARY KEY,   -- Twitch user id
    login        TEXT NOT NULL,      -- Twitch login, lowercased
    display_name TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS viewer_sessions (
    id         TEXT PRIMARY KEY,     -- opaque random hex; the vsid cookie value
    viewer_id  TEXT NOT NULL REFERENCES viewers(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_viewer_sessions_viewer ON viewer_sessions(viewer_id);

-- Credits a viewer holds in a specific streamer's channel. Bits cheered in A's
-- channel are only spendable in A's queue, so the balance is keyed by the pair.
CREATE TABLE IF NOT EXISTS credit_balances (
    viewer_id   TEXT NOT NULL REFERENCES viewers(id) ON DELETE CASCADE,
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    credits     INTEGER NOT NULL DEFAULT 0, -- never negative
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (viewer_id, streamer_id)
);

-- Every processed EventSub message id, so a redelivered/replayed cheer can never
-- credit twice. Inserted in the same transaction as the credit.
CREATE TABLE IF NOT EXISTS bits_events (
    message_id  TEXT PRIMARY KEY,    -- Twitch-Eventsub-Message-Id
    received_at INTEGER NOT NULL
);

-- Append-only audit trail of every balance change (earn or spend).
CREATE TABLE IF NOT EXISTS credit_ledger (
    id          TEXT PRIMARY KEY,
    viewer_id   TEXT NOT NULL,
    streamer_id TEXT NOT NULL,
    delta       INTEGER NOT NULL,    -- + earn / - spend
    reason      TEXT NOT NULL,       -- 'cheer' | 'submit' | 'dev_grant'
    ref         TEXT,                -- message id or queue item id, when relevant
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_viewer ON credit_ledger(viewer_id, streamer_id);
`); err != nil {
		return err
	}

	// Additive migration: add the role column to auth_sessions. Idempotent —
	// SQLite reports "duplicate column name" once the column already exists.
	if _, err := s.db.Exec(
		`ALTER TABLE auth_sessions ADD COLUMN role TEXT NOT NULL DEFAULT 'owner'`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
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

// CreateAuthSession issues a login session for a streamer (as the given role) and
// returns its id (to be stored in the browser cookie). For a moderator session,
// streamerID is the OWNER of the tenant being moderated.
func (s *Store) CreateAuthSession(streamerID, role string, ttl time.Duration) (string, error) {
	id := randToken(32)
	now := time.Now()
	_, err := s.db.Exec(
		`INSERT INTO auth_sessions (id, streamer_id, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		id, streamerID, role, now.Unix(), now.Add(ttl).Unix(),
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetValidAuthSession returns the streamer (the tenant owner) and the caller's
// role for a non-expired session id, or ErrNotFound. Expired sessions are deleted
// opportunistically.
func (s *Store) GetValidAuthSession(sessionID string) (*Streamer, string, error) {
	if sessionID == "" {
		return nil, "", ErrNotFound
	}
	var streamerID, role string
	var expires int64
	err := s.db.QueryRow(
		`SELECT streamer_id, role, expires_at FROM auth_sessions WHERE id=?`, sessionID,
	).Scan(&streamerID, &role, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if time.Now().Unix() >= expires {
		_ = s.DeleteAuthSession(sessionID)
		return nil, "", ErrNotFound
	}
	st, err := s.GetStreamer(streamerID)
	if err != nil {
		return nil, "", err
	}
	return st, role, nil
}

// DeleteAuthSession removes a login session (logout).
func (s *Store) DeleteAuthSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE id=?`, sessionID)
	return err
}

// --- moderator links ---

// ModeratorLink returns the streamer's current moderator link token, or
// ErrNotFound if none is active.
func (s *Store) ModeratorLink(streamerID string) (string, error) {
	var token string
	err := s.db.QueryRow(
		`SELECT token FROM moderator_links WHERE streamer_id=?`, streamerID,
	).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

// RegenerateModeratorLink mints a fresh moderator link for the streamer,
// replacing any existing one, and returns the new token. It does NOT touch
// existing moderator sessions — use RevokeModeratorAccess for that.
func (s *Store) RegenerateModeratorLink(streamerID string) (string, error) {
	token := randToken(24)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM moderator_links WHERE streamer_id=?`, streamerID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO moderator_links (token, streamer_id, created_at) VALUES (?, ?, ?)`,
		token, streamerID, time.Now().Unix(),
	); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveModeratorLink returns the owner streamer id a moderator link token maps
// to, or ErrNotFound if the token is unknown/revoked.
func (s *Store) ResolveModeratorLink(token string) (string, error) {
	if token == "" {
		return "", ErrNotFound
	}
	var streamerID string
	err := s.db.QueryRow(
		`SELECT streamer_id FROM moderator_links WHERE token=?`, token,
	).Scan(&streamerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return streamerID, nil
}

// RevokeModeratorAccess deletes the streamer's moderator link AND all existing
// moderator login sessions for that streamer, so revoking cuts off current
// moderators immediately. The owner's own session is left untouched.
func (s *Store) RevokeModeratorAccess(streamerID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM moderator_links WHERE streamer_id=?`, streamerID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM auth_sessions WHERE streamer_id=? AND role=?`, streamerID, RoleModerator,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// --- viewers ---

// UpsertViewer creates the viewer on first sight or refreshes login/display name,
// always updating last_seen_at. Returns the current record.
func (s *Store) UpsertViewer(id, login, displayName string) (*Viewer, error) {
	now := time.Unix(time.Now().Unix(), 0)
	existing, err := s.GetViewer(id)
	switch {
	case err == nil:
		if _, err := s.db.Exec(
			`UPDATE viewers SET login=?, display_name=?, last_seen_at=? WHERE id=?`,
			login, displayName, now.Unix(), id,
		); err != nil {
			return nil, err
		}
		existing.Login = login
		existing.DisplayName = displayName
		existing.LastSeenAt = now
		return existing, nil
	case errors.Is(err, ErrNotFound):
		v := &Viewer{ID: id, Login: login, DisplayName: displayName, CreatedAt: now, LastSeenAt: now}
		if _, err := s.db.Exec(
			`INSERT INTO viewers (id, login, display_name, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
			v.ID, v.Login, v.DisplayName, v.CreatedAt.Unix(), v.LastSeenAt.Unix(),
		); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, err
	}
}

// GetViewer looks up a viewer by id.
func (s *Store) GetViewer(id string) (*Viewer, error) {
	var v Viewer
	var created, lastSeen int64
	err := s.db.QueryRow(
		`SELECT id, login, display_name, created_at, last_seen_at FROM viewers WHERE id=?`, id,
	).Scan(&v.ID, &v.Login, &v.DisplayName, &created, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.CreatedAt = time.Unix(created, 0)
	v.LastSeenAt = time.Unix(lastSeen, 0)
	return &v, nil
}

// CreateViewerSession issues a viewer login session and returns its id (the vsid
// cookie value).
func (s *Store) CreateViewerSession(viewerID string, ttl time.Duration) (string, error) {
	id := randToken(32)
	now := time.Now()
	if _, err := s.db.Exec(
		`INSERT INTO viewer_sessions (id, viewer_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		id, viewerID, now.Unix(), now.Add(ttl).Unix(),
	); err != nil {
		return "", err
	}
	return id, nil
}

// GetValidViewerSession returns the viewer for a non-expired session id, or
// ErrNotFound. Expired sessions are deleted opportunistically.
func (s *Store) GetValidViewerSession(sessionID string) (*Viewer, error) {
	if sessionID == "" {
		return nil, ErrNotFound
	}
	var viewerID string
	var expires int64
	err := s.db.QueryRow(
		`SELECT viewer_id, expires_at FROM viewer_sessions WHERE id=?`, sessionID,
	).Scan(&viewerID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= expires {
		_ = s.DeleteViewerSession(sessionID)
		return nil, ErrNotFound
	}
	return s.GetViewer(viewerID)
}

// DeleteViewerSession removes a viewer login session (logout).
func (s *Store) DeleteViewerSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM viewer_sessions WHERE id=?`, sessionID)
	return err
}

// --- credits ---

// CreditBits credits a viewer's per-channel balance for a verified cheer. The
// message id, credit, and ledger entry are written in one transaction; a repeated
// message id is a no-op (returns credited=false) so replays never double-credit.
// The viewer identity is upserted from the event so a balance can exist before the
// viewer has ever logged in.
func (s *Store) CreditBits(msgID, viewerID, login, displayName, streamerID string, bits int64) (credited bool, err error) {
	if bits <= 0 {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Dedup: the message id is the primary key, so a duplicate insert fails and we
	// treat the event as already processed.
	if _, err := tx.Exec(
		`INSERT INTO bits_events (message_id, received_at) VALUES (?, ?)`,
		msgID, time.Now().Unix(),
	); err != nil {
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}

	now := time.Now().Unix()
	if _, err := tx.Exec(
		`INSERT INTO viewers (id, login, display_name, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET login=excluded.login, display_name=excluded.display_name`,
		viewerID, login, displayName, now, now,
	); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT INTO credit_balances (viewer_id, streamer_id, credits, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(viewer_id, streamer_id) DO UPDATE SET credits = credits + excluded.credits, updated_at = excluded.updated_at`,
		viewerID, streamerID, bits, now,
	); err != nil {
		return false, err
	}
	if err := insertLedger(tx, viewerID, streamerID, bits, "cheer", msgID, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SpendCredits atomically deducts cost from a viewer's per-channel balance. ok is
// false (with the balance left untouched) when the viewer lacks enough credits.
// The conditional UPDATE guarantees the balance never goes negative even under
// concurrent submits. ref is the queue item id, recorded in the ledger.
func (s *Store) SpendCredits(viewerID, streamerID string, cost int64, ref string) (newBalance int64, ok bool, err error) {
	if cost < 0 {
		return 0, false, errors.New("negative cost")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE credit_balances SET credits = credits - ?, updated_at = ?
		 WHERE viewer_id=? AND streamer_id=? AND credits >= ?`,
		cost, time.Now().Unix(), viewerID, streamerID, cost,
	)
	if err != nil {
		return 0, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if affected != 1 {
		// Either no balance row yet or insufficient funds. Report the current
		// balance (0 when absent) so callers can tell the viewer how short they are.
		var bal int64
		_ = tx.QueryRow(
			`SELECT credits FROM credit_balances WHERE viewer_id=? AND streamer_id=?`, viewerID, streamerID,
		).Scan(&bal)
		return bal, false, nil
	}
	if cost > 0 {
		if err := insertLedger(tx, viewerID, streamerID, -cost, "submit", ref, time.Now().Unix()); err != nil {
			return 0, false, err
		}
	}
	var bal int64
	if err := tx.QueryRow(
		`SELECT credits FROM credit_balances WHERE viewer_id=? AND streamer_id=?`, viewerID, streamerID,
	).Scan(&bal); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return bal, true, nil
}

// Balance returns the viewer's credits in a streamer's channel (0 when none).
func (s *Store) Balance(viewerID, streamerID string) (int64, error) {
	var bal int64
	err := s.db.QueryRow(
		`SELECT credits FROM credit_balances WHERE viewer_id=? AND streamer_id=?`, viewerID, streamerID,
	).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return bal, nil
}

// GrantCredits adds credits to a viewer's per-channel balance without a Twitch
// event. It exists for dev/testing (see the DEV_LOGIN-gated endpoint) and records
// a ledger entry with reason 'dev_grant'.
func (s *Store) GrantCredits(viewerID, streamerID string, amount int64) error {
	if amount <= 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(
		`INSERT INTO credit_balances (viewer_id, streamer_id, credits, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(viewer_id, streamer_id) DO UPDATE SET credits = credits + excluded.credits, updated_at = excluded.updated_at`,
		viewerID, streamerID, amount, now,
	); err != nil {
		return err
	}
	if err := insertLedger(tx, viewerID, streamerID, amount, "dev_grant", "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func insertLedger(tx *sql.Tx, viewerID, streamerID string, delta int64, reason, ref string, now int64) error {
	_, err := tx.Exec(
		`INSERT INTO credit_ledger (id, viewer_id, streamer_id, delta, reason, ref, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		randToken(16), viewerID, streamerID, delta, reason, ref, now,
	)
	return err
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/primary-key conflict.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
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
