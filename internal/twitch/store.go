package twitch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ErrNoToken is returned by a TokenStore when no token has been saved.
var ErrNoToken = errors.New("no token stored")

// TokenStore persists the connected bot's Token. The single-tenant
// implementation stores one token; the interface intentionally leaves room for
// a future per-channel (multi-tenant) implementation.
type TokenStore interface {
	Get() (*Token, error) // returns ErrNoToken when absent
	Save(*Token) error
	Clear() error
}

// FileTokenStore stores a single Token as JSON on disk with 0600 permissions.
type FileTokenStore struct {
	path string
	mu   sync.Mutex
}

// NewFileTokenStore stores the token at <dir>/twitch.json.
func NewFileTokenStore(dir string) *FileTokenStore {
	return &FileTokenStore{path: filepath.Join(dir, "twitch.json")}
}

func (s *FileTokenStore) Get() (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoToken
		}
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, ErrNoToken
	}
	return &t, nil
}

func (s *FileTokenStore) Save(t *Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically via a temp file so a crash can't leave a partial token.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FileTokenStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
