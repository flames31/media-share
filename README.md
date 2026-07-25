# Media Share

A Twitch media-share queue. Viewers submit YouTube clips (or upload their own
media); moderators verify submissions in an admin console; approved clips play on
a standalone player page you capture in your streaming software. Moderators can
also control the queue from Twitch chat (`!skip`, `!pause`, …).

Payments are **not** wired up yet — for now the play length is entered manually on
the submission form. That field is where the future donation→duration formula
(e.g. $1 = 10s) will plug in.

## Features

- **Single submission page** (`/submit`) — YouTube link + start time + play
  length, or a media-file upload.
- **Player page** (`/player`) — auto-plays the approved queue. YouTube via the
  IFrame API (honours the start time and stops after the set length); uploads via
  an HTML5 `<video>`. Add it as a Browser source / capture the tab on stream.
- **Admin console** (`/admin`) — live view of pending submissions (with
  thumbnails/previews), approve/reject, the approved queue with remove, and
  controls: **Skip**, **Pause/Resume**, **Clear queue**, **Clear all**, and a
  **Bypass verification** toggle (auto-approve submissions).
- **Twitch chat commands** (broadcaster + mods only): `!skip` / `!next`,
  `!pause`, `!resume`, `!clear [all]`, `!current`.
- Everything updates in real time over WebSockets. State is in-memory; uploaded
  files persist under `MEDIA_DIR`.

## Requirements

- Go 1.24+

## Run

```sh
# minimal
ADMIN_TOKEN=test go run .

# or via .env
cp .env.example .env      # edit values
export $(grep -v '^#' .env | xargs) && go run .
```

Then open:

- Submission: <http://localhost:8080/submit>
- Player:     <http://localhost:8080/player>
- Admin:      <http://localhost:8080/admin>  (enter `ADMIN_TOKEN`)

> If `ADMIN_TOKEN` is empty the admin console is unprotected — fine for local
> testing, but set a token before exposing the server.

## Configuration

All configuration is via environment variables (see `.env.example`):

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `ADMIN_TOKEN` | _(empty)_ | Bearer token for the admin console/API |
| `MEDIA_DIR` | `./media` | Where uploads are stored |
| `MAX_UPLOAD_MB` | `100` | Upload size cap |
| `ALLOWED_MEDIA_EXT` | `mp4,webm,mov,ogg` | Allowed upload extensions |
| `TWITCH_CHANNEL` | _(empty)_ | Channel to join (bot enabled only when all 3 Twitch vars are set) |
| `TWITCH_BOT_USERNAME` | _(empty)_ | Bot account login |
| `TWITCH_OAUTH_TOKEN` | _(empty)_ | Chat OAuth token (`oauth:` prefix optional) |

## Enabling Twitch chat control

1. **Use a bot account.** Either your own account or a separate account you
   create for the bot. Whichever account the token belongs to is who chat
   messages come from.
2. **Get a chat OAuth token** with the `chat:read` and `chat:edit` scopes:
   - Fastest: while logged in as the bot account, generate a token with a
     community token generator such as <https://twitchtokengenerator.com> (pick
     the "Bot Chat Token" preset), or
   - Register your own app at <https://dev.twitch.tv/console/apps> and run the
     OAuth flow to mint a token with those scopes.
3. **Set the env vars** and restart:

   ```sh
   TWITCH_CHANNEL=your_channel \
   TWITCH_BOT_USERNAME=your_bot_account \
   TWITCH_OAUTH_TOKEN=oauth:xxxxxxxxxxxxxxxxxxxx \
   ADMIN_TOKEN=test go run .
   ```

   You should see `twitch: connected as <bot> in #<channel>` in the logs. In
   chat, as the broadcaster or a mod, try `!current` — the bot replies with the
   now-playing title.

### Chat commands

| Command | Action |
| --- | --- |
| `!skip`, `!next` | Skip the current clip |
| `!pause` | Pause playback |
| `!resume`, `!play` | Resume playback |
| `!clear` | Clear the approved queue |
| `!clear all` | Clear pending, queue, and now-playing |
| `!current`, `!np` | Show what's playing |

Commands are ignored for non-moderators.

## Project layout

```
main.go                     wiring: config → manager → hub → server → bot
internal/
  config/                   env configuration
  queue/                    queue Manager, state machine, YouTube URL parsing (+ tests)
  hub/                      WebSocket fan-out
  server/                   HTTP handlers (pages, submit, admin, player, ws)
  twitch/                   IRC-over-WebSocket chat bot (+ parser tests)
web/
  templates/                submit / player / admin pages (embedded)
  static/                   CSS + vanilla JS (embedded)
media/                      uploaded files (gitignored)
```

## Testing

```sh
go test ./...
```

## Roadmap / deferred

- Payments and the real donation→duration formula (the manual length field is the
  placeholder).
- Optional persistence (queue is in-memory; a JSON snapshot could survive
  restarts).
- Per-user rate limiting / anti-spam.
