package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"media-share/internal/auth"
	"media-share/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode failed", "err", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// render executes a named template into a buffer first so a template error does
// not leave a half-written response.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		slog.Error("render template failed", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// handleIndex is the landing page: logged-in streamers go to the console,
// everyone else sees the login page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, _, err := s.auth.Authenticate(r); err == nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.handleLoginPage(w, r)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", map[string]any{
		"OAuthEnabled": s.cfg.OAuthEnabled(),
		"DevLogin":     s.cfg.DevLogin,
		"Error":        r.URL.Query().Get("error"),
	})
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	st, _ := auth.StreamerFrom(r.Context())
	isOwner := auth.RoleFrom(r.Context()) == store.RoleOwner
	s.render(w, "admin", map[string]any{
		// For an owner this is their own name; for a moderator it's the owner
		// whose console they're running, so they know whose channel it is.
		"HeaderName": st.DisplayName,
		"IsOwner":    isOwner,
		"PlayerURL":  s.cfg.BaseURL() + "/p/" + st.PlayerKey,
	})
}

func (s *Server) handleSubmitPage(w http.ResponseWriter, r *http.Request) {
	accept := strings.Join(s.cfg.AllowedExt, ",") // e.g. ".mp4,.webm"
	display := strings.ReplaceAll(strings.Join(s.cfg.AllowedExt, " "), ".", "")
	// The invite token comes from the /s/{token} path, or a ?s= query fallback.
	token := r.PathValue("token")
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("s"))
	}
	// Viewer login state so the page can show "Log in with Twitch" vs the form.
	var viewerName string
	if v, err := s.auth.AuthenticateViewer(r); err == nil {
		viewerName = v.DisplayName
	}
	s.render(w, "submit", map[string]any{
		"AcceptAttr":        accept,
		"AllowedExtDisplay": strings.ToUpper(display),
		"MaxUploadMB":       s.cfg.MaxUploadMB,
		"SessionToken":      token,
		"OAuthEnabled":      s.cfg.OAuthEnabled(),
		"DevLogin":          s.cfg.DevLogin,
		"CreditsEnabled":    s.cfg.CreditsEnabled,
		"CreditsPerSecond":  s.cfg.CreditsPerSecond,
		"ViewerName":        viewerName,
		"LoggedIn":          viewerName != "",
	})
}

func (s *Server) handlePlayerPage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	st, err := s.store.GetStreamerByPlayerKey(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "player", map[string]any{
		"PlayerKey":    key,
		"StreamerName": st.DisplayName,
	})
}

// handleState returns the authenticated streamer's queue snapshot.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tenant(r).Queue.Snapshot())
}

// handleMe returns the logged-in caller's identity, role, and (for owners) the
// player URL for the admin UI.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	st, _ := auth.StreamerFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"login":       st.Login,
		"displayName": st.DisplayName,
		"role":        auth.RoleFrom(r.Context()),
		"playerUrl":   s.cfg.BaseURL() + "/p/" + st.PlayerKey,
	})
}
