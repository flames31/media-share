# media-share — Documentation

Detailed docs for the multi-tenant Twitch media-share platform. Start here.

## For humans

| Doc | What's in it |
| --- | --- |
| [architecture.md](architecture.md) | The big picture: layers, packages, the tenant model, and how a request becomes a broadcast. |
| [data-flows.md](data-flows.md) | Step-by-step sequences: login, start a session, viewer submission, moderation, playback, session teardown. |
| [http-api.md](http-api.md) | Every route: method, auth, inputs, outputs, and which layer handles it. |
| [data-model.md](data-model.md) | SQLite schema, in-memory state, and what is persistent vs. ephemeral. |

## For agents (AI assistants working in this repo)

The [`agents/`](agents/) folder is written specifically for an AI coding agent
picking this project up cold — how it's laid out, where things live, how to debug,
and how to add features without breaking the tenant-isolation invariants.

| Doc | What's in it |
| --- | --- |
| [agents/README.md](agents/README.md) | Orientation + a mental model you can hold in your head. Read this first. |
| [agents/map.md](agents/map.md) | File-by-file map: "I need to change X → open Y". |
| [agents/debugging.md](agents/debugging.md) | Symptom → likely cause → where to look. Common failure modes. |
| [agents/adding-features.md](agents/adding-features.md) | Extension points and worked recipes (new API, new item type, re-wiring the bot). |
| [agents/conventions.md](agents/conventions.md) | Invariants and idioms you must not violate (locking, broadcast, tenant scoping). |

## Quick facts

- **Language / runtime:** Go (see `go.mod` for the version), stdlib `net/http`
  with Go 1.22+ method+pattern routing (`GET /s/{token}`, `r.PathValue`).
- **Dependencies:** `gorilla/websocket`, `google/uuid`, `modernc.org/sqlite`
  (pure-Go, no cgo).
- **Persistence:** SQLite for streamer accounts + login sessions only. All
  queue/session runtime state is in memory and resets on restart.
- **Entry point:** `main.go` wires everything; `internal/server` owns HTTP.
- **Frontend:** server-rendered `html/template` pages + vanilla JS, all embedded
  via `//go:embed` (`web/embed.go`).

> Keep these docs honest. If you change a data flow, an invariant, or a route,
> update the matching doc in the same change.
