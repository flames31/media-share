package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"media-share/internal/queue"
)

// handleSubmit accepts a multipart submission (YouTube link or file upload) and
// adds it to the queue as a pending item (or approved, if bypass is on).
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	// Cap the request body; add a little headroom over the file cap for form fields.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes()+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not parse form (file too large?)")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) > 40 {
		name = name[:40]
	}

	switch r.FormValue("type") {
	case "youtube":
		s.submitYouTube(w, r, name)
	case "upload":
		s.submitUpload(w, r, name)
	default:
		writeErr(w, http.StatusBadRequest, "unknown submission type")
	}
}

func (s *Server) submitYouTube(w http.ResponseWriter, r *http.Request, name string) {
	raw := strings.TrimSpace(r.FormValue("url"))
	id, startFromURL, ok := queue.ParseYouTube(raw)
	if !ok {
		writeErr(w, http.StatusBadRequest, "that doesn't look like a valid YouTube link")
		return
	}

	// An explicit start field overrides the one embedded in the URL.
	start := startFromURL
	if sv := strings.TrimSpace(r.FormValue("start")); sv != "" {
		if parsed, ok := parseStart(sv); ok {
			start = parsed
		}
	}

	duration := clampDuration(r.FormValue("duration"), 10)

	item := &queue.Item{
		Type:            queue.TypeYouTube,
		Title:           "YouTube · " + id,
		SubmitterName:   name,
		YouTubeID:       id,
		StartSeconds:    start,
		DurationSeconds: duration,
	}
	created := s.mgr.Submit(item)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     created.ID,
		"title":  created.Title,
		"status": string(created.Status),
	})
}

func (s *Server) submitUpload(w http.ResponseWriter, r *http.Request, name string) {
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no file provided")
		return
	}
	defer file.Close()

	if header.Size > s.cfg.MaxUploadBytes() {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d MB limit", s.cfg.MaxUploadMB))
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !s.cfg.ExtAllowed(ext) {
		writeErr(w, http.StatusBadRequest, "file type not allowed: "+ext)
		return
	}

	if err := os.MkdirAll(s.cfg.MediaDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "server storage error")
		return
	}

	storedName := uuid.NewString() + ext
	dstPath := filepath.Join(s.cfg.MediaDir, storedName)
	dst, err := os.Create(dstPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save file")
		return
	}
	written, err := io.Copy(dst, file)
	closeErr := dst.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(dstPath)
		writeErr(w, http.StatusInternalServerError, "could not save file")
		return
	}
	if written > s.cfg.MaxUploadBytes() {
		_ = os.Remove(dstPath)
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}

	duration := clampDuration(r.FormValue("duration"), 0) // 0 = whole file

	title := origTitle(header.Filename)
	item := &queue.Item{
		Type:            queue.TypeUpload,
		Title:           title,
		SubmitterName:   name,
		MediaURL:        "/media/" + storedName,
		DurationSeconds: duration,
	}
	created := s.mgr.Submit(item)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     created.ID,
		"title":  created.Title,
		"status": string(created.Status),
	})
}

// --- small parsing helpers ---

// parseStart accepts plain seconds or mm:ss / h:mm:ss.
func parseStart(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		total := 0
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 0 {
				return 0, false
			}
			total = total*60 + n
		}
		return total, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// clampDuration parses a duration string; on failure returns def. Caps at a
// generous upper bound to avoid absurd values.
func clampDuration(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	const maxSeconds = 3600
	if n > maxSeconds {
		n = maxSeconds
	}
	return n
}

func origTitle(filename string) string {
	base := filepath.Base(filename)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	name = strings.TrimSpace(name)
	if name == "" {
		return "Upload " + time.Now().Format("15:04:05")
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}
