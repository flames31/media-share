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
	PublicBaseURL string   // optional; base for OAuth redirect + logs. Empty => http://localhost:<Port>
	DataDir       string   // where persistent state is stored
	DBPath        string   // SQLite database path

	// Twitch legacy manual bot (used as a fallback when OAuth is not configured).
	// The bot starts from this only when Channel + Username + OAuthToken are all set.
	TwitchChannel  string
	TwitchUsername string
	TwitchToken    string

	// Twitch OAuth app credentials (registered once by the server operator).
	// When both are set, the "Connect with Twitch" flow is available.
	TwitchClientID     string
	TwitchClientSecret string

	// DevLogin, when true, enables a password-less local "Dev login" so you can
	// try the app without registering a Twitch app. Never enable in production.
	DevLogin bool
}

// Load reads configuration from a .env file (if present) and the environment,
// applying sensible defaults. Real environment variables take precedence over
// values in .env.
func Load() *Config {
	loadDotEnv()
	c := &Config{
		Port:           env("PORT", "8080"),
		AdminToken:     env("ADMIN_TOKEN", ""),
		MediaDir:       env("MEDIA_DIR", "./media"),
		MaxUploadMB:    envInt("MAX_UPLOAD_MB", 100),
		AllowedExt:     splitExt(env("ALLOWED_MEDIA_EXT", "mp4,webm,mov,ogg")),
		PublicBaseURL:  strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/"),
		DataDir:        env("DATA_DIR", "./data"),
		DBPath:         env("DB_PATH", "./data/media-share.db"),
		TwitchChannel:  strings.ToLower(strings.TrimPrefix(env("TWITCH_CHANNEL", ""), "#")),
		TwitchUsername: strings.ToLower(env("TWITCH_BOT_USERNAME", "")),
		TwitchToken:    env("TWITCH_OAUTH_TOKEN", ""),

		TwitchClientID:     env("TWITCH_CLIENT_ID", ""),
		TwitchClientSecret: env("TWITCH_CLIENT_SECRET", ""),
		DevLogin:           envBool("DEV_LOGIN", false),
	}
	if !c.OAuthEnabled() && !c.DevLogin {
		log.Println("WARNING: Twitch login is not configured. Set TWITCH_CLIENT_ID/SECRET, or DEV_LOGIN=1 for local testing — otherwise no one can log in.")
	}
	if c.DevLogin {
		log.Println("WARNING: DEV_LOGIN is enabled — anyone can log in as a local dev account without Twitch. Do NOT use in production.")
	}
	return c
}

// TwitchEnabled reports whether the legacy manual chat bot has enough config to
// start from environment variables.
func (c *Config) TwitchEnabled() bool {
	return c.TwitchChannel != "" && c.TwitchUsername != "" && c.TwitchToken != ""
}

// OAuthEnabled reports whether the "Connect with Twitch" flow can be offered.
func (c *Config) OAuthEnabled() bool {
	return c.TwitchClientID != "" && c.TwitchClientSecret != ""
}

// BaseURL returns the public base URL for building absolute links (e.g. the
// OAuth redirect URI). Falls back to http://localhost:<Port> for local use.
func (c *Config) BaseURL() string {
	if c.PublicBaseURL != "" {
		return c.PublicBaseURL
	}
	return "http://localhost:" + c.Port
}

// TwitchRedirectURI is the OAuth callback URL that must be registered in the
// Twitch application settings.
func (c *Config) TwitchRedirectURI() string {
	return c.BaseURL() + "/auth/twitch/callback"
}

// CookieSecure reports whether login cookies should carry the Secure flag
// (true when the public base URL is HTTPS).
func (c *Config) CookieSecure() bool {
	return strings.HasPrefix(c.BaseURL(), "https://")
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

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
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
