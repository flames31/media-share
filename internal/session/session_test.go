package session

import "testing"

func TestSessionLifecycle(t *testing.T) {
	m := New(nil)

	// Closed by default.
	if m.Status().Active {
		t.Fatal("new session should be inactive")
	}
	if m.Valid("anything") {
		t.Fatal("no token should validate while closed")
	}

	// Start mints a token.
	tok := m.Start()
	if tok == "" || !m.Status().Active {
		t.Fatal("Start should activate and return a token")
	}
	if !m.Valid(tok) {
		t.Fatal("the minted token should validate")
	}
	if m.Valid("wrong") {
		t.Fatal("a wrong token must not validate")
	}
	if m.Valid("") {
		t.Fatal("empty token must not validate")
	}

	// Regenerate invalidates the old token.
	tok2 := m.Regenerate()
	if tok2 == tok {
		t.Fatal("Regenerate should produce a different token")
	}
	if m.Valid(tok) {
		t.Fatal("old token must stop working after regenerate")
	}
	if !m.Valid(tok2) {
		t.Fatal("new token should validate")
	}

	// Stop invalidates everything.
	m.Stop()
	if m.Status().Active {
		t.Fatal("Stop should deactivate")
	}
	if m.Valid(tok2) {
		t.Fatal("token must not validate after stop")
	}
}

func TestRegenerateFromClosedActivates(t *testing.T) {
	m := New(nil)
	tok := m.Regenerate()
	if !m.Status().Active || !m.Valid(tok) {
		t.Fatal("Regenerate from closed should activate a valid session")
	}
}
