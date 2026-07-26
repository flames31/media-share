package server

import (
	"errors"
	"log/slog"
	"net/http"

	"media-share/internal/auth"
	"media-share/internal/store"
)

// moderatorLink builds the shareable moderator-invite URL for a token.
func (s *Server) moderatorLink(token string) string {
	if token == "" {
		return ""
	}
	return s.cfg.BaseURL() + "/mod/" + token
}

// ownerID returns the authenticated owner's streamer id. These handlers are
// wrapped by RequireOwner, so the streamer in context is always the owner.
func (s *Server) ownerID(r *http.Request) string {
	st, _ := auth.StreamerFrom(r.Context())
	return st.ID
}

// handleModeratorsStatus returns the streamer's current moderator link (empty if
// none is active).
func (s *Server) handleModeratorsStatus(w http.ResponseWriter, r *http.Request) {
	token, err := s.store.ModeratorLink(s.ownerID(r))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, "could not load moderator link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"link": s.moderatorLink(token)})
}

// handleModeratorsLink mints (or regenerates) the streamer's moderator link. The
// old link, if any, stops working immediately.
func (s *Server) handleModeratorsLink(w http.ResponseWriter, r *http.Request) {
	owner := s.ownerID(r)
	token, err := s.store.RegenerateModeratorLink(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create moderator link")
		return
	}
	slog.Info("moderator link generated", "streamer_id", owner)
	writeJSON(w, http.StatusOK, map[string]string{"link": s.moderatorLink(token)})
}

// handleModeratorsRevoke removes the moderator link and kicks any current
// moderators (their sessions are deleted).
func (s *Server) handleModeratorsRevoke(w http.ResponseWriter, r *http.Request) {
	owner := s.ownerID(r)
	if err := s.store.RevokeModeratorAccess(owner); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke moderator access")
		return
	}
	slog.Warn("moderator access revoked; current moderators signed out", "streamer_id", owner)
	writeOK(w)
}
