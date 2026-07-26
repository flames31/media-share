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

	"media-share/internal/auth"
	"media-share/internal/queue"
	"media-share/internal/store"
	"media-share/internal/tenant"
)

// minBillableSeconds is the shortest play length a paid clip can be charged for.
// It doubles as the enforced playback cap, so the credits paid always match the
// time played (a viewer can't pay for 10s and play a full-length clip).
const minBillableSeconds = 10

// handleSubmit accepts a multipart submission (YouTube link or file upload) from a
// logged-in viewer and adds it to the queue, charging the viewer's per-channel
// credits first. It runs behind RequireViewer, so the viewer identity is trusted.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	viewer, ok := auth.ViewerFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "log in to submit")
		return
	}

	// Cap the request body; add a little headroom over the file cap for form fields.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes()+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not parse form (file too large?)")
		return
	}

	// Submissions are only accepted while an open session's invite token resolves
	// to a streamer. This is the authoritative check; the page gate is just UX.
	t, ok := s.reg.ResolveSession(strings.TrimSpace(r.FormValue("session")))
	if !ok {
		writeErr(w, http.StatusForbidden, "Media share is closed right now.")
		return
	}

	switch r.FormValue("type") {
	case "youtube":
		s.submitYouTube(w, r, t, viewer)
	case "upload":
		s.submitUpload(w, r, t, viewer)
	default:
		writeErr(w, http.StatusBadRequest, "unknown submission type")
	}
}

// chargeSubmit deducts the credit cost for a clip of the given play length from the
// viewer's balance in this channel, before the clip is queued. It returns false
// (after writing a 402 with the cost and current balance) when the viewer can't
// afford it. When credits are disabled it is a no-op that returns true.
func (s *Server) chargeSubmit(w http.ResponseWriter, v *store.Viewer, streamerID string, durationSeconds int, ref string) bool {
	if !s.cfg.CreditsEnabled {
		return true
	}
	cost := int64(durationSeconds) * s.cfg.CreditsPerSecond
	bal, ok, err := s.store.SpendCredits(v.ID, streamerID, cost, ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not process credits")
		return false
	}
	if !ok {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":   "You don't have enough credits — cheer bits in this channel to top up.",
			"cost":    cost,
			"balance": bal,
		})
		return false
	}
	return true
}

// billableDuration clamps a requested play length so a paid clip always has a
// bounded, chargeable length (no "whole file" free-for-all under credits).
func (s *Server) billableDuration(duration int) int {
	if s.cfg.CreditsEnabled && duration < minBillableSeconds {
		return minBillableSeconds
	}
	return duration
}

func (s *Server) submitYouTube(w http.ResponseWriter, r *http.Request, t *tenant.Tenant, viewer *store.Viewer) {
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

	duration := s.billableDuration(clampDuration(r.FormValue("duration"), minBillableSeconds))

	item := &queue.Item{
		ID:              uuid.NewString(),
		Type:            queue.TypeYouTube,
		Title:           "YouTube · " + id,
		SubmitterName:   viewer.DisplayName,
		YouTubeID:       id,
		StartSeconds:    start,
		DurationSeconds: duration,
	}
	// Deduct credits atomically BEFORE queuing; only enqueue on success.
	if !s.chargeSubmit(w, viewer, t.StreamerID, duration, item.ID) {
		return
	}
	created := t.Queue.Submit(item)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     created.ID,
		"title":  created.Title,
		"status": string(created.Status),
	})
}

func (s *Server) submitUpload(w http.ResponseWriter, r *http.Request, t *tenant.Tenant, viewer *store.Viewer) {
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

	// 0 means "whole file"; under credits we clamp up so the clip is billable.
	duration := s.billableDuration(clampDuration(r.FormValue("duration"), 0))

	item := &queue.Item{
		ID:              uuid.NewString(),
		Type:            queue.TypeUpload,
		Title:           origTitle(header.Filename),
		SubmitterName:   viewer.DisplayName,
		MediaURL:        "/media/" + storedName,
		DurationSeconds: duration,
	}
	// Deduct credits atomically BEFORE queuing; drop the saved file if they can't pay.
	if !s.chargeSubmit(w, viewer, t.StreamerID, duration, item.ID) {
		_ = os.Remove(dstPath)
		return
	}
	created := t.Queue.Submit(item)
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
