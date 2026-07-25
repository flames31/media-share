# Adding features

Extension points and worked recipes. The golden rule threads through all of them:
**resolve the tenant server-side, act on its `Queue`/`Session`, and let the
existing broadcast carry the update.** If you follow the patterns already in the
code, live updates and isolation come for free.

## Where to plug in

| You want to… | Touch |
| --- | --- |
| Add an admin action on the queue | new method on `queue.Manager` + handler in `handlers_admin.go` + route + `admin.js` button |
| Add a public (viewer) action | handler resolving tenant via `reg.ResolveSession` or player key + route (no `RequireStreamer`) |
| Add a new submission media type | `queue.ItemType` + a `submitX` in `handlers_submit.go` + player rendering in `player.js` |
| Change what clients receive live | the `Snapshot`/`Status` shape + the templates/JS that read it |
| Add per-account persistent data | a column/table in `store` + a method + a caller |
| Re-enable the Twitch chat bot | wire `internal/twitch` per tenant (see recipe below) |

## Recipe: a new admin queue action (e.g. "move to front")

1. **Domain method** in `internal/queue/queue.go`, following the locking pattern
   exactly:
   ```go
   func (m *Manager) MoveToFront(id string) bool {
       m.mu.Lock()
       it, idx := findByID(m.queue, id)
       if it == nil { m.mu.Unlock(); return false }
       m.queue = append([]*Item{it}, removeAt(m.queue, idx)...)
       snap := m.snapshotLocked()
       fn := m.broadcast
       m.mu.Unlock()
       m.emit(snap, fn)   // broadcast OUTSIDE the lock
       return true
   }
   ```
2. **Handler** in `internal/server/handlers_admin.go` (mirror the existing ones —
   decode `{id}`, call `s.tenant(r).Queue.MoveToFront(id)`, `writeOK`/`writeErr`).
3. **Route** in `server.go` `routes()`:
   ```go
   mux.HandleFunc("POST /api/admin/move-front", s.auth.RequireStreamer(s.handleMoveFront))
   ```
4. **Frontend**: a button in `admin.html` + a `fetch('/api/admin/move-front', …)`
   in `admin.js` (cookie is sent automatically; no `Authorization` header).
5. **Test** the manager method in `queue_test.go`.

No WebSocket code needed — `emit` → the room broadcast already updates every
client.

## Recipe: a new submission type (e.g. a still image)

1. Add `TypeImage ItemType = "image"` in `queue.go`.
2. Add fields to `Item` if needed (JSON-tagged; they ride along in the snapshot).
3. Add a `submitImage(w, r, t, name)` in `handlers_submit.go` and a `case
   "image"` in `handleSubmit`'s switch. Validate/store like `submitUpload` does.
4. Teach `player.js` to render the new type when it's `nowPlaying`.
5. If it uses uploads, respect `ExtAllowed`/`MaxUploadBytes`.

## Recipe: expose more live state to clients

The `state` broadcast payload **is** `queue.Snapshot`. To add a field:
1. Add it to `Snapshot` and populate it in `snapshotLocked`.
2. Read it in `admin.js` / `player.js`.
That's it — every mutation already re-emits the snapshot. For session-related
state, do the same with `session.Status` (public, no token) and the `session`
message.

## Recipe: persist new per-account data

1. Add a column to `streamers` (or a new table) in `store.migrate` — it uses
   `CREATE TABLE IF NOT EXISTS`, safe to extend; for existing DBs you'll need an
   `ALTER TABLE`/migration step.
2. Add store methods (parameterized SQL only) + a test in `store_test.go`.
3. Call them from the relevant handler/flow.
Keep runtime queue/session state **out** of SQLite — that separation is
deliberate (see [../data-model.md](../data-model.md)).

## Recipe: re-wire the Twitch chat bot (deferred feature)

The `internal/twitch` package (bot + controller) exists from the single-tenant
build but is **not** wired in `main.go`. To bring it back per-tenant:

1. Decide identity: the bot needs `chat:read`/`chat:edit` scopes. That means
   extending login to request those scopes (`oauth.AuthorizeURL(state, scopes...)`
   already accepts them) and storing the user token — today only identity is
   fetched, no token is persisted. This is the main new work.
2. Give each `Tenant` a controller that maps chat commands (`!skip`, `!pause`) to
   the tenant's `Queue` methods — the same calls the admin API makes.
3. Start/stop the per-tenant bot alongside the media-share session lifecycle.
4. Surface it in the admin UI (the card was hidden in the platform build).

Treat this as a real project, not a small change — it crosses auth, storage,
tenant lifecycle, and UI. Read `internal/twitch/controller.go` and `bot.go`
first; check they still match the current `queue.Manager` API before reusing.

## Before you finish — checklist

- [ ] `gofmt -l .` prints nothing; `go vet ./...` clean.
- [ ] `go test ./...` green; you added a test for new domain logic.
- [ ] New admin routes are wrapped in `RequireStreamer`; new WS rooms resolved
      server-side (never from client input).
- [ ] Any new broadcast happens **outside** the manager lock.
- [ ] Session control still goes through the `Tenant` wrapper, not `Session.*`.
- [ ] You updated the matching doc: [../http-api.md](../http-api.md) for a new
      route, [../data-model.md](../data-model.md) for new state,
      [../data-flows.md](../data-flows.md) for a new flow.
