package twitch

import "strings"

// privmsg is a parsed chat message with the bits we care about.
type privmsg struct {
	User          string
	Text          string
	IsMod         bool
	IsBroadcaster bool
}

// parsePrivmsg parses a single IRC line into a privmsg. It returns ok=false for
// any line that is not a PRIVMSG.
//
// A tagged line looks like:
//
//	@badges=broadcaster/1;mod=0;... :nick!nick@nick.tmi.twitch.tv PRIVMSG #chan :hello
func parsePrivmsg(line string) (privmsg, bool) {
	var m privmsg

	var tags string
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return m, false
		}
		tags = line[1:sp]
		line = line[sp+1:]
	}

	// Expect ":prefix COMMAND params"
	if !strings.HasPrefix(line, ":") {
		return m, false
	}
	line = line[1:]
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return m, false
	}
	prefix := line[:sp]
	rest := line[sp+1:]

	if !strings.HasPrefix(rest, "PRIVMSG ") {
		return m, false
	}
	rest = strings.TrimPrefix(rest, "PRIVMSG ")

	// rest is "#channel :message"
	if i := strings.Index(rest, " :"); i >= 0 {
		m.Text = rest[i+2:]
	} else {
		return m, false
	}

	// User from prefix "nick!user@host"
	if bang := strings.IndexByte(prefix, '!'); bang >= 0 {
		m.User = prefix[:bang]
	} else {
		m.User = prefix
	}

	parseTags(tags, &m)
	return m, true
}

func parseTags(tags string, m *privmsg) {
	if tags == "" {
		return
	}
	for _, kv := range strings.Split(tags, ";") {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		switch key {
		case "mod":
			if val == "1" {
				m.IsMod = true
			}
		case "badges":
			if strings.Contains(val, "broadcaster/") {
				m.IsBroadcaster = true
			}
			if strings.Contains(val, "moderator/") {
				m.IsMod = true
			}
		case "user-type":
			if val == "mod" {
				m.IsMod = true
			}
		}
	}
}
