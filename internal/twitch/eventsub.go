package twitch

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
)

// EventSub webhook header names (canonical form).
const (
	HeaderMessageID        = "Twitch-Eventsub-Message-Id"
	HeaderMessageTimestamp = "Twitch-Eventsub-Message-Timestamp"
	HeaderMessageSignature = "Twitch-Eventsub-Message-Signature"
	HeaderMessageType      = "Twitch-Eventsub-Message-Type"
	HeaderSubscriptionType = "Twitch-Eventsub-Subscription-Type"
)

// EventSub message types (the Twitch-Eventsub-Message-Type header values).
const (
	MessageTypeVerification = "webhook_callback_verification"
	MessageTypeNotification = "notification"
	MessageTypeRevocation   = "revocation"
)

// EventSubSignature builds the value Twitch puts in the signature header for a
// webhook message: "sha256=" + HMAC_SHA256(secret, messageID+timestamp+body).
func EventSubSignature(secret, messageID, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(messageID))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyEventSubSignature reports whether the request's signature header matches
// an HMAC computed from the message id, timestamp, and raw body using secret. The
// comparison is constant-time. It returns false when the secret is empty or any
// required header is missing.
func VerifyEventSubSignature(h http.Header, body []byte, secret string) bool {
	if secret == "" {
		return false
	}
	id := h.Get(HeaderMessageID)
	ts := h.Get(HeaderMessageTimestamp)
	got := h.Get(HeaderMessageSignature)
	if id == "" || ts == "" || got == "" {
		return false
	}
	want := EventSubSignature(secret, id, ts, body)
	return hmac.Equal([]byte(got), []byte(want))
}

// EventSubTimestampFresh reports whether the message timestamp is within maxAge of
// now, a defence-in-depth guard against replay of old (but validly signed)
// messages. A malformed timestamp is treated as not fresh.
func EventSubTimestampFresh(h http.Header, maxAge time.Duration) bool {
	ts, err := time.Parse(time.RFC3339Nano, h.Get(HeaderMessageTimestamp))
	if err != nil {
		return false
	}
	age := time.Since(ts)
	return age >= -maxAge && age <= maxAge
}
