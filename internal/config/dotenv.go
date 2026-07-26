package config

import (
	"bufio"
	"log/slog"
	"os"
	"strings"
)

// loadDotEnv reads a .env file (path from ENV_FILE, default ".env") and sets any
// keys not already present in the process environment. Real environment
// variables always win, so `FOO=bar go run .` overrides the file.
//
// It is best-effort: a missing file is not an error. Supported lines:
//
//	KEY=value
//	export KEY=value
//	KEY="quoted value"     KEY='quoted value'
//	# comment              KEY=value   # trailing comment (unquoted values only)
func loadDotEnv() {
	path := os.Getenv("ENV_FILE")
	if path == "" {
		path = ".env"
	}
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not read env file", "path", path, "err", err)
		}
		return
	}
	defer f.Close()

	loaded := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseDotEnvLine(sc.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // real environment takes precedence
		}
		if err := os.Setenv(key, val); err == nil {
			loaded++
		}
	}
	if err := sc.Err(); err != nil {
		slog.Warn("error reading env file", "path", path, "err", err)
		return
	}
	if loaded > 0 {
		slog.Debug("loaded settings from env file", "count", loaded, "path", path)
	}
}

// parseDotEnvLine parses a single line into a key/value pair. ok is false for
// blank lines, comments, and malformed lines.
func parseDotEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")

	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eq])
	val = strings.TrimSpace(line[eq+1:])
	if key == "" {
		return "", "", false
	}

	// Quoted values are taken verbatim (inside the quotes); unquoted values may
	// carry a trailing "# comment".
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		quote := val[0]
		if end := strings.IndexByte(val[1:], quote); end >= 0 {
			val = val[1 : 1+end]
		} else {
			val = val[1:]
		}
	} else if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return key, val, true
}
