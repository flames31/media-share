package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
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

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.handleSubmitPage(w, r)
}

func (s *Server) handleSubmitPage(w http.ResponseWriter, r *http.Request) {
	accept := strings.Join(s.cfg.AllowedExt, ",") // e.g. ".mp4,.webm"
	display := strings.ReplaceAll(strings.Join(s.cfg.AllowedExt, " "), ".", "")
	s.render(w, "submit", map[string]any{
		"AcceptAttr":        accept,
		"AllowedExtDisplay": strings.ToUpper(display),
		"MaxUploadMB":       s.cfg.MaxUploadMB,
	})
}

func (s *Server) handlePlayerPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "player", nil)
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "admin", nil)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Snapshot())
}
