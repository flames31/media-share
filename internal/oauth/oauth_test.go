package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAuthorizeURL(t *testing.T) {
	c := New("cid", "secret", "http://localhost:8080/auth/twitch/callback")
	raw := c.AuthorizeURL("st8")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("response_type") != "code" || q.Get("state") != "st8" {
		t.Errorf("unexpected query: %v", q)
	}
	if q.Get("redirect_uri") != "http://localhost:8080/auth/twitch/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestExchangeCodeForUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != "abc" {
				t.Errorf("bad token request: %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"AT","expires_in":3600}`))
		case "/users":
			if r.Header.Get("Authorization") != "Bearer AT" || r.Header.Get("Client-Id") != "cid" {
				t.Errorf("bad users headers: %v", r.Header)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"555","login":"CoolGuy","display_name":"CoolGuy"}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	TokenEndpoint = srv.URL + "/token"
	HelixUsersURL = srv.URL + "/users"

	c := New("cid", "secret", "http://localhost/cb")
	u, err := c.ExchangeCodeForUser(context.Background(), "abc")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if u.ID != "555" || u.Login != "coolguy" || u.DisplayName != "CoolGuy" {
		t.Errorf("user = %+v", u)
	}
}

func TestExchangeErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":400,"message":"invalid client"}`))
	}))
	defer srv.Close()
	TokenEndpoint = srv.URL + "/token"

	c := New("cid", "secret", "http://localhost/cb")
	if _, err := c.ExchangeCodeForUser(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}
