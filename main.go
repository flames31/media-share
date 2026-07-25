// Command media-share runs a multi-tenant media-share platform: streamers log in
// with Twitch, open a submission session, and share an invite link; viewers submit
// YouTube clips or uploads to that streamer's queue, which the streamer moderates
// in their console and plays on a per-streamer player page (for OBS).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"media-share/internal/auth"
	"media-share/internal/config"
	"media-share/internal/hub"
	"media-share/internal/oauth"
	"media-share/internal/server"
	"media-share/internal/store"
	"media-share/internal/tenant"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("media-share: ")

	cfg := config.Load()

	// Ensure the data directory exists for the SQLite database.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	// Hub delivers room-scoped (per-streamer) updates; the registry owns each
	// streamer's queue + session and provides a connecting client's initial state.
	h := hub.New(nil)
	reg := tenant.NewRegistry(h)
	h.OnConnect = reg.InitialMessages

	// "Log in with Twitch" for streamer accounts.
	oauthClient := oauth.New(cfg.TwitchClientID, cfg.TwitchClientSecret, cfg.TwitchRedirectURI())
	authn := auth.New(oauthClient, db, cfg.CookieSecure())

	srv := server.New(cfg, db, reg, authn, h)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		base := cfg.BaseURL()
		log.Printf("listening on %s", base)
		log.Printf("  login/console: %s/admin", base)
		if cfg.OAuthEnabled() {
			log.Printf("  Twitch OAuth redirect (register this): %s", cfg.TwitchRedirectURI())
		} else {
			log.Println("  WARNING: set TWITCH_CLIENT_ID/SECRET to enable 'Log in with Twitch'")
		}
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
