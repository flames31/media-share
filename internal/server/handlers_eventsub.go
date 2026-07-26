package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"media-share/internal/twitch"
)

// eventSubMaxBody caps the webhook body; EventSub notifications are small.
const eventSubMaxBody = 1 << 20

// eventSubReplayWindow rejects validly-signed messages older than this, limiting
// replay of captured notifications.
const eventSubReplayWindow = 10 * time.Minute

// eventSubEnvelope is the outer shape of every EventSub webhook request.
type eventSubEnvelope struct {
	Challenge    string `json:"challenge"`
	Subscription struct {
		Type string `json:"type"`
	} `json:"subscription"`
	Event json.RawMessage `json:"event"`
}

// cheerEvent covers the fields we need from channel.cheer and channel.bits.use.
type cheerEvent struct {
	IsAnonymous       bool   `json:"is_anonymous"`
	UserID            string `json:"user_id"`
	UserLogin         string `json:"user_login"`
	UserName          string `json:"user_name"`
	BroadcasterUserID string `json:"broadcaster_user_id"`
	Bits              int64  `json:"bits"`
}

// handleEventSub receives Twitch EventSub webhook notifications. It verifies the
// HMAC signature over the raw body, guards against replay, handles the
// subscription verification handshake, and credits cheered bits to the cheering
// viewer's per-channel balance. Subscriptions are created out-of-band (Twitch CLI
// / dashboard) with the same TWITCH_EVENTSUB_SECRET.
func (s *Server) handleEventSub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, eventSubMaxBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Authenticate the message BEFORE trusting anything in it.
	if !twitch.VerifyEventSubSignature(r.Header, body, s.cfg.TwitchEventSubSecret) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	if !twitch.EventSubTimestampFresh(r.Header, eventSubReplayWindow) {
		http.Error(w, "stale message", http.StatusForbidden)
		return
	}

	var env eventSubEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	switch r.Header.Get(twitch.HeaderMessageType) {
	case twitch.MessageTypeVerification:
		// Activate the subscription by echoing the challenge verbatim as plain text.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, env.Challenge)
		return

	case twitch.MessageTypeRevocation:
		log.Printf("eventsub: subscription %q revoked", env.Subscription.Type)
		w.WriteHeader(http.StatusOK)
		return

	case twitch.MessageTypeNotification:
		s.handleCheerNotification(r.Header.Get(twitch.HeaderMessageID), env.Event)
		// Always 200 a handled notification so Twitch does not retry it.
		w.WriteHeader(http.StatusOK)
		return

	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCheerNotification credits a verified cheer. Dedup by message id inside the
// store makes this idempotent, so a Twitch retry can never double-credit.
func (s *Server) handleCheerNotification(msgID string, raw json.RawMessage) {
	var e cheerEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		log.Printf("eventsub: bad event payload: %v", err)
		return
	}
	// Anonymous cheers carry no user id, so there is no balance to credit.
	if e.IsAnonymous || e.UserID == "" || e.BroadcasterUserID == "" || e.Bits <= 0 {
		return
	}
	credited, err := s.store.CreditBits(msgID, e.UserID, e.UserLogin, e.UserName, e.BroadcasterUserID, e.Bits)
	if err != nil {
		log.Printf("eventsub: credit failed (msg %s): %v", msgID, err)
		return
	}
	if credited {
		log.Printf("eventsub: credited %d to viewer %s in channel %s", e.Bits, e.UserID, e.BroadcasterUserID)
	}
}
