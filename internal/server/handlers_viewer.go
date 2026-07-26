package server

import (
	"net/http"
	"strings"

	"media-share/internal/auth"
)

// handleViewerAuthStart redirects a viewer to Twitch consent, remembering the
// submit token so they return to the right streamer's submit page afterward.
func (s *Server) handleViewerAuthStart(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OAuthEnabled() {
		http.Error(w, "Twitch login is not configured on this server", http.StatusPreconditionFailed)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("s"))
	http.Redirect(w, r, s.auth.ViewerAuthorizeURL(token), http.StatusSeeOther)
}

// handleViewerLogout ends the viewer session and returns to the submit page.
func (s *Server) handleViewerLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.LogoutViewer(w, r)
	http.Redirect(w, r, submitReturnPath(strings.TrimSpace(r.FormValue("s"))), http.StatusSeeOther)
}

// handleViewerMe returns the logged-in viewer's identity and their credit balance
// in the channel identified by the ?s= submit token. Used by the submit page to
// render login state, balance, and a live cost preview.
func (s *Server) handleViewerMe(w http.ResponseWriter, r *http.Request) {
	v, _ := auth.ViewerFrom(r.Context())

	resp := map[string]any{
		"displayName":      v.DisplayName,
		"login":            v.Login,
		"creditsEnabled":   s.cfg.CreditsEnabled,
		"creditsPerSecond": s.cfg.CreditsPerSecond,
		"balance":          int64(0),
	}
	// Balance is per-channel, so it only means something once we know which
	// streamer this submit token belongs to.
	if t, ok := s.reg.ResolveSession(strings.TrimSpace(r.URL.Query().Get("s"))); ok {
		bal, err := s.store.Balance(v.ID, t.StreamerID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not read balance")
			return
		}
		resp["balance"] = bal
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleViewerDevLogin logs the caller in as a fixed dev viewer. DEV_LOGIN only.
func (s *Server) handleViewerDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		http.NotFound(w, r)
		return
	}
	if _, err := s.auth.DevLoginViewer(w); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, submitReturnPath(strings.TrimSpace(r.FormValue("s"))), http.StatusSeeOther)
}

// devCreditDefault is how many credits the one-click dev top-up grants.
const devCreditDefault = 1000

// handleDevCredit grants credits to the current dev viewer so the spend path can be
// tested without cheering real bits. DEV_LOGIN only. To keep browser testing
// one-click, the channel is resolved from the submit token `s` (so the caller
// needn't know the streamer id) and the amount defaults to devCreditDefault.
func (s *Server) handleDevCredit(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		http.NotFound(w, r)
		return
	}
	v, err := s.auth.AuthenticateViewer(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "log in as a viewer first")
		return
	}
	var b struct {
		Session    string `json:"s"`          // submit token → resolves the channel
		StreamerID string `json:"streamerId"` // optional explicit override
		Amount     int64  `json:"amount"`     // optional; defaults to devCreditDefault
	}
	_ = decode(r, &b)

	streamerID := b.StreamerID
	if streamerID == "" {
		if t, ok := s.reg.ResolveSession(strings.TrimSpace(b.Session)); ok {
			streamerID = t.StreamerID
		}
	}
	if streamerID == "" {
		writeErr(w, http.StatusBadRequest, "open the media share first (need a valid session token) or pass streamerId")
		return
	}

	amount := b.Amount
	if amount <= 0 {
		amount = devCreditDefault
	}
	if err := s.store.GrantCredits(v.ID, streamerID, amount); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not grant credits")
		return
	}
	bal, _ := s.store.Balance(v.ID, streamerID)
	writeJSON(w, http.StatusOK, map[string]any{"balance": bal, "granted": amount})
}
