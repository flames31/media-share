package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnvLine(t *testing.T) {
	cases := []struct {
		line string
		key  string
		val  string
		ok   bool
	}{
		{"PORT=8080", "PORT", "8080", true},
		{"  export TOKEN=abc ", "TOKEN", "abc", true},
		{`NAME="Cool Streamer"`, "NAME", "Cool Streamer", true},
		{"NAME='single quoted'", "NAME", "single quoted", true},
		{"URL=http://x  # trailing comment", "URL", "http://x", true},
		{"EMPTY=", "EMPTY", "", true},
		{"# a comment", "", "", false},
		{"", "", "", false},
		{"noequals", "", "", false},
		{"=novalue", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseDotEnvLine(c.line)
		if ok != c.ok || k != c.key || v != c.val {
			t.Errorf("parseDotEnvLine(%q) = (%q,%q,%v), want (%q,%q,%v)", c.line, k, v, ok, c.key, c.val, c.ok)
		}
	}
}

func TestLoadDotEnvSetsUnsetKeysOnly(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := "FROM_FILE=filevalue\nOVERRIDDEN=fromfile\n# comment\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ENV_FILE", envPath)
	t.Setenv("OVERRIDDEN", "fromenv") // real env should win
	os.Unsetenv("FROM_FILE")
	t.Cleanup(func() { os.Unsetenv("FROM_FILE") })

	loadDotEnv()

	if got := os.Getenv("FROM_FILE"); got != "filevalue" {
		t.Errorf("FROM_FILE = %q, want filevalue", got)
	}
	if got := os.Getenv("OVERRIDDEN"); got != "fromenv" {
		t.Errorf("OVERRIDDEN = %q, want fromenv (real env must win)", got)
	}
}
