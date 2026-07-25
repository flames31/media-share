# Debugging playbook

Symptom → likely cause → where to look. Start by reproducing, then read the
handler named below and trace toward the domain package.

## First moves

```sh
go build ./... && go vet ./... && go test ./...   # is it even wired correctly?
go run .                                           # watch startup logs
```

Startup logs (from `main.go` / `config.Load`) tell you a lot:
- `config: loaded N setting(s) from .env` — the `.env` was read.
- `WARNING: Twitch login is not configured…` — `TWITCH_CLIENT_ID/SECRET` unset.
- `WARNING: DEV_LOGIN is enabled…` — dev login is on.
- `Twitch OAuth redirect (register this): …` — the exact redirect URI Twitch must
  have registered.

## Login / auth

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| No login button on `/` or `/login` | `TWITCH_CLIENT_ID/SECRET` unset **and** `DEV_LOGIN` off. This is correct behavior, not a bug. | `config.OAuthEnabled`, `handleLoginPage`, `login.html` |
| `/auth/twitch/callback` → "invalid or expired login state" | State expired (>10 min) or reused; or the server restarted (states are in-memory). | `auth.consumeState`, `auth.stateTTL` |
| Twitch returns `redirect_mismatch` | The registered redirect URI ≠ `TwitchRedirectURI()`. It's derived from `PUBLIC_BASE_URL` (or `http://localhost:<PORT>`). | `config.TwitchRedirectURI`, startup log |
| Logged in but immediately bounced to `/login` | Cookie not being sent/stored: `Secure` cookie over plain HTTP (set `PUBLIC_BASE_URL` to `http://…` locally, not `https://`), or expired session. | `auth.setCookie`, `config.CookieSecure`, `store.GetValidAuthSession` |
| `403 cross-site request blocked` on an admin POST | `Sec-Fetch-Site` guard rejected it (genuinely cross-site, or a client not sending the header from same-origin). | `auth.sameSite`, `RequireStreamer` |
| `POST /auth/dev/login` → 404 | `DEV_LOGIN` not enabled. | `handleDevLogin`, `config.DevLogin` |

## Submissions

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| Submit → `403 "Media share is closed right now."` | The token doesn't resolve: session not started, was stopped/regenerated, or server restarted (tokens are in-memory). | `reg.ResolveSession`, `session.Valid`, `Tenant.StartSession` |
| Submission lands in the **wrong** streamer's queue | Token index out of sync — almost always because session control bypassed the `Tenant` wrapper. | `tenant.reindex`, `StartSession/StopSession/RegenerateSession` |
| YouTube link rejected as invalid | URL shape not handled by the parser. | `queue.ParseYouTube` (`internal/queue/youtube.go`) |
| Upload → "file type not allowed" / "too large" | Ext not in `ALLOWED_MEDIA_EXT`, or size > `MAX_UPLOAD_MB`. | `config.ExtAllowed`, `config.MaxUploadBytes`, `submitUpload` |
| Uploaded file 404s on the player | File saved under `MEDIA_DIR` but server serving a different dir, or `MediaURL` mismatch. | `submitUpload` (`storedName`), `/media/` route in `routes()` |
| Item appears immediately in the queue (skips pending) | Bypass is on. | `queue.SetBypass`, `Submit` |

## Live updates (WebSocket)

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| Admin/player doesn't update live | WS never connected, or connected to the wrong room. | `handleWS` room resolution, `hub.BroadcastTo` |
| A client sees **another** tenant's events | Room bug — a room derived from client input instead of cookie/key. This is a security bug; fix at the source. | `handleWS`, `Registry.Get` broadcast wiring |
| Client connects but renders empty until first change | `OnConnect` didn't fire or returned nothing. | `hub.ServeWS` → `OnConnect` → `registry.InitialMessages` |
| Player key URL → 404 | Unknown/typo'd `player_key`. | `store.GetStreamerByPlayerKey`, `handlePlayerPage` |
| Connections silently drop under load | Slow client's `send` buffer filled → hub drops it by design. | `hub.BroadcastTo` (the `default:` branch) |

## Playback

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| Queue won't advance after a clip ends | `Ended(id)` id-guard rejected a stale id, or `/api/player/ended` never posted. | `queue.Ended`, `handlePlayerEnded`, `player.js` |
| Next clip doesn't auto-start | `startIfIdleLocked` precondition (something already "playing", or empty queue). | `queue.startIfIdleLocked` |
| Pause doesn't stop the next item from starting | **By design** — pause only affects the currently-playing client, not promotion. | `queue.startIfIdleLocked` comment |
| YouTube ignores start time / plays full length | Client not applying `StartSeconds`/`DurationSeconds`. | `player.js` (IFrame API), `Item` fields |

## Persistence / SQLite

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| `open database` fatal at boot | Data dir missing/unwritable, or bad `DB_PATH`. | `main.go` `MkdirAll`, `store.Open` |
| `database is locked` under concurrency | Should be rare (`MaxOpenConns(1)` + WAL + busy_timeout). If it recurs, a long-held txn is the suspect. | `store.Open` PRAGMAs |
| Everything gone after restart | Expected: queues/sessions/tokens are in-memory; only accounts + login sessions persist. | [../data-model.md](../data-model.md) |
| `created_at`/timestamps off by sub-second in a test | Times must be truncated to whole seconds before compare. | `store.UpsertStreamer` (`time.Unix(...)`) |

## Config / .env

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| `.env` value ignored | A real env var of the same name is set (env wins by design), or the key is malformed. | `config.loadDotEnv`, `parseDotEnvLine` |
| Wrong `.env` loaded | `ENV_FILE` points elsewhere, or CWD isn't the project root. | `loadDotEnv` (path resolution) |
| Links/redirects use `localhost` in prod | `PUBLIC_BASE_URL` unset. | `config.BaseURL` |

## Useful greps

```sh
grep -rn "BroadcastTo"      internal/    # every place that pushes to a room
grep -rn "ResolveSession"   internal/    # token → tenant resolution
grep -rn "RequireStreamer"  internal/    # which routes are cookie-gated
grep -rn "StreamerFrom"     internal/    # handlers that read the authed streamer
grep -rn "s.tenant(r)"      internal/    # admin handlers acting on a tenant
```
