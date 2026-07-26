# HTTP API reference

All routes are registered in `internal/server/server.go` (`routes()`), using Go
1.22+ method + pattern matching. "Auth" columns:

- **public** — no auth.
- **cookie** — requires a valid `sid` login cookie (`RequireStreamer` for APIs,
  `requireStreamerPage` for pages). Unsafe methods also pass the `Sec-Fetch-Site`
  same-origin guard.
- **token** — requires a valid, currently-open invite token.
- **key** — requires a valid `player_key`.
- **mod-link** — requires a valid (unrevoked) moderator link token.
- **viewer** — requires a valid `vsid` viewer login cookie (`RequireViewer`).
  A viewer identity is separate from a streamer's; unsafe methods also pass the
  `Sec-Fetch-Site` same-origin guard.
- **hmac** — a public route authenticated by the Twitch EventSub signature
  (`TWITCH_EVENTSUB_SECRET`), not a cookie.

Cookie sessions carry a **role** (`owner` or `moderator`). `RequireStreamer`
accepts both; `RequireOwner` accepts only owners. A moderator's session
`streamer_id` is the tenant *owner*, so it resolves to the owner's tenant
throughout.

## Pages (HTML)

| Method | Path | Auth | Handler | Notes |
| --- | --- | --- | --- | --- |
| GET | `/` | public | `handleIndex` | Authed → 303 `/admin`; else renders login. |
| GET | `/login` | public | `handleLoginPage` | Shows Twitch button (if `OAuthEnabled`) and/or Dev-login (if `DEV_LOGIN`). `?error=` surfaces auth errors. |
| GET | `/admin` | cookie | `handleAdminPage` | Console. Injects `DisplayName`, `PlayerURL`. Redirects to `/login` if not authed. |
| GET | `/submit` | public | `handleSubmitPage` | Fallback submit page; token from `?s=`. |
| GET | `/s/{token}` | public | `handleSubmitPage` | Invite link. Token injected as `SessionToken`. |
| GET | `/p/{key}` | key | `handlePlayerPage` | OBS player. 404 on unknown key. Injects `PlayerKey`, `StreamerName`. |
| GET | `/mod/{token}` | mod-link | `handleModClaimPage` | Moderator-invite landing page (side-effect-free). 404 on unknown/revoked token. Injects `StreamerName`, `Token`. |

## Auth

| Method | Path | Auth | Handler | Notes |
| --- | --- | --- | --- | --- |
| GET | `/auth/twitch/start` | public | `handleAuthStart` | 302 to Twitch consent (mints single-use state). |
| GET | `/auth/twitch/callback` | public | `handleAuthCallback` | `?code&state` → login → set cookie → 303 `/admin`. Errors → `/login?error=`. |
| POST | `/auth/dev/login` | public\* | `handleDevLogin` | \*404 unless `DEV_LOGIN=1`. Logs in as fixed `dev` account (role `owner`). |
| POST | `/mod/{token}` | mod-link | `handleModClaim` | Exchanges a valid moderator link for a moderator session scoped to the link owner's tenant → 303 `/admin`. 404 if revoked. |
| POST | `/logout` | cookie | `handleLogout` | Deletes the session row + clears cookie. |

## Viewer auth & credits

Viewers log in with Twitch (a separate identity from streamers) so their cheered
bits can be credited and spent. The Twitch redirect URI is **shared** with streamer
login; the single-use OAuth `state` carries the login *kind* (`owner`/`viewer`) and,
for viewers, the submit token to return to.

| Method | Path | Auth | Handler | Notes |
| --- | --- | --- | --- | --- |
| GET | `/viewer/auth/start` | public | `handleViewerAuthStart` | `?s=<token>` → 303 to Twitch consent (viewer state). |
| POST | `/viewer/logout` | public | `handleViewerLogout` | Clears `vsid`, 303 back to the submit page. |
| POST | `/viewer/dev/login` | public\* | `handleViewerDevLogin` | \*404 unless `DEV_LOGIN=1`. Logs in as a fixed `devviewer`. |
| GET | `/api/viewer/me` | viewer | `handleViewerMe` | `?s=<token>` → `{displayName, login, balance, creditsEnabled, creditsPerSecond}`. Balance is per-channel (resolved from the token). |
| POST | `/api/dev/credit` | viewer\* | `handleDevCredit` | \*404 unless `DEV_LOGIN=1`. JSON `{s?, streamerId?, amount?}` — channel resolved from the submit token `s` (or explicit `streamerId`), `amount` defaults to 1000 — grants credits to the current dev viewer → `{balance, granted}`. Backs the one-click **+ Add test credits** button on the submit page. |
| POST | `/api/webhooks/twitch/eventsub` | hmac | `handleEventSub` | Twitch EventSub receiver (see below). |

### EventSub webhook (`/api/webhooks/twitch/eventsub`)

Authenticated by HMAC, **not** a cookie. Every request:

1. Raw body is read verbatim and the signature verified: `sha256=` +
   `HMAC_SHA256(secret, messageId + timestamp + body)`, constant-time
   (`twitch.VerifyEventSubSignature`). Bad signature → **403**. Timestamps older
   than 10 min → **403** (replay guard).
2. Dispatch on `Twitch-Eventsub-Message-Type`:
   - `webhook_callback_verification` → **200** `text/plain` echoing `challenge`
     (activates an out-of-band subscription).
   - `revocation` → logged, **200**.
   - `notification` → `channel.cheer` / `channel.bits.use` parsed; a non-anonymous
     cheer credits `bits` (1 bit = 1 credit) to `(user_id, broadcaster_user_id)` via
     `store.CreditBits`. Dedup by message id makes retries idempotent. Always **200**.

Subscriptions are created **out-of-band** (Twitch CLI / dashboard) with the same
`TWITCH_EVENTSUB_SECRET` and callback `<BaseURL>/api/webhooks/twitch/eventsub`.

## Public API (tenant resolved from token / key)

| Method | Path | Auth | Handler | Request → Response |
| --- | --- | --- | --- | --- |
| POST | `/api/submit` | token + viewer | `handleSubmit` | multipart `{session, type=youtube\|upload, url\|file, start, duration}` → `{id,title,status}`. `SubmitterName` comes from the viewer's Twitch identity (no `name` field). When credits are on, deducts `duration × CreditsPerSecond` atomically **before** enqueue; **402** `{error, cost, balance}` if short. Body capped at `MaxUploadBytes + 1MB`. |
| GET | `/api/session/check` | public | `handleSessionCheck` | `?s=<token>` → `{valid: bool}`. Reveals nothing about the streamer. |
| POST | `/api/player/ended` | key | `handlePlayerEnded` | JSON `{key, id}` → advances that tenant's queue. 404 on bad key. |
| GET | `/ws` | cookie or key | `handleWS` | `?role=admin` (cookie→room) or `?role=player&key=` (key→room). Upgrades to WebSocket. |

## Admin API (cookie-gated; acts on the authenticated streamer's tenant)

Open to **both** the owner and their moderators (`RequireStreamer`), except the
**Moderator management** rows below, which are owner-only (`RequireOwner` → 403 for
a moderator). For a moderator session the tenant is the *owner's* (the session's
`streamer_id` is the owner), so these all operate on the same tenant.

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
| GET | `/api/admin/moderators` **(owner-only)** | `handleModeratorsStatus` | — | `{link}` — current moderator link, empty if none. |
| POST | `/api/admin/moderators/link` **(owner-only)** | `handleModeratorsLink` | — | Mint/regenerate the moderator link (old link dies) → `{link}`. |
| POST | `/api/admin/moderators/revoke` **(owner-only)** | `handleModeratorsRevoke` | — | Delete the link **and** sign out all current moderators → `{ok:true}`. |

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
