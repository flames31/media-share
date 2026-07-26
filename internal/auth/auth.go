// Package auth handles "Log in with Twitch": the OAuth handshake, opaque
// cookie-backed login sessions, and request authentication middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"media-share/internal/oauth"
	"media-share/internal/store"
)

const (
	cookieName = "sid"
	stateTTL   = 10 * time.Minute
	sessionTTL = 30 * 24 * time.Hour
)

// ErrUnauthenticated is returned when a request has no valid login session.
var ErrUnauthenticated = errors.New("unauthenticated")

type stateEntry struct{ expires time.Time }

// Authenticator ties the OAuth client, account store, and login-session cookies
// together.
type Authenticator struct {
	oauth  *oauth.Client
	store  *store.Store
	secure bool // set Secure flag on cookies (behind HTTPS)

	mu     sync.Mutex
	states map[string]stateEntry
}

// New builds an Authenticator. secure should be true when served over HTTPS.
func New(o *oauth.Client, s *store.Store, secure bool) *Authenticator {
	return &Authenticator{oauth: o, store: s, secure: secure, states: map[string]stateEntry{}}
}

// AuthorizeURL creates a single-use state and returns the Twitch consent URL for
// identity login (no scopes needed for basic identity).
func (a *Authenticator) AuthorizeURL() string {
	state := randHex(24)
	a.mu.Lock()
	a.pruneLocked()
	a.states[state] = stateEntry{expires: time.Now().Add(stateTTL)}
	a.mu.Unlock()
	return a.oauth.AuthorizeURL(state)
}

// Login completes the OAuth callback: it validates state, exchanges the code for
// the Twitch identity, upserts the account, opens a login session, and writes the
// cookie. Returns the streamer.
func (a *Authenticator) Login(ctx context.Context, w http.ResponseWriter, code, state string) (*store.Streamer, error) {
	if !a.consumeState(state) {
		return nil, errors.New("invalid or expired login state")
	}
	u, err := a.oauth.ExchangeCodeForUser(ctx, code)
	if err != nil {
		return nil, err
	}
	streamer, err := a.store.UpsertStreamer(u.ID, u.Login, u.DisplayName)
	if err != nil {
		return nil, err
	}
	sid, err := a.store.CreateAuthSession(streamer.ID, store.RoleOwner, sessionTTL)
	if err != nil {
		return nil, err
	}
	a.setCookie(w, sid)
	return streamer, nil
}

// LoginModerator opens a moderator login session for the given tenant owner and
// writes the cookie. The moderator has no account of their own; their session's
// streamer is the owner, distinguished by the moderator role. Callers must have
// already validated the moderator link that authorized this.
func (a *Authenticator) LoginModerator(w http.ResponseWriter, ownerID string) error {
	sid, err := a.store.CreateAuthSession(ownerID, store.RoleModerator, sessionTTL)
	if err != nil {
		return err
	}
	a.setCookie(w, sid)
	return nil
}

// DevLogin logs the caller in as a fixed local dev account, bypassing Twitch.
// It is only reachable when the server has DEV_LOGIN enabled (enforced by the
// caller) and exists purely so the app can be tried without a Twitch app.
func (a *Authenticator) DevLogin(w http.ResponseWriter) (*store.Streamer, error) {
	streamer, err := a.store.UpsertStreamer("dev", "dev", "Dev Streamer")
	if err != nil {
		return nil, err
	}
	sid, err := a.store.CreateAuthSession(streamer.ID, store.RoleOwner, sessionTTL)
	if err != nil {
		return nil, err
	}
	a.setCookie(w, sid)
	return streamer, nil
}

// Logout clears the current login session and cookie.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = a.store.DeleteAuthSession(c.Value)
	}
	a.clearCookie(w)
}

// Authenticate resolves the streamer (tenant owner) and the caller's role for a
// request, or ErrUnauthenticated.
func (a *Authenticator) Authenticate(r *http.Request) (*store.Streamer, string, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, "", ErrUnauthenticated
	}
	st, role, err := a.store.GetValidAuthSession(c.Value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", ErrUnauthenticated
	}
	if err != nil {
		return nil, "", err
	}
	return st, role, nil
}

// --- request context ---

type ctxKey struct{}
type roleKey struct{}

// WithStreamer returns a copy of ctx carrying the streamer (the tenant owner).
func WithStreamer(ctx context.Context, s *store.Streamer) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// StreamerFrom extracts the streamer (tenant owner) placed by RequireStreamer.
func StreamerFrom(ctx context.Context) (*store.Streamer, bool) {
	s, ok := ctx.Value(ctxKey{}).(*store.Streamer)
	return s, ok
}

// WithRole returns a copy of ctx carrying the caller's role.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

// RoleFrom extracts the caller's role (store.RoleOwner / store.RoleModerator).
func RoleFrom(ctx context.Context) string {
	role, _ := ctx.Value(roleKey{}).(string)
	return role
}

// RequireStreamer is middleware for JSON APIs: it 401s unauthenticated requests
// and otherwise injects the streamer (tenant owner) and role into the request
// context. It also applies a lightweight same-origin (CSRF) guard to unsafe
// methods. Both owners and moderators pass — use RequireOwner for owner-only
// endpoints.
func (a *Authenticator) RequireStreamer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameSite(r) {
			http.Error(w, `{"error":"cross-site request blocked"}`, http.StatusForbidden)
			return
		}
		st, role, err := a.Authenticate(r)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		ctx := WithRole(WithStreamer(r.Context(), st), role)
		next(w, r.WithContext(ctx))
	}
}

// RequireOwner is like RequireStreamer but rejects moderators, for endpoints only
// the tenant owner may use (e.g. managing moderator links).
func (a *Authenticator) RequireOwner(next http.HandlerFunc) http.HandlerFunc {
	return a.RequireStreamer(func(w http.ResponseWriter, r *http.Request) {
		if RoleFrom(r.Context()) != store.RoleOwner {
			http.Error(w, `{"error":"owner only"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// --- cookies ---

func (a *Authenticator) setCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL / time.Second),
	})
}

func (a *Authenticator) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// --- state ---

func (a *Authenticator) consumeState(state string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	if _, ok := a.states[state]; !ok {
		return false
	}
	delete(a.states, state)
	return true
}

func (a *Authenticator) pruneLocked() {
	now := time.Now()
	for s, e := range a.states {
		if now.After(e.expires) {
			delete(a.states, s)
		}
	}
}

// sameSite blocks obvious cross-site state-changing requests. Safe methods pass;
// for others, the Sec-Fetch-Site header (when present) must not be cross-site.
func sameSite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "", "same-origin", "same-site", "none":
		return true
	default:
		return false
	}
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
