// Package server wires HTTP handlers for the submission page, player, admin
// console, and the JSON/WebSocket APIs.
package server

import (
	"crypto/subtle"
	"html/template"
	"log"
	"net/http"
	"strings"

	"media-share/internal/config"
	"media-share/internal/hub"
	"media-share/internal/queue"
	"media-share/web"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg  *config.Config
	mgr  *queue.Manager
	hub  *hub.Hub
	tmpl map[string]*template.Template
	mux  *http.ServeMux
}

// New builds a Server and parses templates. It panics if templates fail to
// parse, since that is a programming error, not a runtime condition.
func New(cfg *config.Config, mgr *queue.Manager, h *hub.Hub) *Server {
	s := &Server{cfg: cfg, mgr: mgr, hub: h, tmpl: map[string]*template.Template{}}
	for _, name := range []string{"submit", "player", "admin"} {
		t, err := template.ParseFS(web.TemplatesFS, "templates/"+name+".html")
		if err != nil {
			log.Fatalf("parse template %s: %v", name, err)
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
	mux.HandleFunc("GET /submit", s.handleSubmitPage)
	mux.HandleFunc("GET /player", s.handlePlayerPage)
	mux.HandleFunc("GET /admin", s.handleAdminPage)

	// Public API
	mux.HandleFunc("POST /api/submit", s.handleSubmit)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/player/ended", s.handlePlayerEnded)
	mux.HandleFunc("GET /ws", s.hub.ServeWS)

	// Admin API (auth-gated)
	mux.HandleFunc("GET /api/admin/ping", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/admin/approve", s.requireAdmin(s.handleApprove))
	mux.HandleFunc("POST /api/admin/reject", s.requireAdmin(s.handleReject))
	mux.HandleFunc("POST /api/admin/remove", s.requireAdmin(s.handleRemove))
	mux.HandleFunc("POST /api/admin/skip", s.requireAdmin(s.handleSkip))
	mux.HandleFunc("POST /api/admin/pause", s.requireAdmin(s.handlePause))
	mux.HandleFunc("POST /api/admin/resume", s.requireAdmin(s.handleResume))
	mux.HandleFunc("POST /api/admin/clear", s.requireAdmin(s.handleClear))
	mux.HandleFunc("POST /api/admin/bypass", s.requireAdmin(s.handleBypass))

	// Static assets (embedded) and uploaded media (on disk).
	mux.Handle("GET /static/", http.FileServerFS(web.StaticFS))
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(s.cfg.MediaDir))))

	s.mux = mux
}

// requireAdmin wraps a handler with bearer-token auth. If ADMIN_TOKEN is unset,
// access is allowed (dev mode) — a startup warning is logged in config.Load.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken != "" && !s.validToken(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Server) validToken(r *http.Request) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AdminToken)) == 1
}
