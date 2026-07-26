# Conventions & invariants

The non-negotiables and the idioms. Breaking one of these usually compiles fine
and fails subtly (wrong tenant, dead links, deadlock, data race). Read before
editing the domain or auth packages.

## Invariants (correctness / security)

### 1. Tenant scope is always derived server-side
Admin scope comes from the login cookie; player scope from the `player_key`;
viewer scope from the invite `token`. **Never** read a room / streamer id /
tenant from client input.

- Admin handlers: `server.tenant(r)` → `reg.Get(StreamerFrom(ctx).ID)`.
- WS rooms: resolved only in `server.handleWS` (cookie for admin, key for player).
- If you find yourself passing a streamer id or room in a request body/query,
  stop — that's the bug.

### 1b. A session's `streamer_id` is the tenant owner; `role` is the caller
Moderators have no account. Their login session stores the **owner's**
`streamer_id` with `role="moderator"`, so `server.tenant(r)` and WS rooms resolve
to the owner's tenant unchanged. Consequences to respect:
- Gate owner-only endpoints with `auth.RequireOwner` (not `RequireStreamer`) —
  currently just the `moderators/*` link-management routes. Everything else is
  fine for both roles.
- Never derive the tenant from the *role* or from anything client-supplied; it's
  always `StreamerFrom(ctx).ID`.
- **Revoke must actually kick:** `RevokeModeratorAccess` deletes the link **and**
  the `role='moderator'` sessions. If you add another way to grant mod access,
  make revoke cover it too.

### 2. Session control goes through the `Tenant`, not the `Manager`
Use `Tenant.StartSession() / RegenerateSession() / StopSession()`. These call the
underlying `session.Manager` **and** update the registry's `byToken` index via
`reindex`. Calling `t.Session.Start()` directly opens a session whose token no
`/s/<token>` request can resolve. There is no compiler protection here — it's a
discipline.

### 3. Broadcast outside the lock
Every mutating method in `queue.Manager` and `session.Manager` follows:
```go
m.mu.Lock()
// ...mutate...
snap := m.snapshotLocked()   // build payload under the lock
fn := m.broadcast            // capture the callback under the lock
m.mu.Unlock()                // release
m.emit(snap, fn)             // THEN broadcast
```
Never call the broadcast callback while holding `m.mu`. A slow WebSocket client
must never be able to stall the domain.

### 4. Every state change re-emits a full snapshot
Clients are dumb and stateless: they render whatever the latest `state`/`session`
message says. So every mutation broadcasts a fresh, fully-populated
`Snapshot`/`Status`. Don't invent partial/delta messages — keep the model simple.

### 5. Snapshots are deep copies
`snapshotLocked` clones items (`cloneItem`/`cloneSlice`) so subscribers can't
mutate live state through a shared pointer. If you add a field that's a
slice/map/pointer, clone it too.

### 6. The submit handler is the authoritative gate
`handleSubmit` re-resolves the token via `reg.ResolveSession`. The page-level
`/api/session/check` is UX only. Any new submission path must re-check server-side.

### 7. Secrets vs. capability URLs
- `sid` (login cookie) is a **bearer secret**: HttpOnly, never rendered, never
  logged, never sent in a URL.
- `player_key` and invite `token` are **capability URLs**: unguessable, meant to
  be shared, fine to put in a URL. The invite token is exposed only to the
  authenticated streamer (via `adminSessionView`) and never broadcast.
- All are `crypto/rand`. Use `subtle.ConstantTimeCompare` for token equality
  (see `session.Valid`).

### 8. Parameterized SQL only
Every query in `store` uses `?` placeholders. Never build SQL by string
concatenation.

## Idioms (match the surrounding code)

- **Errors to clients:** `writeErr(w, status, msg)` for JSON APIs
  (`{"error": msg}`); `http.Error` / `http.Redirect` for pages. `writeJSON` for
  success bodies.
- **Templates:** rendered through `server.render`, which executes into a buffer
  first so a template error can't emit a half-written response. Data is a
  `map[string]any`.
- **New env var:** add a field + reader in `config.go` (`env`/`envInt`/`envBool`),
  document it in `.env.example` and the README config table. `.env` auto-loads;
  real env wins.
- **Routing:** method + pattern in one string (`"POST /api/admin/x"`); path params
  via `r.PathValue("name")`. Keep the route table in `routes()` grouped by
  concern, as it is now.
- **IDs:** queue item ids are `uuid.NewString()`. Random tokens/keys are
  `crypto/rand` hex (`store.randToken`, `session.newToken`, `auth.randHex`).
- **Time:** store as Unix seconds; truncate to whole seconds before comparing
  in-memory values to DB reads (`store.UpsertStreamer`).
- **Logging:** structured `log/slog` through the default logger
  (`slog.Info/Warn/Error/Debug`, key/value attrs — never `fmt`-interpolate into the
  message). One handler is installed in `main.go` via `internal/logging`
  (`logging.Init()` then `logging.SetDevLogin(cfg.DevLogin)`); it writes to stderr
  and swapping in a file writer is the only change needed to log to a file. Levels:
  **Error** (an op/request failed), **Warn** (handled-but-suspicious: bad
  signature, misconfig), **Info** (production flow checkpoints: logins,
  session open/close, submissions, credits), **Debug** (dev-only detail — dev
  logins, dev credit, dedup no-ops, insufficient-credit rejections). **Debug is
  only emitted when `DEV_LOGIN=1`.** Log at *checkpoints that show the flow*, not
  every function; include ids (`streamer_id`, `viewer_id`, `item_id`, `message_id`)
  but **never secrets** (tokens, cookies, signatures, credentials).

## Gotchas that have bitten before

- **Restart wipes runtime state.** Queues, sessions, and invite tokens are
  in-memory. After a restart, streamers must click *Start* again and old
  `/s/<token>` links are dead. Expected — don't "fix" it by persisting tokens
  without a deliberate design.
- **Secure cookies over HTTP.** If `PUBLIC_BASE_URL` is `https://…` but you're
  testing on plain HTTP, the browser drops the `Secure` cookie and you appear
  "logged out." Use `http://…` (or leave it empty) locally.
- **`internal/twitch` looks live but isn't.** It compiles and has tests, but
  `main.go` never starts it. Don't assume chat commands work.
- **Sub-second time in tests.** Comparing a freshly-created in-memory timestamp
  to one round-tripped through SQLite (seconds) fails unless you truncate.

## Definition of done

`gofmt -l .` empty · `go vet ./...` clean · `go test ./...` green · new domain
logic has a test · invariants above upheld · the matching doc in `docs/` updated
in the same change.
