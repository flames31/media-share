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
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE, -- the TENANT OWNER
    role        TEXT NOT NULL DEFAULT 'owner', -- 'owner' | 'moderator' (the caller's role)
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX idx_auth_sessions_streamer ON auth_sessions(streamer_id);

-- One active moderator invite link per streamer. Unguessable capability token
-- that resolves to the owner's streamer id. Persisted (mod access is account
-- config, not ephemeral runtime state).
CREATE TABLE moderator_links (
    token       TEXT PRIMARY KEY,
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_moderator_links_streamer ON moderator_links(streamer_id);

-- Viewers are Twitch users who submit clips. A viewer identity is SEPARATE from a
-- streamer's: no console, just a login session (vsid) and per-channel credits.
CREATE TABLE viewers (
    id           TEXT PRIMARY KEY,   -- Twitch user id
    login        TEXT NOT NULL,      -- Twitch login, lowercased
    display_name TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);
CREATE TABLE viewer_sessions (
    id         TEXT PRIMARY KEY,     -- opaque random hex; the vsid cookie value
    viewer_id  TEXT NOT NULL REFERENCES viewers(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_viewer_sessions_viewer ON viewer_sessions(viewer_id);

-- Credits a viewer holds in ONE streamer's channel. Bits cheered to A are only
-- spendable in A's queue, so the balance is keyed by the (viewer, streamer) pair.
CREATE TABLE credit_balances (
    viewer_id   TEXT NOT NULL REFERENCES viewers(id) ON DELETE CASCADE,
    streamer_id TEXT NOT NULL REFERENCES streamers(id) ON DELETE CASCADE,
    credits     INTEGER NOT NULL DEFAULT 0, -- never negative
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (viewer_id, streamer_id)
);

-- Every processed EventSub message id, so a redelivered/replayed cheer can never
-- credit twice. Inserted in the SAME transaction as the credit.
CREATE TABLE bits_events (
    message_id  TEXT PRIMARY KEY,    -- Twitch-Eventsub-Message-Id
    received_at INTEGER NOT NULL
);

-- Append-only audit trail of every balance change (earn or spend).
CREATE TABLE credit_ledger (
    id          TEXT PRIMARY KEY,
    viewer_id   TEXT NOT NULL,
    streamer_id TEXT NOT NULL,
    delta       INTEGER NOT NULL,    -- + earn / - spend
    reason      TEXT NOT NULL,       -- 'cheer' | 'submit' | 'dev_grant'
    ref         TEXT,                -- message id or queue item id, when relevant
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_credit_ledger_viewer ON credit_ledger(viewer_id, streamer_id);
```

> **Credits are per-channel.** A viewer earns credits by cheering bits in a
> streamer's channel (1 bit = 1 credit, credited only inside the HMAC-verified
> EventSub webhook), and spends them queuing clips in *that* channel
> (`duration × CREDITS_PER_SECOND`). Earning and spending both mutate the balance
> **server-side only**: crediting is one transaction with the dedup row so replays
> can't double-credit; spending is a single conditional `UPDATE … WHERE credits >=
> cost` so a balance never goes negative even under concurrent submits.

> **`auth_sessions.role` and moderators.** A session's `streamer_id` is always the
> **tenant owner**. `role` distinguishes the owner from a delegated **moderator**
> who is running that owner's console. A moderator has no account of their own —
> claiming a moderator link just creates an `auth_sessions` row for the owner with
> `role='moderator'`. Because tenant resolution keys off `streamer_id`, moderators
> flow through the same tenant/WS path as the owner with no special-casing;
> `role` only gates owner-only actions (managing moderator links) and tweaks the
> admin UI. The `role` column is added by an **idempotent additive migration**
> (`ALTER TABLE … ADD COLUMN`, "duplicate column" swallowed).

Store methods (all parameterized):

- `UpsertStreamer(id, login, displayName)` — create on first login (mints
  `player_key = randToken(16)`), else refresh login/display/last_login. Preserves
  `player_key`. Truncates `now` to whole seconds so in-memory values match reads.
- `GetStreamer(id)`, `GetStreamerByPlayerKey(key)` — `ErrNotFound` when missing.
- `CreateAuthSession(streamerID, role, ttl)` → session id (30-day TTL from `auth`).
- `GetValidAuthSession(id)` → `(*Streamer, role, error)` — the streamer (tenant
  owner) and caller's role, or `ErrNotFound`; deletes the row opportunistically
  when expired.
- `DeleteAuthSession(id)` — logout.
- `ModeratorLink(streamerID)` → current mod link token or `ErrNotFound`.
- `RegenerateModeratorLink(streamerID)` → mint a fresh token, replacing any
  existing one (does not touch existing mod sessions).
- `ResolveModeratorLink(token)` → owner streamer id, or `ErrNotFound`.
- `RevokeModeratorAccess(streamerID)` → delete the link **and** all `role='moderator'`
  sessions for that streamer (kick current mods; owner session untouched).

Roles are `store.RoleOwner` / `store.RoleModerator`.

Viewer & credit methods (all parameterized):

- `UpsertViewer(id, login, displayName)` / `GetViewer(id)`.
- `CreateViewerSession(viewerID, ttl)` → `vsid`; `GetValidViewerSession(id)` →
  `*Viewer` (opportunistic expiry delete); `DeleteViewerSession(id)`.
- `CreditBits(msgID, viewerID, login, name, streamerID, bits)` → `(credited, err)`
  — one tx: insert dedup row (duplicate ⇒ `credited=false`), upsert the viewer,
  `credits += bits`, ledger `reason='cheer'`.
- `SpendCredits(viewerID, streamerID, cost, ref)` → `(newBalance, ok, err)` —
  conditional `UPDATE … WHERE credits >= cost`; `ok=false` (balance untouched) on
  insufficient funds; ledger `reason='submit'`.
- `Balance(viewerID, streamerID)` → credits (0 when none).
- `GrantCredits(viewerID, streamerID, amount)` — dev/testing top-up; ledger
  `reason='dev_grant'`.

The `Viewer` struct mirrors `Streamer` (`ID`, `Login`, `DisplayName`, `CreatedAt`,
`LastSeenAt`).

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
| `vsid` (viewer session) | `store.CreateViewerSession` (32B) | 30 days, server-checked | Browser cookie (HttpOnly) | Authenticate a viewer (separate identity). |
| `player_key` | `store.UpsertStreamer` (16B) | Permanent, per account | In `/p/<key>` URL | Stable OBS player address. |
| moderator link `token` | `store.RegenerateModeratorLink` (24B) | Until regenerate/revoke (persisted) | Owner admin + `/mod/<token>` | Delegate the console to a moderator (no login). |
| invite `token` | `session.newToken` (18B) | Until stop/regenerate/restart | Admin view + `/s/<token>` | Let anonymous viewers submit. |
| OAuth `state` | `auth.AuthorizeURL` (24B) | 10 min, single-use | Twitch redirect round-trip | CSRF protection for login. |

All are `crypto/rand` hex. The login `sid` is a bearer secret (keep it HttpOnly);
`player_key` and invite `token` are shareable capability URLs by design.
