# Data model

## Persistent — SQLite (`internal/store`)

Only durable identity data lives in SQLite. DB path is `DB_PATH` (default
`./data/media-share.db`). Opened with `MaxOpenConns(1)`, WAL mode,
`busy_timeout=5000`, `foreign_keys=ON`. Timestamps are stored as Unix seconds.

```sql
CREATE TABLE streamers (
    id            TEXT PRIMARY KEY,   -- Twitch user id (the dev account uses "dev")
    login         TEXT NOT NULL,      -- Twitch login, lowercased
    display_name  TEXT NOT NULL,
    player_key    TEXT NOT NULL UNIQUE, -- stable capability key for /p/<key>; minted once
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL
);

CREATE TABLE auth_sessions (
    id          TEXT PRIMARY KEY,     -- opaque random 32-byte hex; this is the cookie value
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX idx_auth_sessions_streamer ON auth_sessions(streamer_id);
```

Store methods (all parameterized):

- `UpsertStreamer(id, login, displayName)` — create on first login (mints
  `player_key = randToken(16)`), else refresh login/display/last_login. Preserves
  `player_key`. Truncates `now` to whole seconds so in-memory values match reads.
- `GetStreamer(id)`, `GetStreamerByPlayerKey(key)` — `ErrNotFound` when missing.
- `CreateAuthSession(streamerID, ttl)` → session id (30-day TTL from `auth`).
- `GetValidAuthSession(id)` — returns the streamer, or `ErrNotFound`; deletes the
  row opportunistically when expired.
- `DeleteAuthSession(id)` — logout.

**What is NOT persisted:** queue items, invite tokens, session active-state,
now-playing, pause/bypass. All ephemeral (see below).

## Ephemeral — in memory

### `tenant.Registry`
- `tenants: map[streamerID]*Tenant` — lazily created by `Get`.
- `byToken: map[inviteToken]streamerID` — kept in sync by `reindex`, driven by
  `Tenant.StartSession/RegenerateSession/StopSession`.

### `queue.Manager` (one per tenant)
```go
pending    []*Item   // awaiting moderation
queue      []*Item   // approved, ordered
nowPlaying *Item     // nil when idle
paused     bool
bypass     bool
```

`Item` (JSON-tagged; also the WS payload shape):
```go
type Item struct {
    ID              string    // uuid
    Type            ItemType  // "youtube" | "upload"
    Title           string
    SubmitterName   string    // optional, ≤40 chars
    Status          Status    // see state machine below
    SubmittedAt     time.Time
    YouTubeID       string    // youtube only
    StartSeconds    int       // youtube only
    MediaURL        string    // upload only, e.g. /media/<uuid>.mp4
    DurationSeconds int       // play length; 0 = natural end. Payment placeholder.
}
```

`Snapshot` (the `state` broadcast payload): `{pending, queue, nowPlaying, paused,
bypass}` — all deep-cloned so subscribers can't mutate live state.

### Queue state machine (`queue.Status`)

```
                 approve
   pending ───────────────► approved ──(front, idle)──► playing ──ended──► done
      │                        │                           │
      │ reject                 │ remove                    │ skip
      ▼                        ▼                           ▼
   rejected                 skipped                     skipped
```

- `Submit` → `pending` (or `approved` directly if `bypass`).
- `startIfIdleLocked` promotes `queue[0]` → `playing` whenever nothing is playing.
- `Ended(id)` → `done` + advance (id-guarded against stale players).
- `Skip`/`Remove`/`Clear` → `skipped`; `Reject`/`Clear(all)` pending → `rejected`.

### `session.Manager` (one per tenant)
```go
active    bool
token     string     // secret invite token; never broadcast
startedAt time.Time
```
Public `Status{Active, StartedAt}` is safe to broadcast; `Token()` is admin-only.
`Valid(token)` uses a constant-time compare and checks `active`.

## Capability tokens & keys — at a glance

| Value | Minted by | Lifetime | Exposed to | Purpose |
| --- | --- | --- | --- | --- |
| `sid` (login session) | `store.CreateAuthSession` (32B) | 30 days, server-checked | Browser cookie (HttpOnly) | Authenticate a streamer. |
| `player_key` | `store.UpsertStreamer` (16B) | Permanent, per account | In `/p/<key>` URL | Stable OBS player address. |
| invite `token` | `session.newToken` (18B) | Until stop/regenerate/restart | Admin view + `/s/<token>` | Let anonymous viewers submit. |
| OAuth `state` | `auth.AuthorizeURL` (24B) | 10 min, single-use | Twitch redirect round-trip | CSRF protection for login. |

All are `crypto/rand` hex. The login `sid` is a bearer secret (keep it HttpOnly);
`player_key` and invite `token` are shareable capability URLs by design.
