package queue

import "testing"

func newItem(title string) *Item {
	return &Item{Type: TypeYouTube, Title: title, YouTubeID: "abcdefghijk", DurationSeconds: 10}
}

func TestSubmitPendingThenApproveStartsPlaying(t *testing.T) {
	m := NewManager(nil)
	it := m.Submit(newItem("a"))

	s := m.Snapshot()
	if len(s.Pending) != 1 || s.NowPlaying != nil {
		t.Fatalf("expected 1 pending and nothing playing, got pending=%d nowPlaying=%v", len(s.Pending), s.NowPlaying)
	}

	if !m.Approve(it.ID) {
		t.Fatal("approve returned false")
	}
	s = m.Snapshot()
	if s.NowPlaying == nil || s.NowPlaying.ID != it.ID {
		t.Fatalf("expected approved item to start playing, got %v", s.NowPlaying)
	}
	if len(s.Pending) != 0 || len(s.Queue) != 0 {
		t.Fatalf("expected empty pending and queue, got pending=%d queue=%d", len(s.Pending), len(s.Queue))
	}
}

func TestBypassSkipsPendingAndAutoPlays(t *testing.T) {
	m := NewManager(nil)
	m.SetBypass(true)
	it := m.Submit(newItem("a"))

	s := m.Snapshot()
	if len(s.Pending) != 0 {
		t.Fatalf("bypass should skip pending, got %d", len(s.Pending))
	}
	if s.NowPlaying == nil || s.NowPlaying.ID != it.ID {
		t.Fatalf("bypassed item should auto-play, got %v", s.NowPlaying)
	}
}

func TestQueueOrderingAndAdvance(t *testing.T) {
	m := NewManager(nil)
	m.SetBypass(true)
	a := m.Submit(newItem("a"))
	b := m.Submit(newItem("b"))
	c := m.Submit(newItem("c"))

	if np := m.NowPlaying(); np == nil || np.ID != a.ID {
		t.Fatalf("expected a playing, got %v", np)
	}

	m.Ended(a.ID)
	if np := m.NowPlaying(); np == nil || np.ID != b.ID {
		t.Fatalf("expected b playing after a ended, got %v", np)
	}

	m.Skip() // skip b -> c
	if np := m.NowPlaying(); np == nil || np.ID != c.ID {
		t.Fatalf("expected c playing after skip, got %v", np)
	}

	m.Ended(c.ID)
	if np := m.NowPlaying(); np != nil {
		t.Fatalf("expected idle after last item, got %v", np)
	}
}

func TestEndedIgnoresStaleID(t *testing.T) {
	m := NewManager(nil)
	m.SetBypass(true)
	a := m.Submit(newItem("a"))
	b := m.Submit(newItem("b"))
	_ = b

	// Report the end of a video that is not the one now playing after we already advanced.
	m.Ended(a.ID)
	np := m.NowPlaying()
	if np == nil || np.ID != b.ID {
		t.Fatalf("expected b playing, got %v", np)
	}
	// Stale report for a should not advance past b.
	m.Ended(a.ID)
	if np := m.NowPlaying(); np == nil || np.ID != b.ID {
		t.Fatalf("stale ended should be ignored, got %v", np)
	}
}

func TestRejectAndRemove(t *testing.T) {
	m := NewManager(nil)
	p := m.Submit(newItem("pending"))
	if !m.Reject(p.ID) {
		t.Fatal("reject failed")
	}
	if len(m.Snapshot().Pending) != 0 {
		t.Fatal("pending not removed after reject")
	}

	m.SetBypass(true)
	a := m.Submit(newItem("a")) // plays immediately
	b := m.Submit(newItem("b")) // queued
	if !m.Remove(b.ID) {
		t.Fatal("remove failed")
	}
	if len(m.Snapshot().Queue) != 0 {
		t.Fatal("queue item not removed")
	}
	if np := m.NowPlaying(); np == nil || np.ID != a.ID {
		t.Fatalf("removing queued item should not affect now playing, got %v", np)
	}
}

func TestClearQueueVsAll(t *testing.T) {
	m := NewManager(nil)
	m.SetBypass(true)
	m.Submit(newItem("a")) // playing
	m.Submit(newItem("b")) // queued
	m.SetBypass(false)
	m.Submit(newItem("c")) // pending

	m.Clear(false)
	s := m.Snapshot()
	if len(s.Queue) != 0 {
		t.Fatalf("clear(queue) should empty approved queue, got %d", len(s.Queue))
	}
	if s.NowPlaying == nil {
		t.Fatal("clear(queue) should not stop now playing")
	}
	if len(s.Pending) != 1 {
		t.Fatalf("clear(queue) should keep pending, got %d", len(s.Pending))
	}

	m.Clear(true)
	s = m.Snapshot()
	if s.NowPlaying != nil || len(s.Pending) != 0 || len(s.Queue) != 0 {
		t.Fatalf("clear(all) should empty everything, got %+v", s)
	}
}

func TestPauseResume(t *testing.T) {
	m := NewManager(nil)
	m.Pause()
	if !m.Snapshot().Paused {
		t.Fatal("expected paused")
	}
	m.Resume()
	if m.Snapshot().Paused {
		t.Fatal("expected resumed")
	}
}

func TestParseYouTube(t *testing.T) {
	cases := []struct {
		in    string
		id    string
		start int
		ok    bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", 0, true},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=30s", "dQw4w9WgXcQ", 30, true},
		{"https://youtu.be/dQw4w9WgXcQ?t=1m30s", "dQw4w9WgXcQ", 90, true},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ", 0, true},
		{"https://youtube.com/embed/dQw4w9WgXcQ?start=45", "dQw4w9WgXcQ", 45, true},
		{"youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ", 0, true},
		{"dQw4w9WgXcQ", "dQw4w9WgXcQ", 0, true},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=90", "dQw4w9WgXcQ", 90, true},
		{"https://example.com/watch?v=dQw4w9WgXcQ", "", 0, false},
		{"not a url", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		id, start, ok := ParseYouTube(c.in)
		if ok != c.ok || id != c.id || start != c.start {
			t.Errorf("ParseYouTube(%q) = (%q,%d,%v), want (%q,%d,%v)", c.in, id, start, ok, c.id, c.start, c.ok)
		}
	}
}
