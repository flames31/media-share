package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"media-share/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
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
		log.Printf("render %s: %v", name, err)
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
	if _, err := s.auth.Authenticate(r); err == nil {
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
	s.render(w, "admin", map[string]any{
		"DisplayName": st.DisplayName,
		"PlayerURL":   s.cfg.BaseURL() + "/p/" + st.PlayerKey,
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
	s.render(w, "submit", map[string]any{
		"AcceptAttr":        accept,
		"AllowedExtDisplay": strings.ToUpper(display),
		"MaxUploadMB":       s.cfg.MaxUploadMB,
		"SessionToken":      token,
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

// handleMe returns the logged-in streamer's identity + player URL for the admin UI.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	st, _ := auth.StreamerFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"login":       st.Login,
		"displayName": st.DisplayName,
		"playerUrl":   s.cfg.BaseURL() + "/p/" + st.PlayerKey,
	})
}
