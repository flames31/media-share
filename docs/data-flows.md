# Data flows

Concrete, step-by-step sequences. Each names the exact functions involved so you
can jump straight to the code.

## 1. Streamer login ("Log in with Twitch")

```
Browser            server (handlers_auth)      auth              oauth            store            Twitch
   │  GET /login          │                       │                │                │                │
   │─────────────────────►│ render login.html     │                │                │                │
   │  click "Log in"      │                       │                │                │                │
   │  GET /auth/twitch/start                       │                │                │                │
   │─────────────────────►│ auth.AuthorizeURL() ──►│ mint state,    │                │                │
   │                       │                       │ AuthorizeURL() ►│                │                │
   │◄──── 302 to Twitch consent URL ───────────────┤                │                │                │
   │  authorize on Twitch ─────────────────────────┼────────────────┼────────────────┼───────────────►│
   │◄──── 302 /auth/twitch/callback?code&state ─────────────────────────────────────────────── ───────┤
   │─────────────────────►│ auth.Login(code,state)►│ consumeState   │                │                │
   │                       │                       │ ExchangeCode ──►│ token + Helix ─┼───────────────►│
   │                       │                       │                │ User{ID,Login} │                │
   │                       │                       │ UpsertStreamer ┼───────────────►│ (mint player_key on first login)
   │                       │                       │ CreateAuthSession ─────────────►│ (row in auth_sessions)
   │◄──── Set-Cookie sid=…; 302 /admin ────────────┤ setCookie      │                │                │
```

- **State** is single-use with a 10-minute TTL (`auth.stateTTL`), stored in
  memory in the `Authenticator`.
- **First login** mints a stable `player_key`; later logins keep it and only
  refresh `login`/`display_name`/`last_login_at` (`store.UpsertStreamer`).
- The cookie value is an opaque random 32-byte id; it means nothing without a
  matching `auth_sessions` row. `HttpOnly`, `SameSite=Lax`, `Secure` when HTTPS.
- **Dev login** (`POST /auth/dev/login`, only when `DEV_LOGIN=1`) short-circuits
  all of the above via `auth.DevLogin`, logging you in as a fixed `dev` account.

## 2. Authenticating a later request

```
Request with Cookie: sid=…
  → auth.Authenticate(r)
      → r.Cookie("sid")
      → store.GetValidAuthSession(sid)   (checks expiry; deletes if expired)
      → returns *store.Streamer  OR  ErrUnauthenticated
```

- **Pages** behind login use `server.requireStreamerPage` → redirects to `/login`
  on failure.
- **JSON APIs** use `auth.RequireStreamer` → `401` on failure, plus the
  `sameSite` CSRF guard on unsafe methods. On success the streamer is put in the
  request context; handlers read it with `auth.StreamerFrom(ctx)` and get their
  tenant via `server.tenant(r)` → `reg.Get(streamer.ID)`.

## 2b. Moderator claim (delegating the console)

Lets the streamer hand the admin console to a trusted moderator who has **no
account**. The key trick: the moderator's login session stores the **owner's**
`streamer_id` with `role='moderator'`, so it resolves to the owner's tenant
everywhere with no special-casing.

```
Owner console            server                         store
   │ POST /api/admin/moderators/link (RequireOwner)      │
   │───────────────────────► handleModeratorsLink ──────►│ RegenerateModeratorLink → token
   │◄──── {link: host/mod/<token>} ─────────────────────┤
   │  (share the link)

Moderator browser        server (handlers_auth)         store            auth
   │ GET /mod/<token> ─────► handleModClaimPage: ResolveModeratorLink → render landing (no side effects)
   │ click "Enter as moderator"
   │ POST /mod/<token> ────► handleModClaim: ResolveModeratorLink → ownerID
   │                         auth.LoginModerator(ownerID) ─────────────► CreateAuthSession(ownerID,'moderator')
   │◄──── Set-Cookie sid=…; 303 /admin ─────────────────
   │  ... now uses /admin exactly like the owner, minus owner-only cards ...
```

- **Claim is a POST** from a side-effect-free GET landing page, so link
  prefetching can't silently mint a moderator session.
- `RequireStreamer` admits the moderator to all `/api/admin/*` **except** the
  `moderators/*` management endpoints, which are `RequireOwner` (→ 403 for a mod).
- **Revoke/regenerate** (`POST /api/admin/moderators/revoke` /`.../link`) →
  `RevokeModeratorAccess` deletes the link; revoke also deletes existing
  `role='moderator'` sessions, so current moderators are signed out at once and
  the old `/mod/<token>` 404s.

## 3. Opening a media-share session (streamer)

```
Admin console        server (handlers_session)      tenant.Tenant        session.Manager      registry
   │ POST /api/admin/session/start                        │                    │                 │
   │────────────────────►│ s.tenant(r).StartSession() ───►│ old = Session.Token()               │
   │                      │                               │ tok = Session.Start() ─────────────►│ (active=true, new token)
   │                      │                               │ reg.reindex(id, old, tok) ─────────►│ byToken[tok] = id
   │◄─ adminSessionView{active, token, link} (JSON) ──────┤                    │                 │
```

- `Start`/`Regenerate`/`Stop` must be called **through the `Tenant`**
  (`StartSession`, `RegenerateSession`, `StopSession`) so the registry's
  `byToken` index is updated. Calling `Session.Start()` directly would open the
  session but leave the token unroutable — a bug. See
  [conventions.md](agents/conventions.md).
- The admin view (`adminSessionView`) is the **only** place the secret token/link
  is exposed, and only to the authenticated streamer. Broadcasts carry the public
  `session.Status` (no token).
- **Stop** and **Regenerate** drop the old token from the index, so previously
  shared `/s/<token>` links stop resolving immediately.

## 4. Viewer submission (paid, logged-in)

Viewers now **log in with Twitch** (a separate identity from streamers) and
**spend credits** to queue clips. Viewer login reuses the streamer OAuth client and
the shared `/auth/twitch/callback`; the single-use `state` carries `kind='viewer'`
and the submit token to return to. `handleAuthCallback` dispatches on the kind.

```
Submit page (/s/<token>)     server                         auth/store              registry / queue
   │ GET /s/<token> ─────────► handleSubmitPage: AuthenticateViewer → LoggedIn?; render submit.html
   │  (not logged in) click "Log in with Twitch"
   │ GET /viewer/auth/start?s=<token> ─► ViewerAuthorizeURL(token) → 302 Twitch consent
   │◄── /auth/twitch/callback?code&state ─► ConsumeState→kind=viewer → LoginViewer → Set-Cookie vsid; 303 /s/<token>
   │ GET /api/viewer/me?s=<token> (RequireViewer) ─► {displayName, balance, creditsPerSecond}
   │ POST /api/submit (RequireViewer; multipart: session, type, url|file, start, duration)
   │────────────────────────► handleSubmit  (viewer from ctx; SubmitterName = viewer.DisplayName)
   │                           reg.ResolveSession(token) ──► tenant OR 403 "closed"
   │                           cost = duration × CreditsPerSecond
   │                           store.SpendCredits(viewer,streamer,cost,itemID)
   │                              ├─ ok=false ─► 402 {error, cost, balance}   (nothing queued)
   │                              └─ ok=true  ─► tenant.Queue.Submit(item) → broadcast Snapshot
   │◄──── {id,title,status} (JSON) ──────────────────────────────────────
```

- `POST /api/submit` runs behind **`RequireViewer`** → `401` without a `vsid`. The
  `name` field is gone; the submitter name is the viewer's trusted Twitch name.
- **Deduct-before-enqueue**: credits are spent atomically *first*; the item is only
  queued on success. Insufficient funds ⇒ `402` and nothing is queued (an already
  uploaded file is deleted). The conditional `UPDATE` keeps balances ≥ 0.
- The submit handler still **re-resolves the token server-side** (authoritative
  gate); a closed session ⇒ `403 "Media share is closed right now."`
- **Pricing:** `duration × CREDITS_PER_SECOND`. Under credits the duration is
  clamped up to `minBillableSeconds` (10s), so what's charged always equals what
  plays (no "whole file" for the price of 10s). YouTube start/duration parsing is
  unchanged. With `CREDITS_ENABLED=false` (or in dev) the spend is skipped.
- With **bypass** on, `Submit` puts the item straight into the approved queue.

## 4b. Cheer → credit (Twitch EventSub webhook)

Bits cheered in a streamer's channel become that viewer's per-channel credits.
Subscriptions are created **out-of-band**; the server only receives notifications.

```
Twitch ──► POST /api/webhooks/twitch/eventsub          server (handlers_eventsub)      store
   │  headers: Message-Id, -Timestamp, -Signature, -Type
   │────────────────────────────────────────────────► read RAW body
   │                                                    twitch.VerifyEventSubSignature (HMAC, const-time) ─► 403 on mismatch
   │                                                    EventSubTimestampFresh (≤10m) ─────────────────────► 403 if stale
   │                                                    switch Message-Type:
   │                                                      verification ─► 200 text/plain echo `challenge`
   │                                                      revocation   ─► log, 200
   │                                                      notification ─► parse channel.cheer / bits.use
   │                                                          store.CreditBits(msgId, user, broadcaster, bits)
   │                                                             INSERT bits_events (dup ⇒ no-op) + credits += bits + ledger
   │◄──── 200 (always, for a handled notification) ───────────────────────
```

- **Authenticated by HMAC**, not a cookie: `sha256=HMAC(secret, id+timestamp+body)`
  over the raw bytes. Bad signature or stale timestamp ⇒ `403`.
- **Idempotent:** the message id is inserted in the *same transaction* as the
  credit, so a Twitch retry / replay credits exactly once.
- **Anonymous cheers** (no `user_id`) are acknowledged but not credited — there's no
  balance to attribute them to.
- **Testing without bits:** with `DEV_LOGIN=1`, the submit page shows a one-click
  **+ Add test credits** button (backed by `POST /api/dev/credit`, which resolves
  the channel from the submit token and defaults the amount). The Twitch CLI can
  also POST a properly-signed cheer to the webhook to exercise verify + dedup +
  credit for real.

## 5. Moderation (streamer)

All are `POST /api/admin/*`, gated by `RequireStreamer`, acting on
`server.tenant(r).Queue`:

| Endpoint | Manager call | Effect |
| --- | --- | --- |
| `/api/admin/approve` (id) | `Approve(id)` | pending → approved queue; starts playing if idle |
| `/api/admin/reject` (id) | `Reject(id)` | pending → rejected (dropped) |
| `/api/admin/remove` (id) | `Remove(id)` | approved → skipped (removed from queue) |
| `/api/admin/skip` | `Skip()` | end now-playing immediately, advance |
| `/api/admin/pause` `/resume` | `Pause()` / `Resume()` | toggle playback pause |
| `/api/admin/clear` (`all`?) | `Clear(all)` | empty approved queue; `all` also clears pending + now-playing |
| `/api/admin/bypass` (enabled) | `SetBypass(b)` | auto-approve new submissions |

Every one emits a fresh `Snapshot` to the room, so admin + player update live.

## 6. Playback and "ended" (player / OBS)

```
Player page (/p/<key>)        server               registry/store      queue.Manager      hub
   │ GET /p/<key> ───────────► handlePlayerPage: GetStreamerByPlayerKey → render player.html (key injected)
   │ WS /ws?role=player&key=<key>
   │────────────────────────► handleWS: GetStreamerByPlayerKey → room = streamer id → hub.ServeWS
   │◄──── initial "state" (OnConnect → registry.InitialMessages) ───────
   │  ... plays nowPlaying (YouTube IFrame honoring start+duration, or <video>) ...
   │ POST /api/player/ended {key, id}
   │────────────────────────► handlePlayerEnded: GetStreamerByPlayerKey
   │                           tenant.Queue.Ended(id) ──► mark done, advance
   │                                                      broadcast ──► BroadcastTo(room,"state")
   │◄──── new "state" (next item now playing) ──────────
```

- `Ended(id)` only advances if `id` matches the current now-playing item (or `id`
  is empty) — this guards against a stale player reporting the end of a clip that
  already advanced.
- Playback auto-starts: whenever the queue becomes non-empty and nothing is
  playing (`startIfIdleLocked`), the front item is promoted to now-playing and
  broadcast. Pause does **not** block starting the next item — it only pauses an
  already-playing clip on the client.

## 7. WebSocket room resolution (security-critical)

`server.handleWS` is the single place rooms are assigned, and it never trusts the
client for the room:

```
GET /ws?role=admin           → authenticate cookie → room = streamer.ID
GET /ws?role=player&key=<k>  → store.GetStreamerByPlayerKey(k) → room = streamer.ID
anything else                → 400
```

Then `hub.ServeWS(w, r, room, role)` registers the client and immediately pushes
initial state via `OnConnect` (`registry.InitialMessages` → `state`, plus
`session` status for admins).
