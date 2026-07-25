package twitch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileTokenStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileTokenStore(dir)

	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("empty store Get = %v, want ErrNoToken", err)
	}

	tok := &Token{
		BotLogin:     "bot",
		Channel:      "chan",
		AccessToken:  "AT",
		RefreshToken: "RT",
		ExpiresAt:    time.Now().Add(time.Hour).Round(time.Second),
	}
	if err := s.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File exists with 0600 perms.
	info, err := os.Stat(filepath.Join(dir, "twitch.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BotLogin != "bot" || got.Channel != "chan" || got.AccessToken != "AT" || got.RefreshToken != "RT" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("expiry mismatch: got %v want %v", got.ExpiresAt, tok.ExpiresAt)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after Clear Get = %v, want ErrNoToken", err)
	}
	// Clearing again is a no-op.
	if err := s.Clear(); err != nil {
		t.Fatalf("second Clear: %v", err)
	}
}

func TestControllerStateSingleUseAndExpiry(t *testing.T) {
	c := NewController("cid", "secret", "http://localhost/cb", NewFileTokenStore(t.TempDir()), nil, nil)

	st := c.NewState("mychannel")
	if st == "" {
		t.Fatal("empty state")
	}
	ch, ok := c.ConsumeState(st)
	if !ok || ch != "mychannel" {
		t.Fatalf("consume = (%q,%v), want (mychannel,true)", ch, ok)
	}
	// Single-use: second consume fails.
	if _, ok := c.ConsumeState(st); ok {
		t.Fatal("state should be single-use")
	}

	// Expired state is rejected.
	st2 := c.NewState("chan2")
	c.mu.Lock()
	e := c.states[st2]
	e.expires = time.Now().Add(-time.Minute)
	c.states[st2] = e
	c.mu.Unlock()
	if _, ok := c.ConsumeState(st2); ok {
		t.Fatal("expired state should be rejected")
	}
}

func TestControllerAvailable(t *testing.T) {
	withCreds := NewController("cid", "secret", "cb", NewFileTokenStore(t.TempDir()), nil, nil)
	if !withCreds.Available() {
		t.Error("expected available with client id+secret")
	}
	without := NewController("", "", "cb", NewFileTokenStore(t.TempDir()), nil, nil)
	if without.Available() {
		t.Error("expected unavailable without client id+secret")
	}
}
