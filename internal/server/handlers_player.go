package server

import "net/http"

// handlePlayerEnded is called by the player when a clip finishes. The player
// identifies its streamer via the player key; the item id guards against a stale
// player advancing a video that already moved on.
func (s *Server) handlePlayerEnded(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	streamer, err := s.store.GetStreamerByPlayerKey(b.Key)
	if err != nil {
		writeErr(w, http.StatusNotFound, "player not found")
		return
	}
	s.reg.Get(streamer.ID).Queue.Ended(b.ID)
	writeOK(w)
}
