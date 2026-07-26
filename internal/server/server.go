// Package server wires HTTP handlers for the platform: Twitch login, the admin
// console, per-streamer players, viewer submissions, and the WebSocket API.
package server

import (
	"html/template"
	"log/slog"
	"net/http"
	"os"

	"media-share/internal/auth"
	"media-share/internal/config"
	"media-share/internal/hub"
	"media-share/internal/store"
	"media-share/internal/tenant"
	"media-share/web"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg   *config.Config
	store *store.Store
	reg   *tenant.Registry
	auth  *auth.Authenticator
	hub   *hub.Hub
	tmpl  map[string]*template.Template
	mux   *http.ServeMux
}

// New builds a Server and parses templates. It panics if templates fail to
// parse, since that is a programming error, not a runtime condition.
func New(cfg *config.Config, st *store.Store, reg *tenant.Registry, a *auth.Authenticator, h *hub.Hub) *Server {
	s := &Server{cfg: cfg, store: st, reg: reg, auth: a, hub: h, tmpl: map[string]*template.Template{}}
	for _, name := range []string{"submit", "player", "admin", "login", "mod_claim"} {
		t, err := template.ParseFS(web.TemplatesFS, "templates/"+name+".html")
		if err != nil {
			slog.Error("parse template", "name", name, "err", err)
			os.Exit(1)
		}
		s.tmpl[name] = t
	}
	s.routes()
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("GET /admin", s.requireStreamerPage(s.handleAdminPage))
	mux.HandleFunc("GET /submit", s.handleSubmitPage)
	mux.HandleFunc("GET /s/{token}", s.handleSubmitPage)
	mux.HandleFunc("GET /p/{key}", s.handlePlayerPage)
	mux.HandleFunc("GET /mod/{token}", s.handleModClaimPage)

	// Auth (Log in with Twitch) — streamers and viewers share the callback.
	mux.HandleFunc("GET /auth/twitch/start", s.handleAuthStart)
	mux.HandleFunc("GET /auth/twitch/callback", s.handleAuthCallback)
	mux.HandleFunc("POST /auth/dev/login", s.handleDevLogin)
	mux.HandleFunc("POST /mod/{token}", s.handleModClaim)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Viewer auth (Log in with Twitch to submit + hold credits)
	mux.HandleFunc("GET /viewer/auth/start", s.handleViewerAuthStart)
	mux.HandleFunc("POST /viewer/logout", s.handleViewerLogout)
	mux.HandleFunc("POST /viewer/dev/login", s.handleViewerDevLogin)

	// Public API (tenant resolved from an invite token or player key)
	mux.HandleFunc("POST /api/submit", s.auth.RequireViewer(s.handleSubmit))
	mux.HandleFunc("GET /api/session/check", s.handleSessionCheck)
	mux.HandleFunc("POST /api/player/ended", s.handlePlayerEnded)
	mux.HandleFunc("GET /ws", s.handleWS)

	// Viewer credits
	mux.HandleFunc("GET /api/viewer/me", s.auth.RequireViewer(s.handleViewerMe))
	mux.HandleFunc("POST /api/dev/credit", s.handleDevCredit)

	// Twitch EventSub webhook (bits → credits). Public: authenticated by HMAC.
	mux.HandleFunc("POST /api/webhooks/twitch/eventsub", s.handleEventSub)

	// Admin API (cookie-gated; tenant taken from the authenticated streamer)
	mux.HandleFunc("GET /api/admin/me", s.auth.RequireStreamer(s.handleMe))
	mux.HandleFunc("GET /api/admin/state", s.auth.RequireStreamer(s.handleState))
	mux.HandleFunc("POST /api/admin/approve", s.auth.RequireStreamer(s.handleApprove))
	mux.HandleFunc("POST /api/admin/reject", s.auth.RequireStreamer(s.handleReject))
	mux.HandleFunc("POST /api/admin/remove", s.auth.RequireStreamer(s.handleRemove))
	mux.HandleFunc("POST /api/admin/skip", s.auth.RequireStreamer(s.handleSkip))
	mux.HandleFunc("POST /api/admin/pause", s.auth.RequireStreamer(s.handlePause))
	mux.HandleFunc("POST /api/admin/resume", s.auth.RequireStreamer(s.handleResume))
	mux.HandleFunc("POST /api/admin/clear", s.auth.RequireStreamer(s.handleClear))
	mux.HandleFunc("POST /api/admin/bypass", s.auth.RequireStreamer(s.handleBypass))

	// Media-share session control
	mux.HandleFunc("GET /api/admin/session", s.auth.RequireStreamer(s.handleSessionStatus))
	mux.HandleFunc("POST /api/admin/session/start", s.auth.RequireStreamer(s.handleSessionStart))
	mux.HandleFunc("POST /api/admin/session/stop", s.auth.RequireStreamer(s.handleSessionStop))
	mux.HandleFunc("POST /api/admin/session/regenerate", s.auth.RequireStreamer(s.handleSessionRegenerate))

	// Moderator link management (owner-only — a moderator cannot mint or revoke access)
	mux.HandleFunc("GET /api/admin/moderators", s.auth.RequireOwner(s.handleModeratorsStatus))
	mux.HandleFunc("POST /api/admin/moderators/link", s.auth.RequireOwner(s.handleModeratorsLink))
	mux.HandleFunc("POST /api/admin/moderators/revoke", s.auth.RequireOwner(s.handleModeratorsRevoke))

	// Static assets (embedded) and uploaded media (on disk).
	mux.Handle("GET /static/", http.FileServerFS(web.StaticFS))
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(s.cfg.MediaDir))))

	s.mux = mux
}

// tenant returns the authenticated streamer's tenant. It must only be called
// from handlers wrapped by RequireStreamer / requireStreamerPage.
func (s *Server) tenant(r *http.Request) *tenant.Tenant {
	st, _ := auth.StreamerFrom(r.Context())
	return s.reg.Get(st.ID)
}

// requireStreamerPage gates a page on login, redirecting to /login when absent.
func (s *Server) requireStreamerPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, role, err := s.auth.Authenticate(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := auth.WithRole(auth.WithStreamer(r.Context(), st), role)
		next(w, r.WithContext(ctx))
	}
}

// handleWS resolves the room/role securely, then upgrades the connection.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	role := hub.Role(r.URL.Query().Get("role"))
	var room string
	switch role {
	case hub.RoleAdmin:
		st, _, err := s.auth.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		room = st.ID
	case hub.RolePlayer:
		st, err := s.store.GetStreamerByPlayerKey(r.URL.Query().Get("key"))
		if err != nil {
			http.Error(w, "player not found", http.StatusNotFound)
			return
		}
		room = st.ID
	default:
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	s.hub.ServeWS(w, r, room, role)
}
