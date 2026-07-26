// Command media-share runs a multi-tenant media-share platform: streamers log in
// with Twitch, open a submission session, and share an invite link; viewers submit
// YouTube clips or uploads to that streamer's queue, which the streamer moderates
// in their console and plays on a per-streamer player page (for OBS).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"media-share/internal/auth"
	"media-share/internal/config"
	"media-share/internal/hub"
	"media-share/internal/logging"
	"media-share/internal/oauth"
	"media-share/internal/server"
	"media-share/internal/store"
	"media-share/internal/tenant"
)

func main() {
	logging.Init()

	cfg := config.Load()
	logging.SetDevLogin(cfg.DevLogin)
	slog.Info("starting media-share",
		"base_url", cfg.BaseURL(),
		"oauth", cfg.OAuthEnabled(),
		"dev_login", cfg.DevLogin,
		"credits_enabled", cfg.CreditsEnabled,
	)

	// Ensure the data directory exists for the SQLite database.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		slog.Error("create data dir", "err", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
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
		slog.Info("server listening", "addr", httpServer.Addr, "console", base+"/admin")
		if cfg.OAuthEnabled() {
			slog.Info("register this Twitch OAuth redirect", "url", cfg.TwitchRedirectURI())
		} else {
			slog.Warn("Twitch login disabled: set TWITCH_CLIENT_ID/SECRET to enable 'Log in with Twitch'")
		}
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}
