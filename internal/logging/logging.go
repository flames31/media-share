// Package logging configures the process-wide structured logger (log/slog).
//
// There is one handler for the whole app; every package logs through the slog
// default logger (slog.Info/Warn/Error/Debug). Levels:
//
//   - Error — something failed that needs attention (a request/op couldn't complete).
//   - Warn  — a suspicious-but-handled condition (bad signature, misconfiguration).
//   - Info  — important flow checkpoints in production (logins, submissions, credits).
//   - Debug — verbose detail, only emitted when DEV_LOGIN is on (dev endpoints,
//     dedup no-ops, per-request minutiae).
//
// Output goes to stderr (the terminal) today. To also (or instead) write to a
// file later, swap the io.Writer in Init for an *os.File or an io.MultiWriter —
// nothing else changes.
package logging

import (
	"log/slog"
	"os"
)

// level is the live log level. It starts at Info (LevelVar's zero value) so that
// anything logged during startup, before SetDevLogin runs, is visible.
var level = new(slog.LevelVar)

// Init installs the process-wide slog handler. Call it once, as early as possible
// in main, before anything logs.
func Init() {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// SetDevLogin raises the log level to Debug when DEV_LOGIN is enabled, so dev-only
// detail shows up locally without bloating production logs. Call it right after
// config is loaded.
func SetDevLogin(devLogin bool) {
	if devLogin {
		level.Set(slog.LevelDebug)
		slog.Debug("debug logging enabled (DEV_LOGIN)")
		return
	}
	level.Set(slog.LevelInfo)
}
