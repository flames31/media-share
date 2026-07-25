package server

import "net/http"

// handlePlayerEnded is called by the player when a clip finishes (either its
// natural end or the duration cap). The id guards against a stale player
// advancing a video that already moved on.
func (s *Server) handlePlayerEnded(w http.ResponseWriter, r *http.Request) {
	var b idBody
	_ = decode(r, &b) // id is optional; empty forces advance
	s.mgr.Ended(b.ID)
	writeOK(w)
}
