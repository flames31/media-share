# Architecture

## What this is

A **multi-tenant** platform. Many streamers log in with Twitch at the same time;
each gets an isolated workspace: their own submission queue, their own
media-share session (invite link), and their own player page for OBS. Viewers
stay anonymous — a valid invite link is all they need, and it is scoped to one
streamer. No crosstalk between tenants.

Payments are **not** wired up. The "play length" is entered manually on the
submission form; that field is the placeholder for a future donation→duration
formula.

## The one idea to hold onto: the tenant

Everything hangs off one concept. A **tenant** is one streamer's runtime state:

```
Streamer (a Twitch account, persisted in SQLite)
    └── Tenant (in memory, created lazily on first use)
            ├── queue.Manager    — that streamer's submission queue + playback state
            └── session.Manager  — that streamer's invite-link on/off window
```

The `tenant.Registry` maps **streamer id → Tenant** and also maintains an
**invite token → streamer id** index so an anonymous viewer's submission can be
routed to the right tenant without any login.

The WebSocket **hub** groups connected clients into **rooms**, where `room ==
streamer id`. A tenant's queue/session broadcasts go to that room only, so a
streamer's admin console and player page receive their events and nobody else's.

If you remember only one thing: **the room name is the streamer id, and it is
always derived server-side (from the login cookie for admins, from the player key
for players) — never trusted from the client.**

## Layers

```
                         ┌──────────────────────────────────────────┐
  Browser / OBS  ──────► │  internal/server  (HTTP + WS routing)     │
  (admin, player,        │  routes → handlers → render/writeJSON     │
   viewer submit)        └───────────────┬──────────────────────────┘
                                         │
        ┌────────────────────────────────┼───────────────────────────────┐
        ▼                                 ▼                               ▼
 ┌─────────────┐               ┌────────────────────┐          ┌──────────────────┐
 │ internal/   │               │ internal/tenant    │          │ internal/auth    │
 │ auth        │               │  Registry          │          │  (login sessions,│
 │ (cookies,   │  streamer id  │   ├── Tenant.Queue │          │   RequireStreamer│
 │  CSRF,      │ ────────────► │   │    (queue.Mgr) │          │   middleware)    │
 │  Twitch)    │               │   └── Tenant.Sess. │          └────────┬─────────┘
 └──────┬──────┘               │        (session.  │                   │
        │                      │         Manager)  │                   │
        ▼                      └─────────┬──────────┘                   ▼
 ┌─────────────┐                         │                     ┌──────────────────┐
 │ internal/   │                         │ BroadcastTo(room)   │ internal/store   │
 │ oauth       │                         ▼                     │  SQLite:         │
 │ (Twitch     │               ┌────────────────────┐          │  streamers,      │
 │  identity)  │               │ internal/hub       │          │  auth_sessions   │
 └─────────────┘               │  rooms, WS fan-out │          └──────────────────┘
                               └────────────────────┘
```

- **`internal/server`** — the only package that knows about HTTP. It routes,
  authenticates (delegating to `auth`), resolves the correct tenant, calls into
  the domain packages, and renders HTML or JSON. Handlers are split by concern:
  `handlers_auth.go`, `handlers_admin.go`, `handlers_session.go`,
  `handlers_submit.go`, `handlers_player.go`, plus `render.go` for pages/JSON and
  `server.go` for routing + WS room resolution.
- **`internal/auth`** — "Log in with Twitch" lifecycle: OAuth handshake (via
  `oauth`), opaque cookie-backed login sessions (via `store`), the
  `RequireStreamer` middleware, and a lightweight same-origin CSRF guard.
- **`internal/oauth`** — a thin Twitch Authorization-Code client. Builds the
  consent URL, exchanges the code, and resolves the user's identity via Helix
  `GET /users`. Endpoints are package vars so tests can point them at a stub.
- **`internal/store`** — SQLite persistence for the only durable data:
  `streamers` and `auth_sessions`. Pure-Go driver (`modernc.org/sqlite`).
- **`internal/tenant`** — the registry and `Tenant`. Owns lazy tenant creation,
  the invite-token index, and the wiring that points each tenant's broadcasts at
  its hub room. Session start/stop/regenerate go **through the tenant** so the
  token index stays in sync.
- **`internal/queue`** — the queue `Manager` and its state machine
  (pending → approved → playing → done, plus rejected/skipped). One mutex; every
  mutation emits a fresh immutable `Snapshot` to the broadcast callback.
- **`internal/session`** — the streamer-controlled submission window. Holds the
  secret invite `token`; exposes a public `Status` (active + startedAt, no token)
  for broadcasting.
- **`internal/hub`** — room-scoped WebSocket fan-out. `BroadcastTo(room, …)`,
  `ServeWS(w, r, room, role)`, and an `OnConnect(room, role)` hook (backed by the
  registry) that sends a newly connected client its initial state.
- **`internal/config`** — env + `.env` loading and derived settings (`BaseURL`,
  `TwitchRedirectURI`, `CookieSecure`, upload limits).
- **`internal/twitch`** — the legacy single-tenant chat bot (`!skip`, `!pause`).
  **Kept but unwired** in the platform build; slated for a future per-tenant
  re-wire. Nothing in the running server references it.
- **`web`** — embedded templates and static assets.

## Trust boundaries (read before touching auth or WS)

| Actor | How they're identified | Scope they get |
| --- | --- | --- |
| **Streamer (admin)** | `sid` cookie → `auth_sessions` row → streamer | Their own tenant. Room = their streamer id, taken from the cookie. |
| **Player (OBS)** | `player_key` in the URL/WS query → streamer | Read-only view of one tenant. Room resolved from the key. |
| **Viewer (submitter)** | Invite `token` in the form/path | Can submit to exactly one tenant while its session is open. |

The authoritative rules, enforced in code:

- Admin room and tenant are **always** derived from the authenticated cookie
  (`server.tenant(r)` / `handleWS`), never from a client-supplied id.
- The player key and invite token are **capability URLs** — unguessable, meant to
  be shared. Stopping or regenerating a session drops the old token from the
  index immediately, so old invite links stop resolving.
- `handleSubmit` re-checks `reg.ResolveSession(token)` server-side. The page-level
  "is the session open?" check is UX only; the submit handler is the real gate.
- State-changing admin requests pass a `Sec-Fetch-Site` same-origin check
  (`auth.sameSite`) on top of `SameSite=Lax` cookies.

## Concurrency model

- **`queue.Manager`** and **`session.Manager`** each own a `sync.Mutex`. The
  pattern is: take the lock, mutate, build the `Snapshot`/`Status`, capture the
  broadcast fn, **release the lock, then broadcast**. Broadcasting never happens
  while holding the lock, so a slow WebSocket client can't stall the domain.
- **`hub.Hub`** guards its client set with a mutex. Each client has a buffered
  `send` channel; if it's full (slow client) the client is dropped rather than
  blocking the broadcast.
- **`tenant.Registry`** guards its two maps with a mutex; `Get` is lazy and
  idempotent.
- SQLite runs with `MaxOpenConns(1)` + WAL + `busy_timeout`, so writes serialize
  cleanly under light load.

## Persistent vs. ephemeral

| State | Where | Survives restart? |
| --- | --- | --- |
| Streamer accounts, `player_key` | SQLite `streamers` | **Yes** |
| Login sessions (cookies) | SQLite `auth_sessions` | **Yes** |
| Uploaded media files | `MEDIA_DIR` on disk | **Yes** (files); queue entry pointing at them does not |
| Queue items, now-playing, pause/bypass | `queue.Manager` (memory) | No |
| Session active/token | `session.Manager` (memory) | No |
| Tenant registry + token index | `tenant.Registry` (memory) | No |

On restart, every media-share session is closed and every queue is empty;
streamers simply click **Start media share** again. This is intentional and
matches the original single-tenant behavior.

## Request → broadcast, end to end

A viewer submits a YouTube clip:

1. `POST /api/submit` with the invite `token` hits `handleSubmit`.
2. `reg.ResolveSession(token)` returns the tenant (or 403 if the session is
   closed / token stale).
3. `submitYouTube` parses the URL, builds a `queue.Item`, calls
   `tenant.Queue.Submit(item)`.
4. `Submit` appends the item (pending, or approved if bypass is on), advances
   playback if idle, builds a `Snapshot`, releases the lock, and calls the
   broadcast callback.
5. The callback (wired in `Registry.Get`) is `hub.BroadcastTo(room, "state",
   snapshot)` with `room == streamer id`.
6. Every WS client in that room — the streamer's admin console and their player
   page — receives the `state` message and re-renders. No other tenant sees it.

See [data-flows.md](data-flows.md) for the other flows (login, session lifecycle,
playback/ended, moderation).
