// Command media-share runs a Twitch media-share queue: viewers submit YouTube
// clips or uploads, moderators verify them in an admin console, and an approved
// queue plays on a standalone player page. Twitch chat commands (!skip, !pause,
// …) control the queue when credentials are configured.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"media-share/internal/config"
	"media-share/internal/hub"
	"media-share/internal/queue"
	"media-share/internal/server"
	"media-share/internal/twitch"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("media-share: ")

	cfg := config.Load()

	// Wire the hub and manager together: every queue change broadcasts a state
	// snapshot to all connected clients.
	h := hub.New(nil)
	mgr := queue.NewManager(func(s queue.Snapshot) {
		h.Broadcast("state", s)
	})
	h.StateProvider = func() any { return mgr.Snapshot() }

	srv := server.New(cfg, mgr, h)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start the Twitch bot if configured.
	if cfg.TwitchEnabled() {
		bot := twitch.New(cfg.TwitchChannel, cfg.TwitchUsername, cfg.TwitchToken, mgr)
		go bot.Run(ctx)
	} else {
		log.Println("Twitch bot disabled (set TWITCH_CHANNEL, TWITCH_BOT_USERNAME, TWITCH_OAUTH_TOKEN to enable)")
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on http://localhost:%s", cfg.Port)
		log.Printf("  submit: http://localhost:%s/submit", cfg.Port)
		log.Printf("  player: http://localhost:%s/player", cfg.Port)
		log.Printf("  admin:  http://localhost:%s/admin", cfg.Port)
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
