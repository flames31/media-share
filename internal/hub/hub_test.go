package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialRoom connects a websocket client into the given room/role.
func dialRoom(t *testing.T, srvURL string, room string, role Role) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/?room=" + room + "&role=" + string(role)
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func readType(t *testing.T, c *websocket.Conn) Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestRoomIsolation(t *testing.T) {
	h := New(nil)
	// Server resolves room/role from the query for the test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		room := r.URL.Query().Get("room")
		role := Role(r.URL.Query().Get("role"))
		h.ServeWS(w, r, room, role)
	}))
	defer srv.Close()

	a := dialRoom(t, srv.URL, "roomA", RoleAdmin)
	defer a.Close()
	b := dialRoom(t, srv.URL, "roomB", RoleAdmin)
	defer b.Close()

	// Give registrations a moment to complete.
	time.Sleep(100 * time.Millisecond)

	h.BroadcastTo("roomA", "state", map[string]string{"hello": "A"})

	// A receives it.
	m := readType(t, a)
	if m.Type != "state" {
		t.Fatalf("A got %q, want state", m.Type)
	}

	// B must NOT receive it.
	b.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := b.ReadMessage(); err == nil {
		t.Fatal("B received a message meant for room A")
	}
}

func TestOnConnectInitialMessages(t *testing.T) {
	h := New(nil)
	h.OnConnect = func(room, role string) []Message {
		return []Message{{Type: "state", Payload: room}, {Type: "session", Payload: role}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeWS(w, r, r.URL.Query().Get("room"), Role(r.URL.Query().Get("role")))
	}))
	defer srv.Close()

	c := dialRoom(t, srv.URL, "room1", RoleAdmin)
	defer c.Close()

	if m := readType(t, c); m.Type != "state" || m.Payload != "room1" {
		t.Fatalf("first msg = %+v", m)
	}
	if m := readType(t, c); m.Type != "session" || m.Payload != "admin" {
		t.Fatalf("second msg = %+v", m)
	}
}
