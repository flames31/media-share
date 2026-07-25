package server

import (
	"net/http"
	"net/url"
)

// handleAuthStart redirects to Twitch's consent screen to begin login.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OAuthEnabled() {
		http.Error(w, "Twitch login is not configured on this server", http.StatusPreconditionFailed)
		return
	}
	http.Redirect(w, r, s.auth.AuthorizeURL(), http.StatusSeeOther)
}

// handleAuthCallback completes login and drops the streamer at the console.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		redirectLoginError(w, r, q.Get("error_description"))
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		redirectLoginError(w, r, "Missing code or state.")
		return
	}
	if _, err := s.auth.Login(r.Context(), w, code, state); err != nil {
		redirectLoginError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleDevLogin logs in as a local dev account. Only enabled when DEV_LOGIN=1.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		http.NotFound(w, r)
		return
	}
	if _, err := s.auth.DevLogin(w); err != nil {
		redirectLoginError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func redirectLoginError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}
