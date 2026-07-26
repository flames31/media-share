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
	cookieName       = "sid"  // streamer / moderator login cookie
	viewerCookieName = "vsid" // viewer login cookie (separate identity)
	stateTTL         = 10 * time.Minute
	sessionTTL       = 30 * 24 * time.Hour
)

// OAuth login kinds carried in the state so the shared /auth/twitch/callback can
// tell a streamer login apart from a viewer login (both use one redirect URI).
const (
	KindOwner  = "owner"
	KindViewer = "viewer"
)

// ErrUnauthenticated is returned when a request has no valid login session.
var ErrUnauthenticated = errors.New("unauthenticated")

// stateEntry records a pending OAuth handshake: when it expires, whether it's an
// owner or viewer login, and (for viewers) the submit token to return them to.
type stateEntry struct {
	expires  time.Time
	kind     string
	returnTo string
}

// StateInfo is the consumed state's login intent, returned to the callback handler.
type StateInfo struct {
	Kind     string
	ReturnTo string
}

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

// AuthorizeURL creates a single-use state for a streamer login and returns the
// Twitch consent URL (no scopes needed for basic identity).
func (a *Authenticator) AuthorizeURL() string {
	return a.authorizeURL(KindOwner, "")
}

// ViewerAuthorizeURL creates a single-use state for a viewer login and returns the
// Twitch consent URL. returnToken is the submit session token the viewer should be
// dropped back onto after login.
func (a *Authenticator) ViewerAuthorizeURL(returnToken string) string {
	return a.authorizeURL(KindViewer, returnToken)
}

func (a *Authenticator) authorizeURL(kind, returnTo string) string {
	state := randHex(24)
	a.mu.Lock()
	a.pruneLocked()
	a.states[state] = stateEntry{expires: time.Now().Add(stateTTL), kind: kind, returnTo: returnTo}
	a.mu.Unlock()
	return a.oauth.AuthorizeURL(state)
}

// ConsumeState validates and consumes a single-use OAuth state, returning its
// login intent. The callback handler calls this once, then dispatches to Login or
// LoginViewer based on the kind.
func (a *Authenticator) ConsumeState(state string) (StateInfo, bool) {
	e, ok := a.consumeStateEntry(state)
	if !ok {
		return StateInfo{}, false
	}
	kind := e.kind
	if kind == "" {
		kind = KindOwner // states minted before kinds existed default to owner
	}
	return StateInfo{Kind: kind, ReturnTo: e.returnTo}, true
}

// Login exchanges the code for the Twitch identity, upserts the streamer account,
// opens a login session, and writes the cookie. The caller must have already
// consumed the OAuth state (see ConsumeState). Returns the streamer.
func (a *Authenticator) Login(ctx context.Context, w http.ResponseWriter, code string) (*store.Streamer, error) {
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

// LoginViewer exchanges the code for the Twitch identity, upserts the viewer,
// opens a viewer session, and writes the vsid cookie. The caller must have already
// consumed the OAuth state.
func (a *Authenticator) LoginViewer(ctx context.Context, w http.ResponseWriter, code string) (*store.Viewer, error) {
	u, err := a.oauth.ExchangeCodeForUser(ctx, code)
	if err != nil {
		return nil, err
	}
	viewer, err := a.store.UpsertViewer(u.ID, u.Login, u.DisplayName)
	if err != nil {
		return nil, err
	}
	vsid, err := a.store.CreateViewerSession(viewer.ID, sessionTTL)
	if err != nil {
		return nil, err
	}
	a.setViewerCookie(w, vsid)
	return viewer, nil
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

// DevLoginViewer logs the caller in as a fixed local dev viewer, bypassing Twitch.
// Only reachable when DEV_LOGIN is enabled (enforced by the caller), so spending
// can be tested without a real Twitch login.
func (a *Authenticator) DevLoginViewer(w http.ResponseWriter) (*store.Viewer, error) {
	viewer, err := a.store.UpsertViewer("devviewer", "devviewer", "Dev Viewer")
	if err != nil {
		return nil, err
	}
	vsid, err := a.store.CreateViewerSession(viewer.ID, sessionTTL)
	if err != nil {
		return nil, err
	}
	a.setViewerCookie(w, vsid)
	return viewer, nil
}

// Logout clears the current streamer login session and cookie.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = a.store.DeleteAuthSession(c.Value)
	}
	a.clearCookie(w)
}

// LogoutViewer clears the current viewer login session and cookie.
func (a *Authenticator) LogoutViewer(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(viewerCookieName); err == nil {
		_ = a.store.DeleteViewerSession(c.Value)
	}
	a.clearViewerCookie(w)
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

// AuthenticateViewer resolves the viewer for a request from the vsid cookie, or
// ErrUnauthenticated.
func (a *Authenticator) AuthenticateViewer(r *http.Request) (*store.Viewer, error) {
	c, err := r.Cookie(viewerCookieName)
	if err != nil || c.Value == "" {
		return nil, ErrUnauthenticated
	}
	v, err := a.store.GetValidViewerSession(c.Value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// --- request context ---

type ctxKey struct{}
type roleKey struct{}
type viewerKey struct{}

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

// WithViewer returns a copy of ctx carrying the viewer.
func WithViewer(ctx context.Context, v *store.Viewer) context.Context {
	return context.WithValue(ctx, viewerKey{}, v)
}

// ViewerFrom extracts the viewer placed by RequireViewer.
func ViewerFrom(ctx context.Context) (*store.Viewer, bool) {
	v, ok := ctx.Value(viewerKey{}).(*store.Viewer)
	return v, ok
}

// RequireViewer is middleware for viewer-facing JSON APIs: it 401s requests
// without a valid viewer session and otherwise injects the viewer into context.
// It applies the same same-origin (CSRF) guard on unsafe methods as
// RequireStreamer.
func (a *Authenticator) RequireViewer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameSite(r) {
			http.Error(w, `{"error":"cross-site request blocked"}`, http.StatusForbidden)
			return
		}
		v, err := a.AuthenticateViewer(r)
		if err != nil {
			http.Error(w, `{"error":"login required"}`, http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(WithViewer(r.Context(), v)))
	}
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

func (a *Authenticator) setViewerCookie(w http.ResponseWriter, vsid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     viewerCookieName,
		Value:    vsid,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL / time.Second),
	})
}

func (a *Authenticator) clearViewerCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     viewerCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// --- state ---

func (a *Authenticator) consumeStateEntry(state string) (stateEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	e, ok := a.states[state]
	if !ok {
		return stateEntry{}, false
	}
	delete(a.states, state)
	return e, true
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
