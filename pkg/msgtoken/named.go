// ClawEh
// License: MIT

package msgtoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
)

// namedFileName is the state file under the data dir's state/ directory that
// holds the long-lived named message-API tokens for every agent.
const namedFileName = "message-api-tokens.json"

// NamedTokenPath returns the absolute path to the named-token state file for the
// given data directory (e.g. $CLAW_HOME or ~/.claw). It mirrors servicetoken.Path
// so all long-lived token state lives together under state/.
func NamedTokenPath(dataDir string) string {
	return filepath.Join(dataDir, "state", namedFileName)
}

// NamedToken is a single long-lived, user-named message-API token. Unlike the
// rotating Manager tokens these never expire and are shown in plaintext in the
// Web UI, so external apps (GPS trackers, alarms, monitors, CI) can POST to an
// agent over a stable URL. ID is a short random handle used only for deletion;
// Token is the actual secret that authenticates the request.
type NamedToken struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	CreatedAtMS int64  `json:"created_at_ms"`
}

// NamedStore is a thread-safe, file-backed store of named tokens keyed by agent
// id. It is deliberately independent of the rotating Manager and its window
// settings: a named token lives until it is explicitly revoked. The on-disk file
// is the single source of truth; every mutation rewrites it atomically (0600) so
// the gateway (which validates) and the WebUI API (which mints/revokes) can share
// one store instance and stay consistent across a config reload.
type NamedStore struct {
	path string
	mu   sync.RWMutex
	// tokens maps agentID -> its named tokens. Loaded once at construction and
	// kept in memory; every write persists the whole map before returning.
	tokens map[string][]NamedToken
}

// NewNamedStore loads (or initializes) the named-token store at path. A missing
// file is not an error — it starts empty. A corrupt file is treated as empty so a
// bad state file never bricks the gateway; the next write overwrites it.
func NewNamedStore(path string) (*NamedStore, error) {
	s := &NamedStore{path: path, tokens: map[string][]NamedToken{}}
	// An empty path means "no persistence" — an in-memory-only store used as a
	// safe fallback when the real state file cannot be loaded.
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("msgtoken: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.tokens); err != nil || s.tokens == nil {
		s.tokens = map[string][]NamedToken{}
	}
	return s, nil
}

// generateNamedToken returns a fresh strong secret: 64 lowercase hex characters
// (32 random bytes). It reuses the same entropy strength as the rotating tokens.
func generateNamedToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("msgtoken: crypto/rand read: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// generateID returns a short random handle (8 hex chars) used to reference a
// token for deletion. It is not a secret.
func generateID() (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("msgtoken: crypto/rand read: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// List returns a copy of the named tokens for an agent (never nil). The slice is
// cloned so callers cannot mutate the store's internal state.
func (s *NamedStore) List(agentID string) []NamedToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.tokens[agentID]
	out := make([]NamedToken, len(src))
	copy(out, src)
	return out
}

// Create mints a new named token for an agent, persists the store, and returns
// the created token (including its plaintext secret). name may be blank.
func (s *NamedStore) Create(agentID, name string) (NamedToken, error) {
	token, err := generateNamedToken()
	if err != nil {
		return NamedToken{}, err
	}
	id, err := generateID()
	if err != nil {
		return NamedToken{}, err
	}
	nt := NamedToken{
		ID:          id,
		Name:        name,
		Token:       token,
		CreatedAtMS: time.Now().UnixMilli(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[agentID] = append(s.tokens[agentID], nt)
	if err := s.saveLocked(); err != nil {
		// Roll back the in-memory append so the store matches disk on failure.
		list := s.tokens[agentID]
		s.tokens[agentID] = list[:len(list)-1]
		return NamedToken{}, err
	}
	return nt, nil
}

// Delete removes the token with the given id from an agent and persists the
// store. It returns true if a token was removed, false if no such token existed.
func (s *NamedStore) Delete(agentID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.tokens[agentID]
	idx := -1
	for i := range list {
		if list[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return false
	}
	s.tokens[agentID] = append(list[:idx], list[idx+1:]...)
	// Persist; on write failure the in-memory removal still stands but the next
	// successful write reconciles disk. A failed revoke that keeps validating is
	// the unsafe direction, so we log-and-continue rather than resurrect it.
	_ = s.saveLocked()
	return true
}

// Validate resolves a token to its owning agent using a constant-time compare so
// the endpoint does not leak token bytes via timing. Returns ("", false) when no
// token matches.
func (s *NamedStore) Validate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	tokenBytes := []byte(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for agentID, list := range s.tokens {
		for i := range list {
			if subtle.ConstantTimeCompare([]byte(list[i].Token), tokenBytes) == 1 {
				return agentID, true
			}
		}
	}
	return "", false
}

// saveLocked writes the whole token map atomically (0600), creating the parent
// state/ directory if needed. An empty path means an in-memory-only fallback
// store, so persistence is skipped. Must be called with s.mu held.
func (s *NamedStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("msgtoken: mkdir %s: %w", filepath.Dir(s.path), err)
	}
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("msgtoken: marshal: %w", err)
	}
	if err := fileutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("msgtoken: write %s: %w", s.path, err)
	}
	return nil
}
