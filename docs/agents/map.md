# Code map — "I need to change X → open Y"

Repo-relative paths. Line counts are rough; treat them as "how big is this."

## Entry point & wiring

| File | Responsibility |
| --- | --- |
| `main.go` | Boot: load config → open SQLite → build hub + registry (`h.OnConnect = reg.InitialMessages`) → oauth client → authenticator → server → HTTP server + graceful shutdown. **This is where dependencies are constructed.** |

## `internal/server` — HTTP layer (the only package that speaks HTTP)

| File | What's in it |
| --- | --- |
| `server.go` | `Server` struct, `New` (parses templates), **`routes()` = the full route table**, `tenant(r)` (authed streamer → tenant), `requireStreamerPage`, `handleWS` (secure room resolution). |
| `render.go` | `writeJSON`/`writeErr`, `render` (buffered template exec), page handlers: `handleIndex`, `handleLoginPage`, `handleAdminPage`, `handleSubmitPage`, `handlePlayerPage`, plus `handleState`, `handleMe`. |
| `handlers_auth.go` | `handleAuthStart`, `handleAuthCallback`, `handleDevLogin`, `handleLogout`, `redirectLoginError`, and the moderator-claim handlers `handleModClaimPage` (GET landing) / `handleModClaim` (POST → moderator session). |
| `handlers_moderators.go` | Owner-only moderator-link endpoints: `handleModeratorsStatus`/`Link`/`Revoke`, plus `moderatorLink(token)` and `ownerID(r)` helpers. |
| `handlers_admin.go` | Queue moderation endpoints: approve/reject/remove/skip/pause/resume/clear/bypass. Shared `decode` (strict, `DisallowUnknownFields`) + `idBody`/`writeOK` helpers; each calls `s.tenant(r).Queue.*`. |
| `handlers_session.go` | `sessionLink`, `adminSessionView`, `handleSessionCheck` (public), and session status/start/stop/regenerate. |
| `handlers_submit.go` | `handleSubmit` (behind `RequireViewer`) + `submitYouTube`/`submitUpload`, the credit `chargeSubmit`/`billableDuration` helpers (`minBillableSeconds`), and parsing helpers (`parseStart`, `clampDuration`, `origTitle`). The token gate + credit deduction live here. |
| `handlers_player.go` | `handlePlayerEnded` (player key → tenant → `Queue.Ended`). |
| `handlers_viewer.go` | Viewer auth + credits: `handleViewerAuthStart`, `handleViewerLogout`, `handleViewerMe` (per-channel balance), `handleViewerDevLogin`, `handleDevCredit` (dev top-up). |
| `handlers_eventsub.go` | Twitch EventSub webhook `handleEventSub`: HMAC verify + replay guard → verification-challenge / revocation / notification; `handleCheerNotification` → `store.CreditBits`. |

> There's also a small `decode`/`writeOK` helper used by admin/player handlers —
> grep for them if you add a handler that needs JSON-body decoding.

## Domain packages (no HTTP knowledge)

| Package / file | What's in it |
| --- | --- |
| `internal/tenant/registry.go` | `Registry`, `Tenant`, `Get` (lazy, wires broadcasts to the room), `ResolveSession`, `InitialMessages`, `reindex`, and `StartSession`/`RegenerateSession`/`StopSession`. **Tenant isolation lives here.** |
| `internal/queue/queue.go` | `Manager`, `Item`, `Snapshot`, the state machine, all mutations. |
| `internal/queue/youtube.go` | `ParseYouTube` — id + start-time extraction from various YouTube URL shapes. |
| `internal/session/session.go` | `Manager` for the streamer-controlled submission window; secret token vs. public `Status`. |
| `internal/hub/hub.go` | `Hub`, rooms, `BroadcastTo`, `ServeWS`, `OnConnect`, read/write pumps, ping/pong. |

## Auth & identity

| File | What's in it |
| --- | --- |
| `internal/auth/auth.go` | `Authenticator`: streamer login (`AuthorizeURL`, `Login`, `LoginModerator`, `DevLogin`, `Authenticate`, `RequireStreamer`/`RequireOwner`) **and** viewer login (`ViewerAuthorizeURL`, `LoginViewer`, `DevLoginViewer`, `AuthenticateViewer`, `RequireViewer`, `LogoutViewer`, `WithViewer`/`ViewerFrom`). `ConsumeState` returns the OAuth state's `kind` (owner/viewer) + return token. `sameSite` CSRF guard; `sid`/`vsid` cookie helpers. |
| `internal/oauth/oauth.go` | Twitch Authorization-Code client: `AuthorizeURL`, `ExchangeCodeForUser`, `getUser` (Helix). Scope-agnostic (reused for both streamer and viewer identity login). Endpoints are package vars for test stubbing. |
| `internal/store/store.go` | SQLite: `Open`/`migrate`, streamers/auth-sessions/moderator-links (as before), **plus viewers/credits**: `Viewer`, `UpsertViewer`/`GetViewer`, `CreateViewerSession`/`GetValidViewerSession`/`DeleteViewerSession`, `CreditBits` (dedup+credit tx), `SpendCredits` (atomic conditional deduct), `Balance`, `GrantCredits`. |
| `internal/twitch/eventsub.go` | `VerifyEventSubSignature` (HMAC over id+timestamp+raw body, const-time), `EventSubSignature`, `EventSubTimestampFresh`, and the EventSub header/message-type constants. **This is wired into the running server** (unlike the rest of `internal/twitch`). |

## Config

| File | What's in it |
| --- | --- |
| `internal/config/config.go` | `Config` struct + `Load()`; derived helpers `OAuthEnabled`, `BaseURL`, `TwitchRedirectURI`, `CookieSecure`, `MaxUploadBytes`, `ExtAllowed`; `env`/`envInt`/`envBool` readers. Credit settings: `TwitchEventSubSecret`, `CreditsEnabled`, `CreditsPerSecond`. |
| `internal/config/dotenv.go` | `.env` loader (`loadDotEnv`, `parseDotEnvLine`). Real env wins; `ENV_FILE` overrides path; missing file is fine. |
| `internal/logging/logging.go` | `Init()` installs the process-wide `log/slog` handler (text → stderr); `SetDevLogin(bool)` raises the level to Debug under `DEV_LOGIN`. Everything else just calls `slog.Info/Warn/Error/Debug`. Swap the writer here to log to a file. |

## Frontend (embedded via `web/embed.go`)

| File | Page |
| --- | --- |
| `web/templates/login.html` | Login: Twitch button + optional Dev-login. |
| `web/templates/admin.html` + `web/static/admin.js` | Console: cookie auth, session card, player-URL card, pending/queue/now-playing, controls. WS `role=admin`. |
| `web/templates/player.html` + `web/static/player.js` | OBS player: injects `__PLAYER_KEY__`; WS `role=player&key=`; posts `/api/player/ended`. |
| `web/templates/submit.html` + `web/static/submit.js` | Viewer submission form; gates on Twitch viewer login, shows per-channel balance + live cost preview, handles 402. Uses the injected invite token. |
| `web/templates/mod_claim.html` | Moderator-invite landing page ("Enter as moderator" → POST `/mod/{token}`). |
| `web/static/app.css` | Shared styles. |
| `web/templates/twitch_callback.html` | Legacy/aux callback template. |

## Deferred (present, not wired into the running server)

| Path | Status |
| --- | --- |
| `internal/twitch/` (`bot.go`, `controller.go`, `oauth.go`, `store.go`, `parse.go` + tests) | The single-tenant chat bot (`!skip`, `!pause`, …). Compiles and tests pass, but `main.go` does not start it. A future phase re-wires it per tenant. Don't assume any of it executes at runtime. **Exception:** `eventsub.go` in this package *is* wired (used by `handlers_eventsub.go`). |

## Tests (mirror their packages)

`*_test.go` next to each package: `config` (dotenv), `store`, `oauth`, `queue`,
`session`, `hub`, `tenant`, and `twitch`. Run `go test ./...`.
