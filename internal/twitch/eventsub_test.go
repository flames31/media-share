package twitch

import (
	"net/http"
	"testing"
	"time"
)

func headers(id, ts, sig string) http.Header {
	h := http.Header{}
	h.Set(HeaderMessageID, id)
	h.Set(HeaderMessageTimestamp, ts)
	h.Set(HeaderMessageSignature, sig)
	return h
}

func TestVerifyEventSubSignature(t *testing.T) {
	secret := "s3cr3t-shared-secret"
	id := "abc-123"
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	body := []byte(`{"subscription":{"type":"channel.cheer"},"event":{"bits":100}}`)

	sig := EventSubSignature(secret, id, ts, body)

	if !VerifyEventSubSignature(headers(id, ts, sig), body, secret) {
		t.Fatal("valid signature rejected")
	}

	// Tampered body must fail.
	if VerifyEventSubSignature(headers(id, ts, sig), []byte(`{"event":{"bits":100000}}`), secret) {
		t.Error("tampered body accepted")
	}
	// Wrong secret must fail.
	if VerifyEventSubSignature(headers(id, ts, sig), body, "wrong-secret") {
		t.Error("wrong secret accepted")
	}
	// Empty secret never verifies.
	if VerifyEventSubSignature(headers(id, ts, sig), body, "") {
		t.Error("empty secret accepted")
	}
	// Missing headers fail.
	if VerifyEventSubSignature(headers("", ts, sig), body, secret) {
		t.Error("missing message id accepted")
	}
}

func TestEventSubTimestampFresh(t *testing.T) {
	maxAge := 10 * time.Minute
	fresh := http.Header{}
	fresh.Set(HeaderMessageTimestamp, time.Now().UTC().Format(time.RFC3339Nano))
	if !EventSubTimestampFresh(fresh, maxAge) {
		t.Error("current timestamp treated as stale")
	}

	old := http.Header{}
	old.Set(HeaderMessageTimestamp, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if EventSubTimestampFresh(old, maxAge) {
		t.Error("hour-old timestamp treated as fresh")
	}

	bad := http.Header{}
	bad.Set(HeaderMessageTimestamp, "not-a-time")
	if EventSubTimestampFresh(bad, maxAge) {
		t.Error("malformed timestamp treated as fresh")
	}
}
