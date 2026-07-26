// Package twitch implements a minimal Twitch chat (IRC-over-WebSocket) bot that
// lets the broadcaster and moderators control the media queue with commands.
package twitch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"media-share/internal/queue"
)

const ircURL = "wss://irc-ws.chat.twitch.tv:443"

// Actions is the subset of the queue Manager the bot drives. It is satisfied by
// *queue.Manager.
type Actions interface {
	Skip()
	Pause()
	Resume()
	Clear(all bool)
	NowPlaying() *queue.Item
}

// Creds are the credentials the bot needs to authenticate a chat connection.
type Creds struct {
	AccessToken string // without the "oauth:" prefix
	Login       string // bot account login (lowercase)
	Channel     string // channel to join (lowercase, no '#')
}

// CredsFunc returns fresh credentials for a connection. It is called before each
// (re)connect so the controller can refresh an expired token transparently.
type CredsFunc func(ctx context.Context) (Creds, error)

// Bot connects to Twitch chat and dispatches mod-gated commands. Credentials are
// obtained lazily via creds so token refresh is handled outside the IRC logic.
type Bot struct {
	creds   CredsFunc
	actions Actions

	channel string // set at the start of each session; used by reply()
}

// NewBot creates a Bot that authenticates using credentials from creds.
func NewBot(creds CredsFunc, actions Actions) *Bot {
	return &Bot{creds: creds, actions: actions}
}

func ensureOAuthPrefix(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return t
	}
	if !strings.HasPrefix(t, "oauth:") {
		return "oauth:" + t
	}
	return t
}

// Run connects and processes messages until ctx is cancelled, reconnecting with
// a backoff on any failure. It blocks; run it in its own goroutine.
func (b *Bot) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := b.session(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("twitch: session ended, reconnecting", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (b *Bot) session(ctx context.Context) error {
	creds, err := b.creds(ctx)
	if err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	b.channel = strings.ToLower(strings.TrimPrefix(creds.Channel, "#"))
	username := strings.ToLower(creds.Login)
	pass := ensureOAuthPrefix(creds.AccessToken)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, ircURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Close the connection when the context is cancelled so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	send := func(format string, args ...any) error {
		return conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil, format, args...))
	}

	// Request tags so we can read mod/broadcaster badges; then authenticate and join.
	if err := send("CAP REQ :twitch.tv/tags twitch.tv/commands"); err != nil {
		return err
	}
	if err := send("PASS %s", pass); err != nil {
		return err
	}
	if err := send("NICK %s", username); err != nil {
		return err
	}
	if err := send("JOIN #%s", b.channel); err != nil {
		return err
	}
	slog.Info("twitch: connected", "bot", username, "channel", b.channel)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		for line := range strings.SplitSeq(string(data), "\r\n") {
			if line == "" {
				continue
			}
			// Twitch reports a bad/expired token via a NOTICE. Surface it as an
			// error so the reconnect loop refreshes credentials on the next try.
			if strings.Contains(line, "Login authentication failed") || strings.Contains(line, "Login unsuccessful") {
				return errAuthFailed
			}
			b.handleLine(conn, line)
		}
	}
}

// errAuthFailed signals the token was rejected by Twitch.
var errAuthFailed = fmt.Errorf("twitch login authentication failed")

func (b *Bot) handleLine(conn *websocket.Conn, line string) {
	// Respond to server pings to stay connected.
	if strings.HasPrefix(line, "PING") {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("PONG :tmi.twitch.tv"))
		return
	}

	msg, ok := parsePrivmsg(line)
	if !ok {
		return
	}

	text := strings.TrimSpace(msg.Text)
	if !strings.HasPrefix(text, "!") {
		return
	}
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])

	// Only the broadcaster and moderators may control the queue.
	if !msg.IsMod && !msg.IsBroadcaster {
		return
	}

	switch cmd {
	case "!skip", "!next":
		b.actions.Skip()
		b.reply(conn, "⏭ Skipped.")
	case "!pause":
		b.actions.Pause()
		b.reply(conn, "⏸ Paused.")
	case "!resume", "!play", "!unpause":
		b.actions.Resume()
		b.reply(conn, "▶ Resumed.")
	case "!clear":
		all := len(fields) > 1 && strings.EqualFold(fields[1], "all")
		b.actions.Clear(all)
		if all {
			b.reply(conn, "🗑 Cleared everything.")
		} else {
			b.reply(conn, "🧹 Cleared the queue.")
		}
	case "!current", "!nowplaying", "!np":
		if it := b.actions.NowPlaying(); it != nil {
			by := ""
			if it.SubmitterName != "" {
				by = " (by " + it.SubmitterName + ")"
			}
			b.reply(conn, "▶ Now playing: "+it.Title+by)
		} else {
			b.reply(conn, "Nothing is playing right now.")
		}
	}
}

func (b *Bot) reply(conn *websocket.Conn, text string) {
	_ = conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil, "PRIVMSG #%s :%s", b.channel, text))
}
