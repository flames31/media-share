package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Twitch OAuth / API endpoints. Overridable in tests.
var (
	authorizeEndpoint = "https://id.twitch.tv/oauth2/authorize"
	tokenEndpoint     = "https://id.twitch.tv/oauth2/token"
	helixUsersURL     = "https://api.twitch.tv/helix/users"
)

// chatScopes are requested for a bot that reads and posts chat.
var chatScopes = []string{"chat:read", "chat:edit"}

// Token holds the credentials and identity for a connected bot account.
type Token struct {
	BotLogin     string    `json:"botLogin"`
	Channel      string    `json:"channel"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Expired reports whether the access token is at or past its expiry, with a
// small safety skew so we refresh slightly early.
func (t *Token) Expired() bool {
	if t == nil {
		return true
	}
	return time.Now().Add(30 * time.Second).After(t.ExpiresAt)
}

// AuthorizeURL builds the Twitch consent URL for the Authorization Code flow.
func AuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(chatScopes, " "))
	q.Set("state", state)
	q.Set("force_verify", "true") // always show the account picker so the bot account can be chosen
	return authorizeEndpoint + "?" + q.Encode()
}

// oauthClient performs the token/identity HTTP calls.
type oauthClient struct {
	clientID     string
	clientSecret string
	redirectURI  string
	http         *http.Client
}

func newOAuthClient(clientID, clientSecret, redirectURI string) *oauthClient {
	return &oauthClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	Message      string `json:"message"`
}

// ExchangeCode swaps an authorization code for tokens and resolves the bot login.
func (c *oauthClient) ExchangeCode(ctx context.Context, code string) (*Token, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", c.redirectURI)

	tr, err := c.postToken(ctx, form)
	if err != nil {
		return nil, err
	}
	tok := &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
	login, err := c.getUserLogin(ctx, tok.AccessToken)
	if err != nil {
		return nil, err
	}
	tok.BotLogin = login
	return tok, nil
}

// Refresh exchanges a refresh token for a fresh access token. It preserves the
// caller's BotLogin/Channel by copying from prev.
func (c *oauthClient) Refresh(ctx context.Context, prev *Token) (*Token, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", prev.RefreshToken)

	tr, err := c.postToken(ctx, form)
	if err != nil {
		return nil, err
	}
	next := &Token{
		BotLogin:     prev.BotLogin,
		Channel:      prev.Channel,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
	// Twitch may omit a new refresh token; keep the old one if so.
	if next.RefreshToken == "" {
		next.RefreshToken = prev.RefreshToken
	}
	return next, nil
}

func (c *oauthClient) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token endpoint: bad response (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := tr.Message
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("token endpoint: %s", msg)
	}
	return &tr, nil
}

type helixUsersResponse struct {
	Data []struct {
		Login string `json:"login"`
	} `json:"data"`
}

// getUserLogin resolves the login name of the account that owns accessToken.
func (c *oauthClient) getUserLogin(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, helixUsersURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Client-Id", c.clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("helix users: status %d", resp.StatusCode)
	}
	var ur helixUsersResponse
	if err := json.Unmarshal(body, &ur); err != nil || len(ur.Data) == 0 {
		return "", fmt.Errorf("helix users: could not read login")
	}
	return strings.ToLower(ur.Data[0].Login), nil
}
