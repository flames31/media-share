// Package config loads runtime configuration from environment variables.
package config

import (
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Config holds all runtime settings for the server and Twitch bot.
type Config struct {
	Port          string
	AdminToken    string
	MediaDir      string
	MaxUploadMB   int64
	AllowedExt    []string // media file extensions allowed for upload, lowercase with dot (e.g. ".mp4")
	PublicBaseURL string   // optional, used only for display in logs

	// Twitch (bot only starts when Channel + Username + OAuthToken are all set)
	TwitchChannel  string
	TwitchUsername string
	TwitchToken    string
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	c := &Config{
		Port:           env("PORT", "8080"),
		AdminToken:     env("ADMIN_TOKEN", ""),
		MediaDir:       env("MEDIA_DIR", "./media"),
		MaxUploadMB:    envInt("MAX_UPLOAD_MB", 100),
		AllowedExt:     splitExt(env("ALLOWED_MEDIA_EXT", "mp4,webm,mov,ogg")),
		PublicBaseURL:  env("PUBLIC_BASE_URL", ""),
		TwitchChannel:  strings.ToLower(strings.TrimPrefix(env("TWITCH_CHANNEL", ""), "#")),
		TwitchUsername: strings.ToLower(env("TWITCH_BOT_USERNAME", "")),
		TwitchToken:    env("TWITCH_OAUTH_TOKEN", ""),
	}
	if c.AdminToken == "" {
		log.Println("WARNING: ADMIN_TOKEN is empty; the admin console will be unprotected. Set ADMIN_TOKEN before exposing this server.")
	}
	return c
}

// TwitchEnabled reports whether the chat bot has enough config to start.
func (c *Config) TwitchEnabled() bool {
	return c.TwitchChannel != "" && c.TwitchUsername != "" && c.TwitchToken != ""
}

// MaxUploadBytes returns the upload size cap in bytes.
func (c *Config) MaxUploadBytes() int64 {
	return c.MaxUploadMB * 1024 * 1024
}

// ExtAllowed reports whether the given extension (with or without a leading dot)
// is permitted for upload.
func (c *Config) ExtAllowed(ext string) bool {
	ext = strings.ToLower(ext)
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return slices.Contains(c.AllowedExt, ext)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		log.Printf("WARNING: %s=%q is not a valid integer; using default %d", key, v, def)
	}
	return def
}

// splitExt turns "mp4, webm" into [".mp4", ".webm"].
func splitExt(csv string) []string {
	var out []string
	for part := range strings.SplitSeq(csv, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, ".") {
			part = "." + part
		}
		out = append(out, part)
	}
	return out
}
