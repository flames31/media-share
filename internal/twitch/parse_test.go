package twitch

import "testing"

func TestParsePrivmsg(t *testing.T) {
	cases := []struct {
		name          string
		line          string
		wantOK        bool
		wantUser      string
		wantText      string
		wantMod       bool
		wantBroadcast bool
	}{
		{
			name:          "broadcaster command",
			line:          "@badges=broadcaster/1;mod=0 :bob!bob@bob.tmi.twitch.tv PRIVMSG #chan :!skip",
			wantOK:        true,
			wantUser:      "bob",
			wantText:      "!skip",
			wantBroadcast: true,
		},
		{
			name:     "mod via mod tag",
			line:     "@mod=1;badges=moderator/1 :mo!mo@mo.tmi.twitch.tv PRIVMSG #chan :!pause",
			wantOK:   true,
			wantUser: "mo",
			wantText: "!pause",
			wantMod:  true,
		},
		{
			name:     "plain viewer",
			line:     "@mod=0;badges= :v!v@v.tmi.twitch.tv PRIVMSG #chan :hello world",
			wantOK:   true,
			wantUser: "v",
			wantText: "hello world",
		},
		{
			name:   "not a privmsg",
			line:   ":tmi.twitch.tv 001 bot :Welcome",
			wantOK: false,
		},
		{
			name:   "ping",
			line:   "PING :tmi.twitch.tv",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, ok := parsePrivmsg(c.line)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if m.User != c.wantUser {
				t.Errorf("user = %q, want %q", m.User, c.wantUser)
			}
			if m.Text != c.wantText {
				t.Errorf("text = %q, want %q", m.Text, c.wantText)
			}
			if m.IsMod != c.wantMod {
				t.Errorf("mod = %v, want %v", m.IsMod, c.wantMod)
			}
			if m.IsBroadcaster != c.wantBroadcast {
				t.Errorf("broadcaster = %v, want %v", m.IsBroadcaster, c.wantBroadcast)
			}
		})
	}
}
