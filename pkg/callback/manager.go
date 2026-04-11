package callback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// Token represents a single rotating callback token with an expiry time.
type Token struct {
	Value     string `json:"value"`
	ExpiresAt int64  `json:"expires_at"` // unix timestamp
}

// Store is the on-disk representation of the token state.
type Store struct {
	Tokens         []Token `json:"tokens"`
	NextRotationAt int64   `json:"next_rotation_at"` // unix timestamp
}

// Manager manages rotating callback tokens for a single agent.
// A nil Manager indicates that callbacks are disabled for that agent.
type Manager struct {
	agentID       string
	windowMinutes int
	windowCount   int
	storePath     string
	store         Store
	mu            sync.Mutex
	stopCh        chan struct{}
	done          chan struct{}
}

// NewManager creates or loads a callback Manager for the given agent.
//
// If windowMinutes == 0, callbacks are disabled: any existing store file at
// storePath is removed and (nil, nil) is returned.
//
// If windowMinutes > 0, an existing store is loaded, stale tokens are pruned,
// rotation is triggered if due, and a background goroutine is started to
// handle future rotations.
func NewManager(agentID, storePath string, windowMinutes, windowCount int) (*Manager, error) {
	if windowMinutes == 0 {
		// Disabled: clean up any leftover store file.
		if _, err := os.Stat(storePath); err == nil {
			if err := os.Remove(storePath); err != nil {
				logger.WarnCF("callback", "Failed to remove disabled callback store",
					map[string]any{"agent": agentID, "path": storePath, "error": err.Error()})
			}
		}
		return nil, nil
	}

	m := &Manager{
		agentID:       agentID,
		windowMinutes: windowMinutes,
		windowCount:   windowCount,
		storePath:     storePath,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}

	// Load existing store if present.
	if data, err := os.ReadFile(storePath); err == nil {
		if err := json.Unmarshal(data, &m.store); err != nil {
			logger.WarnCF("callback", "Failed to parse callback store, starting fresh",
				map[string]any{"agent": agentID, "error": err.Error()})
			m.store = Store{}
		}
	}

	// Prune expired tokens.
	m.pruneExpired()

	// Rotate immediately if due or if no valid tokens exist.
	now := time.Now().Unix()
	if m.store.NextRotationAt <= now || len(m.store.Tokens) == 0 {
		m.rotate()
	}

	if err := m.save(); err != nil {
		return nil, err
	}

	// Background rotation goroutine.
	go func() {
		defer close(m.done)
		for {
			m.mu.Lock()
			sleepUntil := time.Unix(m.store.NextRotationAt, 0)
			m.mu.Unlock()

			delay := time.Until(sleepUntil)
			if delay < 0 {
				delay = 0
			}

			select {
			case <-m.stopCh:
				return
			case <-time.After(delay):
				m.mu.Lock()
				m.rotate()
				m.save() //nolint:errcheck — logged inside save
				m.mu.Unlock()
			}
		}
	}()

	return m, nil
}

// rotate generates a new token, appends it, prunes expired tokens, and
// advances NextRotationAt. Must be called with m.mu held.
func (m *Manager) rotate() {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		logger.WarnCF("callback", "Failed to generate token",
			map[string]any{"agent": m.agentID, "error": err.Error()})
		return
	}
	value := hex.EncodeToString(raw)

	expiry := time.Now().Unix() + int64(m.windowMinutes*m.windowCount*60)
	m.store.Tokens = append(m.store.Tokens, Token{
		Value:     value,
		ExpiresAt: expiry,
	})

	m.pruneExpired()

	nextRotation := time.Now().Unix() + int64(m.windowMinutes*60)
	m.store.NextRotationAt = nextRotation

	logger.InfoCF("callback", "Token rotated",
		map[string]any{"agent": m.agentID, "next_rotation": nextRotation})
}

// pruneExpired removes tokens that have already expired.
// Must be called with m.mu held (or during construction before the goroutine starts).
func (m *Manager) pruneExpired() {
	now := time.Now().Unix()
	valid := m.store.Tokens[:0]
	for _, t := range m.store.Tokens {
		if t.ExpiresAt > now {
			valid = append(valid, t)
		}
	}
	m.store.Tokens = valid
}

// save writes the store atomically to disk. Must be called with m.mu held.
func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.storePath), 0o700); err != nil {
		logger.WarnCF("callback", "Failed to create callback store directory",
			map[string]any{"agent": m.agentID, "error": err.Error()})
		return err
	}

	data, err := json.Marshal(m.store)
	if err != nil {
		logger.WarnCF("callback", "Failed to marshal callback store",
			map[string]any{"agent": m.agentID, "error": err.Error()})
		return err
	}

	if err := fileutil.WriteFileAtomic(m.storePath, data, 0o600); err != nil {
		logger.WarnCF("callback", "Failed to write callback store",
			map[string]any{"agent": m.agentID, "error": err.Error()})
		return err
	}
	return nil
}

// Validate returns true if token is present in the store and not expired.
func (m *Manager) Validate(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().Unix()
	for _, t := range m.store.Tokens {
		if t.Value == token && t.ExpiresAt > now {
			return true
		}
	}
	return false
}

// CurrentToken returns the most recently generated token, or "" if none exist.
func (m *Manager) CurrentToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.store.Tokens) == 0 {
		return ""
	}
	return m.store.Tokens[len(m.store.Tokens)-1].Value
}

// Stop signals the background rotation goroutine to exit and waits for it.
func (m *Manager) Stop() {
	close(m.stopCh)
	<-m.done
}
