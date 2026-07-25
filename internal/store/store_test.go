package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertStreamerIdempotentKeepsPlayerKey(t *testing.T) {
	s := newTestStore(t)

	a, err := s.UpsertStreamer("123", "coolstreamer", "CoolStreamer")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if a.PlayerKey == "" {
		t.Fatal("player key should be minted")
	}

	// Second login: display/login can change, player key + created_at stay.
	b, err := s.UpsertStreamer("123", "coolstreamer2", "Cooler")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if b.PlayerKey != a.PlayerKey {
		t.Errorf("player key changed: %q -> %q", a.PlayerKey, b.PlayerKey)
	}
	if b.Login != "coolstreamer2" || b.DisplayName != "Cooler" {
		t.Errorf("login/display not updated: %+v", b)
	}
	if !b.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("created_at should be stable")
	}
}

func TestGetStreamerAndByPlayerKey(t *testing.T) {
	s := newTestStore(t)
	st, _ := s.UpsertStreamer("42", "bob", "Bob")

	got, err := s.GetStreamer("42")
	if err != nil || got.ID != "42" {
		t.Fatalf("GetStreamer = %v, %v", got, err)
	}
	byKey, err := s.GetStreamerByPlayerKey(st.PlayerKey)
	if err != nil || byKey.ID != "42" {
		t.Fatalf("GetStreamerByPlayerKey = %v, %v", byKey, err)
	}

	if _, err := s.GetStreamer("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing streamer err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetStreamerByPlayerKey("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}

func TestAuthSessionLifecycle(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("7", "amy", "Amy")

	sid, err := s.CreateAuthSession("7", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st, err := s.GetValidAuthSession(sid)
	if err != nil || st.ID != "7" {
		t.Fatalf("valid session = %v, %v", st, err)
	}

	// Empty / unknown ids are not valid.
	if _, err := s.GetValidAuthSession(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty sid err = %v", err)
	}
	if _, err := s.GetValidAuthSession("bogus"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bogus sid err = %v", err)
	}

	// Logout invalidates.
	if err := s.DeleteAuthSession(sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetValidAuthSession(sid); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
}

func TestExpiredAuthSessionRejected(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("9", "zoe", "Zoe")

	sid, _ := s.CreateAuthSession("9", -time.Minute) // already expired
	if _, err := s.GetValidAuthSession(sid); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session err = %v, want ErrNotFound", err)
	}
}
