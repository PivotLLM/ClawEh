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

// DefaultRatePerMin and DefaultBlockMinutes are the per-token rate-limit
// defaults applied when a token's own values are 0 (the zero value, which also
// covers tokens minted before rate limiting existed). See EffectiveRatePerMin /
// EffectiveBlockMinutes.
const (
	DefaultRatePerMin   = 30
	DefaultBlockMinutes = 15
)

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
	// RatePerMin / BlockMinutes are the per-token rate-limit config. 0 means
	// "use the default" (see Effective* below), so existing tokens and blank
	// WebUI fields keep working. omitempty keeps the state file tidy.
	RatePerMin   int `json:"rate_per_min,omitempty"`
	BlockMinutes int `json:"block_minutes,omitempty"`
}

// EffectiveRatePerMin returns the token's configured requests-per-minute limit,
// falling back to DefaultRatePerMin when unset (0).
func (t NamedToken) EffectiveRatePerMin() int {
	if t.RatePerMin <= 0 {
		return DefaultRatePerMin
	}
	return t.RatePerMin
}

// EffectiveBlockMinutes returns the token's configured block duration in
// minutes, falling back to DefaultBlockMinutes when unset (0).
func (t NamedToken) EffectiveBlockMinutes() int {
	if t.BlockMinutes <= 0 {
		return DefaultBlockMinutes
	}
	return t.BlockMinutes
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

	// Rate-limiter state is process-local and NOT persisted: a restart clearing
	// a block is harmless. limitMu guards limits only; it is a separate mutex
	// from mu so the limiter never blocks token reads/writes.
	//
	// LOCK ORDER: to avoid deadlock, code that needs both reads token config
	// under mu.RLock, RELEASES mu, then takes limitMu. Never hold both at once.
	limitMu sync.Mutex
	// limits maps tokenID -> its sliding-window/block counters.
	limits map[string]*tokenLimit
	// now is the clock, injectable so tests can advance time deterministically.
	now func() time.Time
}

// tokenLimit is one token's in-memory limiter state. hits is a sliding-window
// log of request timestamps (unix-nano); blockedUntil is the unix-nano instant
// a block expires (0 = not blocked).
type tokenLimit struct {
	hits         []int64
	blockedUntil int64
}

// NewNamedStore loads (or initializes) the named-token store at path. A missing
// file is not an error — it starts empty. A corrupt file is treated as empty so a
// bad state file never bricks the gateway; the next write overwrites it.
func NewNamedStore(path string) (*NamedStore, error) {
	s := &NamedStore{
		path:   path,
		tokens: map[string][]NamedToken{},
		limits: map[string]*tokenLimit{},
		now:    time.Now,
	}
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
	agentID, _, ok := s.ValidateWithID(token)
	return agentID, ok
}

// ValidateWithID is Validate but also returns the matched token's stable ID so
// the caller can key the per-token rate limiter on it. Constant-time compare so
// the endpoint does not leak token bytes via timing.
func (s *NamedStore) ValidateWithID(token string) (agentID, tokenID string, ok bool) {
	if token == "" {
		return "", "", false
	}
	tokenBytes := []byte(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for aid, list := range s.tokens {
		for i := range list {
			if subtle.ConstantTimeCompare([]byte(list[i].Token), tokenBytes) == 1 {
				return aid, list[i].ID, true
			}
		}
	}
	return "", "", false
}

// Allow records a request against the per-token sliding-window limiter and
// reports whether it is permitted. On the request that first exceeds the limit
// it sets a fixed block window and rejects; while blocked it keeps rejecting
// with the remaining time WITHOUT extending the block; after the block expires
// the counters reset and requests flow again.
//
// retryAfter is the time until the block clears (only meaningful when
// allowed==false). agentID/tokenID come from ValidateWithID.
func (s *NamedStore) Allow(agentID, tokenID string) (allowed bool, retryAfter time.Duration) {
	rate, blockMin := s.tokenLimitConfig(agentID, tokenID)

	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := s.now()
	nowNS := now.UnixNano()
	tl := s.limits[tokenID]
	if tl == nil {
		tl = &tokenLimit{}
		s.limits[tokenID] = tl
	}

	// Still inside an active block: reject with the remaining time, do NOT
	// extend the window (fixed block, not sliding).
	if tl.blockedUntil > nowNS {
		return false, time.Duration(tl.blockedUntil - nowNS)
	}
	// Block just expired (or never blocked): reset counters and start fresh.
	if tl.blockedUntil != 0 {
		tl.blockedUntil = 0
		tl.hits = tl.hits[:0]
	}

	// Prune hits older than the rolling 60s window.
	cutoff := now.Add(-time.Minute).UnixNano()
	tl.hits = pruneHits(tl.hits, cutoff)

	if len(tl.hits) >= rate {
		// This request exceeds the limit → open a fixed block window.
		tl.blockedUntil = now.Add(time.Duration(blockMin) * time.Minute).UnixNano()
		return false, time.Duration(tl.blockedUntil - nowNS)
	}
	tl.hits = append(tl.hits, nowNS)
	return true, 0
}

// tokenLimitConfig reads a token's effective rate/block config under mu.RLock
// and releases mu BEFORE the caller takes limitMu (see NamedStore lock order).
// Missing token → defaults, so a race that deletes a token mid-flight still
// rate-limits sanely.
func (s *NamedStore) tokenLimitConfig(agentID, tokenID string) (rate, blockMinutes int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.tokens[agentID] {
		if s.tokens[agentID][i].ID == tokenID {
			return s.tokens[agentID][i].EffectiveRatePerMin(), s.tokens[agentID][i].EffectiveBlockMinutes()
		}
	}
	return DefaultRatePerMin, DefaultBlockMinutes
}

// pruneHits drops timestamps <= cutoff from the front of a time-ordered slice.
func pruneHits(hits []int64, cutoff int64) []int64 {
	i := 0
	for i < len(hits) && hits[i] <= cutoff {
		i++
	}
	if i == 0 {
		return hits
	}
	return hits[:copy(hits, hits[i:])]
}

// ResetBlocks clears active blocks for an agent's tokens. An empty name clears
// every token; a non-empty name clears only the token with that name. Returns
// the number of tokens whose block was cleared.
func (s *NamedStore) ResetBlocks(agentID, name string) int {
	// Resolve target token IDs under mu.RLock, release, then touch limitMu.
	s.mu.RLock()
	var ids []string
	for i := range s.tokens[agentID] {
		if name == "" || s.tokens[agentID][i].Name == name {
			ids = append(ids, s.tokens[agentID][i].ID)
		}
	}
	s.mu.RUnlock()

	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := s.now().UnixNano()
	cleared := 0
	for _, id := range ids {
		tl := s.limits[id]
		if tl != nil && tl.blockedUntil > now {
			tl.blockedUntil = 0
			tl.hits = tl.hits[:0]
			cleared++
		}
	}
	return cleared
}

// Update sets a token's rate/block config and persists the store. rate/block of
// 0 mean "use the default" (see Effective*). Returns false when no token with
// the given id exists for the agent.
func (s *NamedStore) Update(agentID, id string, ratePerMin, blockMinutes int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.tokens[agentID]
	for i := range list {
		if list[i].ID == id {
			list[i].RatePerMin = ratePerMin
			list[i].BlockMinutes = blockMinutes
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// TokenQuota is a per-token rate-limit status snapshot surfaced to the WebUI and
// the /quota command.
type TokenQuota struct {
	ID             string
	Name           string
	RatePerMin     int
	BlockMinutes   int
	HitsInWindow   int
	Blocked        bool
	BlockRemaining time.Duration
}

// Quota returns a status snapshot for every named token of an agent, pruning
// each token's rolling window so HitsInWindow is current.
func (s *NamedStore) Quota(agentID string) []TokenQuota {
	// Snapshot token config under mu.RLock, release, then read limiter state.
	s.mu.RLock()
	tokens := make([]NamedToken, len(s.tokens[agentID]))
	copy(tokens, s.tokens[agentID])
	s.mu.RUnlock()

	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := s.now()
	nowNS := now.UnixNano()
	cutoff := now.Add(-time.Minute).UnixNano()

	out := make([]TokenQuota, 0, len(tokens))
	for i := range tokens {
		q := TokenQuota{
			ID:           tokens[i].ID,
			Name:         tokens[i].Name,
			RatePerMin:   tokens[i].EffectiveRatePerMin(),
			BlockMinutes: tokens[i].EffectiveBlockMinutes(),
		}
		if tl := s.limits[tokens[i].ID]; tl != nil {
			if tl.blockedUntil > nowNS {
				q.Blocked = true
				q.BlockRemaining = time.Duration(tl.blockedUntil - nowNS)
			} else {
				tl.hits = pruneHits(tl.hits, cutoff)
				q.HitsInWindow = len(tl.hits)
			}
		}
		out = append(out, q)
	}
	return out
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
