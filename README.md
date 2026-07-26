# Media Share

A **multi-tenant** Twitch media-share platform. Streamers **log in with Twitch**,
open a submission session, and share an invite link; viewers submit YouTube clips
(or upload their own media) to that streamer's queue; the streamer moderates in
their console and plays approved clips on their own player page (an OBS browser
source). Many streamers run isolated sessions at the same time — no crosstalk.

Payments are **not** wired up yet — for now the play length is entered manually on
the submission form. That field is where the future donation→duration formula
(e.g. $1 = 10s) will plug in.

## Features

- **Log in with Twitch** — a streamer's Twitch account is their workspace. Open,
  self-serve; no passwords. Accounts + login sessions persist in SQLite.
- **Per-streamer isolation** — each streamer has their own queue, session, invite
  link, and player, keyed by their Twitch account. WebSocket updates are scoped
  per streamer (rooms).
- **Streamer-controlled sessions** — submissions are closed until the streamer
  clicks **Start media share**, which generates a shareable invite link
  (`/s/<token>`). **Stop** (or **Regenerate link**) invalidates old links at once.
- **Submission page** (via the invite link) — YouTube link + start time + play
  length, or a media-file upload. Shows "Media share is closed" when there's no
  open session.
- **Per-streamer player** (`/p/<key>`) — a stable capability URL for OBS that
  auto-plays that streamer's approved queue. YouTube via the IFrame API (honours
  start time, stops after the set length); uploads via an HTML5 `<video>`.
- **Admin console** (`/admin`, behind Twitch login) — live pending review
  (thumbnails/previews), approve/reject, the approved queue with remove, and
  controls: **Skip**, **Pause/Resume**, **Clear queue**, **Clear all**, and a
  **Bypass verification** toggle. Shows the invite link and the OBS player URL.
- **Moderators** — the streamer can generate a **moderator link** and hand the
  console to trusted helpers (no account needed) so they don't have to run it
  themselves. Moderators get full moderation + session control; only the streamer
  can mint or **revoke** moderator links (revoking signs current moderators out
  immediately).
- Everything updates in real time over WebSockets. Queue/session state is
  in-memory per streamer; uploaded files persist under `MEDIA_DIR`.

> The Twitch chat bot (`!skip`, `!pause`, …) from the single-tenant version is
> **deferred** in the platform build; the `internal/twitch` package is kept for a
> future per-streamer re-wire.

## Requirements

- Go 1.24+

## Setup: register one Twitch app (operator, once)

Streamers log in with Twitch, so the person running the server registers **one**
Twitch application:

1. Go to <https://dev.twitch.tv/console/apps> → Register Your Application.
2. **OAuth Redirect URL:** `http://localhost:8080/auth/twitch/callback`
   (must match `PORT` / `PUBLIC_BASE_URL`; add your deployed `https://…/auth/twitch/callback` when hosting).
3. Copy the **Client ID** and generate a **Client Secret**.

## Run

```sh
cp .env.example .env      # fill in TWITCH_CLIENT_ID / TWITCH_CLIENT_SECRET
go run .                  # .env in the working dir is loaded automatically

# or inline (real env vars override .env)
TWITCH_CLIENT_ID=xxxx TWITCH_CLIENT_SECRET=yyyy go run .
```

The server reads a `.env` file from the working directory on startup (override the
path with `ENV_FILE=/path/to/.env`). Values already set in the real environment
take precedence.

Then open <http://localhost:8080/> and **Log in with Twitch**.

> **Just want to try it without registering a Twitch app?** Run with `DEV_LOGIN=1`
> (e.g. `DEV_LOGIN=1 go run .`) to get a password-less **Dev login** button on the
> home page. It logs you in as a local dev account. **Local testing only — never
> enable in production.**

## How it works

1. **Streamer logs in** at `/login` → lands on their console at `/admin`.
2. The console shows the streamer's **OBS player URL** (`/p/<key>`) — add it as a
   Browser source. It stays the same across sessions and restarts.
3. Click **Start media share** → copy the **invite link** (`/s/<token>`) and share
   it with viewers (chat, panel, etc.).
4. Viewers open the link, submit a YouTube clip or upload; it appears under
   **Pending review**. Approve/reject; approved clips auto-play on the player.
5. Click **Stop media share** (or **Regenerate link**) to close submissions — old
   links stop working immediately.

Each streamer only ever sees and controls their own queue; sessions run
concurrently and independently.

**Handing off to moderators (optional):** in the console, generate a **moderator
link** and share it with someone you trust. They open it, click **Enter as
moderator**, and land on the same console — approving/rejecting, skipping, and
opening/closing the media share on your behalf (no account needed), so you don't
have to run it yourself. Only you can mint or **revoke** the link; revoking signs
current moderators out at once.

## Configuration

All configuration is via environment variables (see `.env.example`):

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `PUBLIC_BASE_URL` | _(empty)_ | Base URL for OAuth redirect + links; defaults to `http://localhost:<PORT>`. HTTPS enables Secure cookies |
| `DB_PATH` | `./data/media-share.db` | SQLite database (accounts + login sessions) |
| `MEDIA_DIR` | `./media` | Where uploads are stored |
| `MAX_UPLOAD_MB` | `100` | Upload size cap |
| `ALLOWED_MEDIA_EXT` | `mp4,webm,mov,ogg` | Allowed upload extensions |
| `TWITCH_CLIENT_ID` | _(empty)_ | Twitch app Client ID (**required** for login) |
| `TWITCH_CLIENT_SECRET` | _(empty)_ | Twitch app Client Secret (**required** for login) |
| `DEV_LOGIN` | _(off)_ | `1` shows a local password-less "Dev login" button — testing only, never in production |

## Project layout

```
main.go                     wiring: config → store(DB) → registry → auth → hub → server
internal/
  config/                   env configuration
  store/                    SQLite: streamers + login sessions + moderator links (+ tests)
  oauth/                    Twitch "Log in with Twitch" OAuth client (+ tests)
  auth/                     login flow, cookie sessions, owner/moderator roles, RequireStreamer/RequireOwner
  tenant/                   per-streamer registry (queue+session), token/key indexes (+ tests)
  queue/                    queue Manager, state machine, YouTube URL parsing (+ tests)
  session/                  streamer-controlled submission sessions (+ tests)
  hub/                      room-scoped WebSocket fan-out (+ tests)
  server/                   HTTP handlers (login, admin, moderators, player, submit, ws)
  twitch/                   chat bot — kept for a future per-streamer re-wire (deferred)
web/
  templates/                login / admin / player / submit / mod_claim pages (embedded)
  static/                   CSS + vanilla JS (embedded)
media/                      uploaded files (gitignored)
data/                       SQLite database (gitignored)
```

## Documentation

Detailed docs live in [`docs/`](docs/):

- [`docs/architecture.md`](docs/architecture.md) — layers, the tenant model, trust
  boundaries, concurrency.
- [`docs/data-flows.md`](docs/data-flows.md) — login, sessions, submission,
  moderation, playback, WS rooms.
- [`docs/http-api.md`](docs/http-api.md) — every route.
- [`docs/data-model.md`](docs/data-model.md) — SQLite schema + in-memory state.
- [`docs/agents/`](docs/agents/) — onboarding for AI coding agents: a code map,
  debugging playbook, feature recipes, and the invariants to uphold.

## Security notes

- Login sessions are opaque random ids stored in SQLite and set as HttpOnly,
  SameSite=Lax cookies (Secure over HTTPS). Admin scope is always taken from the
  cookie — never from client-supplied ids.
- Invite tokens and player keys are unguessable capability URLs, meant to be
  shared. Stopping/regenerating a session invalidates old invite links at once.
- Admin state-changing requests are same-origin-guarded (SameSite + Sec-Fetch-Site).

## Testing

```sh
go test ./...
```

## Roadmap / deferred

- Per-streamer Twitch chat bot (`!skip`, `!pause`, …) — re-wire `internal/twitch`
  per tenant.
- Payments and the real donation→duration formula (the manual length field is the
  placeholder).
- Per-user rate limiting / anti-spam; idle-tenant eviction; queue persistence.
