package twitch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// Broadcaster pushes status updates to connected clients. *hub.Hub satisfies it.
type Broadcaster interface {
	Broadcast(msgType string, payload any)
}

// Status is the connection state surfaced to the admin UI.
type Status struct {
	Available bool   `json:"available"` // OAuth configured — the Connect button can be shown
	Connected bool   `json:"connected"`
	BotLogin  string `json:"botLogin,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Error     string `json:"error,omitempty"`
}

const stateTTL = 10 * time.Minute

type stateEntry struct {
	channel string
	expires time.Time
}

// Controller owns the Twitch bot lifecycle: OAuth connect/disconnect, token
// storage and refresh, and (re)starting the chat bot. It is safe for concurrent
// use.
type Controller struct {
	oauth       *oauthClient
	store       TokenStore
	actions     Actions
	broadcaster Broadcaster
	available   bool
	clientID    string
	redirectURI string

	rootCtx context.Context

	mu      sync.Mutex
	token   *Token
	cancel  context.CancelFunc
	lastErr string
	states  map[string]stateEntry
}

// NewController builds a Controller. When the OAuth client id/secret are unset,
// the Connect flow is unavailable but a legacy token can still be started.
func NewController(clientID, clientSecret, redirectURI string, store TokenStore, actions Actions, b Broadcaster) *Controller {
	return &Controller{
		oauth:       newOAuthClient(clientID, clientSecret, redirectURI),
		store:       store,
		actions:     actions,
		broadcaster: b,
		available:   clientID != "" && clientSecret != "",
		clientID:    clientID,
		redirectURI: redirectURI,
		states:      map[string]stateEntry{},
	}
}

// Start loads any persisted token (or the provided legacy token) and launches
// the bot if credentials are available. ctx bounds the lifetime of all bot
// goroutines. legacy may be nil.
func (c *Controller) Start(ctx context.Context, legacy *Token) {
	c.mu.Lock()
	c.rootCtx = ctx
	c.mu.Unlock()

	tok, err := c.store.Get()
	if err != nil && !errors.Is(err, ErrNoToken) {
		log.Printf("twitch: could not read stored token: %v", err)
	}
	if tok == nil && legacy != nil {
		tok = legacy // in-memory only; legacy manual tokens are not persisted
		log.Printf("twitch: using legacy TWITCH_OAUTH_TOKEN for #%s", legacy.Channel)
	}
	if tok == nil {
		if c.available {
			log.Println("twitch: ready — connect a bot account from the admin console")
		} else {
			log.Println("twitch: disabled (set TWITCH_CLIENT_ID/SECRET for the Connect button, or the legacy TWITCH_* vars)")
		}
		c.broadcastStatus()
		return
	}

	c.mu.Lock()
	c.token = tok
	c.mu.Unlock()
	c.startBot()
	c.broadcastStatus()
}

// Connect completes the OAuth flow: it exchanges code, persists the token, and
// (re)starts the bot joining channel.
func (c *Controller) Connect(ctx context.Context, code, channel string) error {
	tok, err := c.oauth.ExchangeCode(ctx, code)
	if err != nil {
		c.setError(err.Error())
		return err
	}
	tok.Channel = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(channel), "#"))
	if tok.Channel == "" {
		tok.Channel = tok.BotLogin // default: join the bot's own channel
	}
	if err := c.store.Save(tok); err != nil {
		c.setError("could not save token: " + err.Error())
		return err
	}

	c.mu.Lock()
	c.token = tok
	c.lastErr = ""
	c.mu.Unlock()

	c.startBot()
	c.broadcastStatus()
	return nil
}

// Disconnect stops the bot and clears the stored token.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.token = nil
	c.lastErr = ""
	c.mu.Unlock()

	if err := c.store.Clear(); err != nil {
		log.Printf("twitch: clear token: %v", err)
	}
	log.Println("twitch: disconnected")
	c.broadcastStatus()
}

// Status returns the current connection state.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Available: c.available, Error: c.lastErr}
	if c.token != nil {
		s.Connected = c.cancel != nil
		s.BotLogin = c.token.BotLogin
		s.Channel = c.token.Channel
	}
	return s
}

// startBot cancels any running bot and launches a fresh one bound to rootCtx.
func (c *Controller) startBot() {
	c.mu.Lock()
	if c.rootCtx == nil {
		c.mu.Unlock()
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	botCtx, cancel := context.WithCancel(c.rootCtx)
	c.cancel = cancel
	c.mu.Unlock()

	bot := NewBot(c.credentials, c.actions)
	go bot.Run(botCtx)
}

// credentials returns fresh credentials for the bot, refreshing the token when
// it is expired and a refresh token is available.
func (c *Controller) credentials(ctx context.Context) (Creds, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()

	if tok == nil {
		return Creds{}, errors.New("not connected")
	}

	if tok.Expired() && tok.RefreshToken != "" {
		refreshed, err := c.oauth.Refresh(ctx, tok)
		if err != nil {
			c.setError("token refresh failed: " + err.Error())
			return Creds{}, err
		}
		if err := c.store.Save(refreshed); err != nil {
			log.Printf("twitch: save refreshed token: %v", err)
		}
		c.mu.Lock()
		c.token = refreshed
		c.lastErr = ""
		c.mu.Unlock()
		tok = refreshed
		log.Printf("twitch: refreshed access token for %s", tok.BotLogin)
	}

	return Creds{AccessToken: tok.AccessToken, Login: tok.BotLogin, Channel: tok.Channel}, nil
}

func (c *Controller) setError(msg string) {
	c.mu.Lock()
	c.lastErr = msg
	c.mu.Unlock()
	log.Printf("twitch: %s", msg)
	c.broadcastStatus()
}

func (c *Controller) broadcastStatus() {
	if c.broadcaster != nil {
		c.broadcaster.Broadcast("twitch", c.Status())
	}
}

// NewState creates a single-use OAuth state bound to the target channel.
func (c *Controller) NewState(channel string) string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	state := hex.EncodeToString(buf)

	c.mu.Lock()
	c.pruneStatesLocked()
	c.states[state] = stateEntry{channel: channel, expires: time.Now().Add(stateTTL)}
	c.mu.Unlock()
	return state
}

// ConsumeState validates and removes a state, returning its channel.
func (c *Controller) ConsumeState(state string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneStatesLocked()
	e, ok := c.states[state]
	if !ok {
		return "", false
	}
	delete(c.states, state)
	return e.channel, true
}

func (c *Controller) pruneStatesLocked() {
	now := time.Now()
	for s, e := range c.states {
		if now.After(e.expires) {
			delete(c.states, s)
		}
	}
}

// Available reports whether the OAuth Connect flow is configured.
func (c *Controller) Available() bool { return c.available }

// AuthorizeURL builds the consent URL for the given state.
func (c *Controller) AuthorizeURL(state string) string {
	return AuthorizeURL(c.clientID, c.redirectURI, state)
}
