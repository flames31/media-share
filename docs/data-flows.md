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

## 4. Viewer submission

```
Submit page (/s/<token>)     server (handlers_submit)     registry        queue.Manager       hub
   │ GET /s/<token> ─────────► render submit.html (token injected as SessionToken)
   │ (optional) GET /api/session/check?s=<token> ─► reg.ResolveSession → {valid: bool}   (UX only)
   │ POST /api/submit (multipart: session, type, url|file, start, duration, name)
   │────────────────────────► handleSubmit
   │                           reg.ResolveSession(token) ──► tenant OR 403 "closed"
   │                           submitYouTube / submitUpload
   │                             parse/validate; save file to MEDIA_DIR if upload
   │                             tenant.Queue.Submit(item) ─────────────► append (pending|approved)
   │                                                                       startIfIdleLocked()
   │                                                                       broadcast Snapshot ──► BroadcastTo(room,"state")
   │◄──── {id,title,status} (JSON) ──────────────────────────────────────
```

- The submit handler **re-resolves the token server-side** — the authoritative
  gate. A closed/stopped session ⇒ `403 "Media share is closed right now."`
- YouTube: `queue.ParseYouTube` extracts the video id + optional start; an
  explicit `start` field overrides the URL's. `duration` defaults to 10s,
  clamped to ≤ 3600s.
- Upload: size- and extension-checked against config; stored under `MEDIA_DIR`
  as `<uuid><ext>`; `MediaURL` is `/media/<file>`. `duration` defaults to 0
  ("play whole file").
- With **bypass** on, `Submit` puts the item straight into the approved queue.

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
