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

	sid, err := s.CreateAuthSession("7", RoleOwner, time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st, role, err := s.GetValidAuthSession(sid)
	if err != nil || st.ID != "7" {
		t.Fatalf("valid session = %v, %v", st, err)
	}
	if role != RoleOwner {
		t.Errorf("role = %q, want %q", role, RoleOwner)
	}

	// Empty / unknown ids are not valid.
	if _, _, err := s.GetValidAuthSession(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty sid err = %v", err)
	}
	if _, _, err := s.GetValidAuthSession("bogus"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bogus sid err = %v", err)
	}

	// Logout invalidates.
	if err := s.DeleteAuthSession(sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := s.GetValidAuthSession(sid); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
}

func TestExpiredAuthSessionRejected(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("9", "zoe", "Zoe")

	sid, _ := s.CreateAuthSession("9", RoleOwner, -time.Minute) // already expired
	if _, _, err := s.GetValidAuthSession(sid); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session err = %v, want ErrNotFound", err)
	}
}

func TestModeratorSessionRoleRoundTrips(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("owner1", "owen", "Owen")

	// A moderator session is bound to the OWNER's id, with role=moderator.
	sid, err := s.CreateAuthSession("owner1", RoleModerator, time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st, role, err := s.GetValidAuthSession(sid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if st.ID != "owner1" {
		t.Errorf("streamer = %q, want owner1 (the tenant owner)", st.ID)
	}
	if role != RoleModerator {
		t.Errorf("role = %q, want %q", role, RoleModerator)
	}
}

func TestModeratorLinkLifecycle(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("owner1", "owen", "Owen")

	// No link yet.
	if _, err := s.ModeratorLink("owner1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("initial link err = %v, want ErrNotFound", err)
	}

	tok, err := s.RegenerateModeratorLink("owner1")
	if err != nil || tok == "" {
		t.Fatalf("regenerate: %q, %v", tok, err)
	}
	if got, err := s.ModeratorLink("owner1"); err != nil || got != tok {
		t.Fatalf("ModeratorLink = %q, %v; want %q", got, err, tok)
	}
	if owner, err := s.ResolveModeratorLink(tok); err != nil || owner != "owner1" {
		t.Fatalf("ResolveModeratorLink = %q, %v; want owner1", owner, err)
	}

	// Regenerate replaces the old token (one active link per streamer).
	tok2, err := s.RegenerateModeratorLink("owner1")
	if err != nil {
		t.Fatalf("regenerate 2: %v", err)
	}
	if tok2 == tok {
		t.Error("regenerate should mint a different token")
	}
	if _, err := s.ResolveModeratorLink(tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token still resolves after regenerate: %v", err)
	}
	if owner, err := s.ResolveModeratorLink(tok2); err != nil || owner != "owner1" {
		t.Fatalf("new token resolve = %q, %v", owner, err)
	}
}

func TestViewerSessionLifecycle(t *testing.T) {
	s := newTestStore(t)

	v, err := s.UpsertViewer("v1", "vicky", "Vicky")
	if err != nil || v.ID != "v1" {
		t.Fatalf("upsert viewer: %v, %v", v, err)
	}

	vsid, err := s.CreateViewerSession("v1", time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := s.GetValidViewerSession(vsid)
	if err != nil || got.ID != "v1" {
		t.Fatalf("valid session = %v, %v", got, err)
	}

	if _, err := s.GetValidViewerSession(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty vsid err = %v", err)
	}
	// Expired session is rejected.
	expired, _ := s.CreateViewerSession("v1", -time.Minute)
	if _, err := s.GetValidViewerSession(expired); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired vsid err = %v, want ErrNotFound", err)
	}

	if err := s.DeleteViewerSession(vsid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetValidViewerSession(vsid); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
}

func TestCreditBitsCreditsOnceAndDedups(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("A", "streamera", "StreamerA")

	// A cheer for a viewer who has never logged in still credits (viewer upserted).
	credited, err := s.CreditBits("msg-1", "v1", "vicky", "Vicky", "A", 100)
	if err != nil || !credited {
		t.Fatalf("first credit = %v, %v; want true, nil", credited, err)
	}
	if bal, _ := s.Balance("v1", "A"); bal != 100 {
		t.Fatalf("balance = %d, want 100", bal)
	}
	if v, err := s.GetViewer("v1"); err != nil || v.DisplayName != "Vicky" {
		t.Fatalf("viewer not upserted from event: %v, %v", v, err)
	}

	// Replaying the same message id must not credit again.
	credited, err = s.CreditBits("msg-1", "v1", "vicky", "Vicky", "A", 100)
	if err != nil || credited {
		t.Fatalf("replay credit = %v, %v; want false, nil", credited, err)
	}
	if bal, _ := s.Balance("v1", "A"); bal != 100 {
		t.Fatalf("balance after replay = %d, want 100", bal)
	}

	// A distinct message id stacks onto the balance.
	if _, err := s.CreditBits("msg-2", "v1", "vicky", "Vicky", "A", 50); err != nil {
		t.Fatalf("second credit: %v", err)
	}
	if bal, _ := s.Balance("v1", "A"); bal != 150 {
		t.Fatalf("balance = %d, want 150", bal)
	}
}

func TestCreditBalancesArePerChannel(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("A", "streamera", "StreamerA")
	s.UpsertStreamer("B", "streamerb", "StreamerB")

	if _, err := s.CreditBits("m1", "v1", "vicky", "Vicky", "A", 200); err != nil {
		t.Fatalf("credit: %v", err)
	}
	if bal, _ := s.Balance("v1", "A"); bal != 200 {
		t.Errorf("A balance = %d, want 200", bal)
	}
	if bal, _ := s.Balance("v1", "B"); bal != 0 {
		t.Errorf("B balance = %d, want 0 (credits are per-channel)", bal)
	}
}

func TestSpendCreditsAtomicAndNeverNegative(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("A", "streamera", "StreamerA")
	s.CreditBits("m1", "v1", "vicky", "Vicky", "A", 100)

	// Spend within budget.
	bal, ok, err := s.SpendCredits("v1", "A", 60, "item-1")
	if err != nil || !ok || bal != 40 {
		t.Fatalf("spend 60 = bal %d, ok %v, err %v; want 40, true, nil", bal, ok, err)
	}

	// Overspend is rejected; balance untouched, current balance reported back.
	bal, ok, err = s.SpendCredits("v1", "A", 100, "item-2")
	if err != nil || ok || bal != 40 {
		t.Fatalf("overspend = bal %d, ok %v, err %v; want 40, false, nil", bal, ok, err)
	}

	// Spending in a channel with no balance row fails cleanly (reports 0).
	bal, ok, _ = s.SpendCredits("v1", "B", 10, "item-3")
	if ok || bal != 0 {
		t.Fatalf("spend with no row = bal %d, ok %v; want 0, false", bal, ok)
	}
}

func TestGrantCredits(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("A", "streamera", "StreamerA")
	s.UpsertViewer("v1", "vicky", "Vicky")

	if err := s.GrantCredits("v1", "A", 500); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if bal, _ := s.Balance("v1", "A"); bal != 500 {
		t.Fatalf("balance = %d, want 500", bal)
	}
}

func TestRevokeModeratorAccessKicksModsKeepsOwner(t *testing.T) {
	s := newTestStore(t)
	s.UpsertStreamer("owner1", "owen", "Owen")

	tok, _ := s.RegenerateModeratorLink("owner1")
	ownerSid, _ := s.CreateAuthSession("owner1", RoleOwner, time.Hour)
	modSid, _ := s.CreateAuthSession("owner1", RoleModerator, time.Hour)

	if err := s.RevokeModeratorAccess("owner1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Link gone.
	if _, err := s.ResolveModeratorLink(tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("link still resolves after revoke: %v", err)
	}
	// Moderator session kicked.
	if _, _, err := s.GetValidAuthSession(modSid); !errors.Is(err, ErrNotFound) {
		t.Errorf("mod session survived revoke: %v", err)
	}
	// Owner session untouched.
	if _, role, err := s.GetValidAuthSession(ownerSid); err != nil || role != RoleOwner {
		t.Errorf("owner session = role %q, err %v; want owner, nil", role, err)
	}
}
