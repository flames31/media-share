package queue

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ParseYouTube extracts the 11-character video ID and an optional start time (in
// seconds) from a YouTube URL or a raw video ID. It understands the common
// forms: watch?v=, youtu.be/, /embed/, /shorts/, and the t=/start= parameters
// (which may be like "1h2m3s", "90s", or "90").
func ParseYouTube(raw string) (id string, startSeconds int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false
	}

	// Bare video ID.
	if youtubeIDRe.MatchString(raw) {
		return raw, 0, true
	}

	// Ensure the URL has a scheme so url.Parse populates Host.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, false
	}

	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	q := u.Query()

	switch {
	case host == "youtu.be":
		id = strings.Trim(u.Path, "/")
	case strings.HasSuffix(host, "youtube.com") || strings.HasSuffix(host, "youtube-nocookie.com"):
		if v := q.Get("v"); v != "" {
			id = v
		} else {
			// /embed/<id>, /shorts/<id>, /v/<id>, /live/<id>
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 {
				switch parts[0] {
				case "embed", "shorts", "v", "live":
					id = parts[1]
				}
			}
		}
	default:
		return "", 0, false
	}

	if !youtubeIDRe.MatchString(id) {
		return "", 0, false
	}

	start := q.Get("t")
	if start == "" {
		start = q.Get("start")
	}
	if start == "" {
		if u.Fragment != "" && strings.HasPrefix(u.Fragment, "t=") {
			start = strings.TrimPrefix(u.Fragment, "t=")
		}
	}
	return id, parseTimeToSeconds(start), true
}

// parseTimeToSeconds converts "1h2m3s", "90s", or "90" into seconds. Returns 0
// for empty or unparseable input.
func parseTimeToSeconds(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	// Plain integer seconds.
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			return 0
		}
		return n
	}
	// Colon form m:ss or h:mm:ss.
	if strings.Contains(s, ":") {
		return parseColonTime(s)
	}
	// h/m/s suffix form.
	var total, cur int
	matched := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			cur = cur*10 + int(r-'0')
		case r == 'h':
			total += cur * 3600
			cur = 0
			matched = true
		case r == 'm':
			total += cur * 60
			cur = 0
			matched = true
		case r == 's':
			total += cur
			cur = 0
			matched = true
		default:
			return 0
		}
	}
	if !matched {
		return 0
	}
	total += cur // trailing digits without a suffix count as seconds
	return total
}

func parseColonTime(s string) int {
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0
		}
		total = total*60 + n
	}
	return total
}
