package twitch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizeURL(t *testing.T) {
	raw := AuthorizeURL("cid", "http://localhost:8080/auth/twitch/callback", "st8")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("redirect_uri") != "http://localhost:8080/auth/twitch/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "st8" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("scope") != "chat:read chat:edit" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.FormValue("grant_type") != "authorization_code" {
				t.Errorf("grant_type = %q", r.FormValue("grant_type"))
			}
			if r.FormValue("code") != "the-code" {
				t.Errorf("code = %q", r.FormValue("code"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
		case "/helix/users":
			if r.Header.Get("Authorization") != "Bearer AT" {
				t.Errorf("auth header = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Client-Id") != "cid" {
				t.Errorf("client-id header = %q", r.Header.Get("Client-Id"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"login":"BotName"}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	tokenEndpoint = srv.URL + "/oauth2/token"
	helixUsersURL = srv.URL + "/helix/users"

	c := newOAuthClient("cid", "secret", "http://localhost/cb")
	tok, err := c.ExchangeCode(context.Background(), "the-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok.AccessToken != "AT" || tok.RefreshToken != "RT" {
		t.Errorf("tokens = %q / %q", tok.AccessToken, tok.RefreshToken)
	}
	if tok.BotLogin != "botname" {
		t.Errorf("login = %q, want lowercased botname", tok.BotLogin)
	}
	if tok.Expired() {
		t.Error("token should not be expired")
	}
}

func TestRefreshKeepsIdentityAndOldRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "OLD-RT" {
			t.Errorf("refresh_token = %q", r.FormValue("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		// Note: no refresh_token in the response — should keep the old one.
		w.Write([]byte(`{"access_token":"NEW-AT","expires_in":3600}`))
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL + "/oauth2/token"

	c := newOAuthClient("cid", "secret", "http://localhost/cb")
	prev := &Token{BotLogin: "bot", Channel: "chan", AccessToken: "OLD-AT", RefreshToken: "OLD-RT"}
	next, err := c.Refresh(context.Background(), prev)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if next.AccessToken != "NEW-AT" {
		t.Errorf("access = %q", next.AccessToken)
	}
	if next.RefreshToken != "OLD-RT" {
		t.Errorf("refresh should be preserved, got %q", next.RefreshToken)
	}
	if next.BotLogin != "bot" || next.Channel != "chan" {
		t.Errorf("identity not preserved: %+v", next)
	}
}

func TestExchangeCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":400,"message":"Invalid authorization code"}`))
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL + "/oauth2/token"

	c := newOAuthClient("cid", "secret", "http://localhost/cb")
	_, err := c.ExchangeCode(context.Background(), "bad")
	if err == nil || !strings.Contains(err.Error(), "Invalid authorization code") {
		t.Fatalf("expected error with message, got %v", err)
	}
}
