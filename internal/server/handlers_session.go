package server

import (
	"net/http"
	"strings"
)

// sessionLink builds the shareable invite URL for a token.
func (s *Server) sessionLink(token string) string {
	if token == "" {
		return ""
	}
	return s.cfg.BaseURL() + "/s/" + token
}

// adminSessionView is the admin-only view including the secret token + link.
type adminSessionView struct {
	Active    bool   `json:"active"`
	StartedAt string `json:"startedAt,omitempty"`
	Token     string `json:"token,omitempty"`
	Link      string `json:"link,omitempty"`
}

func (s *Server) adminSessionView(r *http.Request) adminSessionView {
	t := s.tenant(r)
	st := t.Session.Status()
	v := adminSessionView{Active: st.Active}
	if st.Active {
		v.StartedAt = st.StartedAt.Format("2006-01-02T15:04:05Z07:00")
		v.Token = t.Session.Token()
		v.Link = s.sessionLink(v.Token)
	}
	return v
}

// handleSessionCheck is public: it reports whether a given invite token is
// currently valid, without revealing anything about the streamer.
func (s *Server) handleSessionCheck(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("s"))
	_, ok := s.reg.ResolveSession(token)
	writeJSON(w, http.StatusOK, map[string]bool{"valid": ok})
}

func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.adminSessionView(r))
}

func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	s.tenant(r).StartSession()
	writeJSON(w, http.StatusOK, s.adminSessionView(r))
}

func (s *Server) handleSessionRegenerate(w http.ResponseWriter, r *http.Request) {
	s.tenant(r).RegenerateSession()
	writeJSON(w, http.StatusOK, s.adminSessionView(r))
}

func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request) {
	s.tenant(r).StopSession()
	writeJSON(w, http.StatusOK, s.adminSessionView(r))
}
