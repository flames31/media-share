# HTTP API reference

All routes are registered in `internal/server/server.go` (`routes()`), using Go
1.22+ method + pattern matching. "Auth" columns:

- **public** — no auth.
- **cookie** — requires a valid `sid` login cookie (`RequireStreamer` for APIs,
  `requireStreamerPage` for pages). Unsafe methods also pass the `Sec-Fetch-Site`
  same-origin guard.
- **token** — requires a valid, currently-open invite token.
- **key** — requires a valid `player_key`.

## Pages (HTML)

| Method | Path | Auth | Handler | Notes |
| --- | --- | --- | --- | --- |
| GET | `/` | public | `handleIndex` | Authed → 303 `/admin`; else renders login. |
| GET | `/login` | public | `handleLoginPage` | Shows Twitch button (if `OAuthEnabled`) and/or Dev-login (if `DEV_LOGIN`). `?error=` surfaces auth errors. |
| GET | `/admin` | cookie | `handleAdminPage` | Console. Injects `DisplayName`, `PlayerURL`. Redirects to `/login` if not authed. |
| GET | `/submit` | public | `handleSubmitPage` | Fallback submit page; token from `?s=`. |
| GET | `/s/{token}` | public | `handleSubmitPage` | Invite link. Token injected as `SessionToken`. |
| GET | `/p/{key}` | key | `handlePlayerPage` | OBS player. 404 on unknown key. Injects `PlayerKey`, `StreamerName`. |

## Auth

| Method | Path | Auth | Handler | Notes |
| --- | --- | --- | --- | --- |
| GET | `/auth/twitch/start` | public | `handleAuthStart` | 302 to Twitch consent (mints single-use state). |
| GET | `/auth/twitch/callback` | public | `handleAuthCallback` | `?code&state` → login → set cookie → 303 `/admin`. Errors → `/login?error=`. |
| POST | `/auth/dev/login` | public\* | `handleDevLogin` | \*404 unless `DEV_LOGIN=1`. Logs in as fixed `dev` account. |
| POST | `/logout` | cookie | `handleLogout` | Deletes the session row + clears cookie. |

## Public API (tenant resolved from token / key)

| Method | Path | Auth | Handler | Request → Response |
| --- | --- | --- | --- | --- |
| POST | `/api/submit` | token | `handleSubmit` | multipart `{session, type=youtube\|upload, url\|file, start, duration, name}` → `{id,title,status}` or `4xx {error}`. Body capped at `MaxUploadBytes + 1MB`. |
| GET | `/api/session/check` | public | `handleSessionCheck` | `?s=<token>` → `{valid: bool}`. Reveals nothing about the streamer. |
| POST | `/api/player/ended` | key | `handlePlayerEnded` | JSON `{key, id}` → advances that tenant's queue. 404 on bad key. |
| GET | `/ws` | cookie or key | `handleWS` | `?role=admin` (cookie→room) or `?role=player&key=` (key→room). Upgrades to WebSocket. |

## Admin API (cookie-gated; acts on the authenticated streamer's tenant)

| Method | Path | Handler | Body | Effect |
| --- | --- | --- | --- | --- |
| GET | `/api/admin/me` | `handleMe` | — | `{login, displayName, playerUrl}`. |
| GET | `/api/admin/state` | `handleState` | — | The tenant's `queue.Snapshot`. |
| POST | `/api/admin/approve` | `handleApprove` | `{id}` | pending → approved. |
| POST | `/api/admin/reject` | `handleReject` | `{id}` | pending → rejected. |
| POST | `/api/admin/remove` | `handleRemove` | `{id}` | approved → removed. |
| POST | `/api/admin/skip` | `handleSkip` | — | End now-playing, advance. |
| POST | `/api/admin/pause` | `handlePause` | — | Pause playback. |
| POST | `/api/admin/resume` | `handleResume` | — | Resume playback. |
| POST | `/api/admin/clear` | `handleClear` | `{all?}` | Clear queue (`all` also clears pending + now-playing). |
| POST | `/api/admin/bypass` | `handleBypass` | `{enabled}` | Toggle auto-approve. |
| GET | `/api/admin/session` | `handleSessionStatus` | — | `adminSessionView {active, startedAt, token, link}`. |
| POST | `/api/admin/session/start` | `handleSessionStart` | — | Open session, mint token → `adminSessionView`. |
| POST | `/api/admin/session/stop` | `handleSessionStop` | — | Close session, drop token → `adminSessionView`. |
| POST | `/api/admin/session/regenerate` | `handleSessionRegenerate` | — | New token, old links die → `adminSessionView`. |

> Item actions take `{id}` (shared `idBody`); toggles take their own small body
> (e.g. `{all}`, `{enabled}`). Bodies are decoded strictly
> (`DisallowUnknownFields`); an empty body is allowed and leaves fields zero. See
> `internal/server/handlers_admin.go` when wiring a new client call.

## Static & media

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/static/…` | Embedded assets (`web.StaticFS`), CSS + JS. |
| GET | `/media/…` | Uploaded files served from `MEDIA_DIR` on disk. |

## WebSocket messages

Server → client envelope (`hub.Message`):

```json
{ "type": "state",   "payload": { /* queue.Snapshot */ } }
{ "type": "session", "payload": { "active": true, "startedAt": "…" } }  // admins only
```

- On connect, the hub pushes initial `state` (and `session` for admins) via
  `registry.InitialMessages`.
- Clients don't send meaningful messages upstream; the read pump only keeps the
  connection alive (ping/pong). All control happens over the REST admin API.
