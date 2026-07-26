# For agents — orientation

You're an AI agent picking up this repo. This folder gets you productive fast
without re-deriving the architecture from scratch. Read this page, then jump to
[map.md](map.md) when you need to find code, [debugging.md](debugging.md) when
something's broken, or [adding-features.md](adding-features.md) when you're
building.

**Also read the top-level [`../architecture.md`](../architecture.md) once** — it's
the source of truth for the mental model. This page is the short version.

## What the app is (one paragraph)

A multi-tenant Twitch media-share platform in Go. Streamers log in with Twitch;
each gets an isolated **tenant** (their own queue + invite-link session + OBS
player). Viewers open a streamer's invite link and submit YouTube clips or
uploads; the streamer moderates in a console and approved clips auto-play on
their player page. Everything is server-rendered HTML + vanilla JS, with live
updates over per-streamer WebSocket rooms. Only accounts and login sessions are
persisted (SQLite); all queue/session state is in memory and resets on restart.
Payments aren't implemented — the "duration" field is a placeholder.

## The mental model (hold this in your head)

```
streamer id  ─is the key for─►  Tenant {Queue, Session}  ─and is also─►  hub room name
     ▲                                                                        │
     │ derived server-side from:                                              │ broadcasts go to
     │   • admin  → login cookie                                              │ exactly the streamer's
     │   • player → player_key                                                ▼ own admin + player
     └── never from client input                              (no other tenant sees them)
```

Actors and credentials, all resolved to a streamer id server-side:

| Actor | Credential | Where it's checked |
| --- | --- | --- |
| Streamer (owner) | `sid` cookie (`role=owner`) | `auth.Authenticate` / `RequireStreamer` / `RequireOwner` |
| Moderator | `sid` cookie (`role=moderator`, `streamer_id`=owner) | `auth.Authenticate`; claimed via `/mod/<token>` → `store.ResolveModeratorLink` |
| Player (OBS) | `player_key` in URL | `store.GetStreamerByPlayerKey` |
| Viewer (submitter) | invite `token` | `reg.ResolveSession` |

A **moderator** is a delegated helper with no account: their login session stores
the *owner's* `streamer_id` plus `role=moderator`, so they resolve to the owner's
tenant through the same path as the owner. `role` only gates owner-only endpoints
(`RequireOwner`, e.g. managing moderator links) and small UI differences.

## The invariants you must not break

1. **Never trust a client-supplied room / streamer id / tenant.** Admin scope
   comes from the cookie; player scope from the key. `handleWS` is the chokepoint.
2. **Go through the `Tenant` for session control** (`StartSession`,
   `RegenerateSession`, `StopSession`) — not `Session.Start()` directly — or the
   invite-token index goes stale and `/s/<token>` breaks.
3. **Broadcast outside the lock.** Domain managers build a snapshot under the
   mutex, release it, then call the broadcast fn. Don't broadcast while holding
   the lock.
4. **Every queue mutation must emit a fresh `Snapshot`** so clients stay live.
   The existing methods already do; follow the pattern.
5. **The submit handler is the real gate**, not the page. Re-resolve the token
   server-side in any submission path.

Details and the "why" behind each are in [conventions.md](conventions.md).

## Build / test / run

```sh
go build ./...        # compile
go vet ./...          # vet
gofmt -l .            # should print nothing
go test ./...         # full suite (unit tests across packages)

# run locally without a Twitch app:
printf 'DEV_LOGIN=1\n' > .env && go run .    # then open http://localhost:8080 → "Dev login"
```

`.env` in the working dir is auto-loaded by `config.Load()` (`internal/config/
dotenv.go`); real env vars override it. See [`../../README.md`](../../README.md)
for the full config table.

## Where things live (30-second version)

- **HTTP & routing:** `internal/server/` (`server.go` = routes; `handlers_*.go`).
- **Tenants / isolation:** `internal/tenant/registry.go`.
- **Queue + state machine:** `internal/queue/`.
- **Invite-link sessions:** `internal/session/`.
- **WebSocket rooms:** `internal/hub/`.
- **Login / cookies / CSRF:** `internal/auth/`; Twitch identity: `internal/oauth/`.
- **SQLite:** `internal/store/`.
- **Config / .env:** `internal/config/`.
- **Frontend:** `web/templates/*.html`, `web/static/*.js|css` (embedded).
- **Deferred chat bot:** `internal/twitch/` — present but unwired; don't assume
  it runs.

Full file-by-file index: [map.md](map.md).
