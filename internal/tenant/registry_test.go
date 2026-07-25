package tenant

import (
	"testing"

	"media-share/internal/hub"
	"media-share/internal/queue"
)

func newItem() *queue.Item {
	return &queue.Item{Type: queue.TypeYouTube, Title: "t", YouTubeID: "abcdefghijk", DurationSeconds: 5}
}

func TestTenantsAreIsolated(t *testing.T) {
	r := NewRegistry(hub.New(nil))
	a := r.Get("streamerA")
	b := r.Get("streamerB")

	if a == b {
		t.Fatal("distinct streamers must get distinct tenants")
	}

	// Bypass verification so submissions land straight in the queue.
	a.Queue.SetBypass(true)
	a.Queue.Submit(newItem())

	if np := a.Queue.NowPlaying(); np == nil {
		t.Fatal("A should have a now-playing item")
	}
	if np := b.Queue.NowPlaying(); np != nil {
		t.Fatal("B's queue must be unaffected by A's submission")
	}

	// Same id returns the same tenant instance.
	if r.Get("streamerA") != a {
		t.Fatal("Get should return the cached tenant")
	}
}

func TestResolveSessionMapsToOwningTenant(t *testing.T) {
	r := NewRegistry(hub.New(nil))
	a := r.Get("A")
	b := r.Get("B")

	tokA := a.StartSession()
	tokB := b.StartSession()

	if got, ok := r.ResolveSession(tokA); !ok || got != a {
		t.Fatalf("token A resolved to %v (ok=%v), want tenant A", got, ok)
	}
	if got, ok := r.ResolveSession(tokB); !ok || got != b {
		t.Fatalf("token B resolved to %v (ok=%v), want tenant B", got, ok)
	}

	// Regenerate invalidates the old token and installs the new one.
	tokA2 := a.RegenerateSession()
	if _, ok := r.ResolveSession(tokA); ok {
		t.Fatal("old token A should no longer resolve")
	}
	if got, ok := r.ResolveSession(tokA2); !ok || got != a {
		t.Fatalf("new token A resolved to %v (ok=%v)", got, ok)
	}

	// Stop drops the token entirely.
	a.StopSession()
	if _, ok := r.ResolveSession(tokA2); ok {
		t.Fatal("token should not resolve after stop")
	}

	// Unknown/empty tokens never resolve.
	if _, ok := r.ResolveSession("nope"); ok {
		t.Fatal("unknown token resolved")
	}
	if _, ok := r.ResolveSession(""); ok {
		t.Fatal("empty token resolved")
	}
}

func TestInitialMessages(t *testing.T) {
	r := NewRegistry(hub.New(nil))
	player := r.InitialMessages("A", string(hub.RolePlayer))
	if len(player) != 1 || player[0].Type != "state" {
		t.Fatalf("player initial = %+v", player)
	}
	admin := r.InitialMessages("A", string(hub.RoleAdmin))
	if len(admin) != 2 || admin[1].Type != "session" {
		t.Fatalf("admin initial = %+v", admin)
	}
}
