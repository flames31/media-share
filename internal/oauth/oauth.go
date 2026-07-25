// Package oauth is a small Twitch OAuth (Authorization Code) client used for
// "Log in with Twitch". It resolves the authenticated user's identity so the
// platform can create/lookup their account.
package oauth

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

// Twitch endpoints. Overridable in tests.
var (
	AuthorizeEndpoint = "https://id.twitch.tv/oauth2/authorize"
	TokenEndpoint     = "https://id.twitch.tv/oauth2/token"
	HelixUsersURL     = "https://api.twitch.tv/helix/users"
)

// User is the subset of a Twitch user we need for an account.
type User struct {
	ID          string
	Login       string
	DisplayName string
}

// Client performs the OAuth handshake against Twitch.
type Client struct {
	clientID     string
	clientSecret string
	redirectURI  string
	http         *http.Client
}

// New builds a Client.
func New(clientID, clientSecret, redirectURI string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

// AuthorizeURL builds the consent URL. scopes may be empty for identity-only login.
func (c *Client) AuthorizeURL(state string, scopes ...string) string {
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("redirect_uri", c.redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	return AuthorizeEndpoint + "?" + q.Encode()
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	Message     string `json:"message"`
}

// ExchangeCodeForUser swaps an authorization code for an access token and returns
// the authenticated Twitch user.
func (c *Client) ExchangeCodeForUser(ctx context.Context, code string) (*User, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", c.redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(form.Encode()))
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
		msg := firstNonEmpty(tr.Message, tr.Error, fmt.Sprintf("status %d", resp.StatusCode))
		return nil, fmt.Errorf("token endpoint: %s", msg)
	}
	return c.getUser(ctx, tr.AccessToken)
}

type helixUsersResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

func (c *Client) getUser(ctx context.Context, accessToken string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HelixUsersURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", c.clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("helix users: status %d", resp.StatusCode)
	}
	var ur helixUsersResponse
	if err := json.Unmarshal(body, &ur); err != nil || len(ur.Data) == 0 {
		return nil, fmt.Errorf("helix users: could not read identity")
	}
	u := ur.Data[0]
	dn := u.DisplayName
	if dn == "" {
		dn = u.Login
	}
	return &User{ID: u.ID, Login: strings.ToLower(u.Login), DisplayName: dn}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
