package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// idBody is the common request shape for actions targeting a single item.
type idBody struct {
	ID string `json:"id"`
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if errors.Is(err, io.EOF) { // empty body is allowed; fields stay zero
		return nil
	}
	return err
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var b idBody
	if err := decode(r, &b); err != nil || b.ID == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	if !s.mgr.Approve(b.ID) {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	writeOK(w)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	var b idBody
	if err := decode(r, &b); err != nil || b.ID == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	if !s.mgr.Reject(b.ID) {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	writeOK(w)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	var b idBody
	if err := decode(r, &b); err != nil || b.ID == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	if !s.mgr.Remove(b.ID) {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	writeOK(w)
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	s.mgr.Skip()
	writeOK(w)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.mgr.Pause()
	writeOK(w)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.mgr.Resume()
	writeOK(w)
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scope string `json:"scope"`
	}
	_ = decode(r, &b)
	s.mgr.Clear(b.Scope == "all")
	writeOK(w)
}

func (s *Server) handleBypass(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	s.mgr.SetBypass(b.Enabled)
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
