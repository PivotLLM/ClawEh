// ClawEh
// License: MIT

package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// sessionTokenPrefix is the magic literal at the start of every session token.
// Chosen to be distinct from the agent token prefix (AGT) and redactable by regex.
const sessionTokenPrefix = "SST"

// sessionTokenParam is the snake_case parameter name added to session-scoped tool
// schemas. The MCP server strips it before dispatching to the tool implementation.
const sessionTokenParam = "session_token"

// invalidSessionTokenMessage is returned when the session_token is missing,
// malformed, or unknown. The wording mirrors invalidTokenMessage so a confused
// LLM can self-correct.
const invalidSessionTokenMessage = "invalid or missing session_token; supply your assigned token (format: SST<64 hex>)"

// sessionTokenCrossAgentMessage is returned when the session_token resolves to a
// different agent than the caller's agent_token.
const sessionTokenCrossAgentMessage = "session_token does not belong to the calling agent"

// sessionRecord holds the mapping from a session token to its session.
type sessionRecord struct {
	agentID     string
	sessionKey  string
	archiveDir  string
	isTestToken bool // true for tokens registered via Register(); never rotated by Issue()
}

// sessionTokenStore maps SST<64hex> tokens to session records.
// Tokens are generated on first session use and rotated on clear.
//
// Session-scoped tools (get_session_messages, search_session_messages) require a
// session_token so the MCP server can inject the correct session key into the
// tool's execution context regardless of which HTTP request carries the call.
type sessionTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]sessionRecord // token → record
	bySess map[string]string        // sessionKey → token (for revocation by session key)
}

func newSessionTokenStore() *sessionTokenStore {
	return &sessionTokenStore{
		tokens: make(map[string]sessionRecord),
		bySess: make(map[string]string),
	}
}

// Issue generates a new token for the given session and stores the mapping.
// If a token already exists for this sessionKey, it is revoked first.
// Returns the new SST<64hex> token.
func (s *sessionTokenStore) Issue(agentID, sessionKey, archiveDir string) string {
	tok, err := generateSessionToken()
	if err != nil {
		// crypto/rand failure is catastrophic; return empty so callers can fail
		// closed rather than binding to a predictable value.
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If a test token is already registered for this session key, preserve it —
	// test tokens are pinned for the lifetime of the test run and must not be
	// rotated by normal session activity. Return the existing token so the caller
	// (getContextManager) can store it in the system prompt if needed.
	if old, ok := s.bySess[sessionKey]; ok {
		if s.tokens[old].isTestToken {
			return old
		}
		delete(s.tokens, old)
	}

	rec := sessionRecord{agentID: agentID, sessionKey: sessionKey, archiveDir: archiveDir}
	s.tokens[tok] = rec
	s.bySess[sessionKey] = tok
	return tok
}

// Register stores a pre-specified token → session mapping. Unlike Issue,
// it does not generate a new token — the caller supplies the exact token
// string. If a token already exists for this sessionKey, it is revoked first.
// Intended for test setups where a known token must be registered before
// any LLM session has started.
func (s *sessionTokenStore) Register(token, agentID, sessionKey, archiveDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Revoke any existing token for this session key.
	if old, ok := s.bySess[sessionKey]; ok {
		delete(s.tokens, old)
	}

	rec := sessionRecord{agentID: agentID, sessionKey: sessionKey, archiveDir: archiveDir, isTestToken: true}
	s.tokens[token] = rec
	s.bySess[sessionKey] = token
}

// Resolve looks up a token. Returns the record and true if found.
func (s *sessionTokenStore) Resolve(token string) (sessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.tokens[token]
	return rec, ok
}

// Revoke removes the token for a given session key (called on clear/eviction).
func (s *sessionTokenStore) Revoke(sessionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok, ok := s.bySess[sessionKey]; ok {
		delete(s.tokens, tok)
		delete(s.bySess, sessionKey)
	}
}

// RevokeAgent removes all tokens for a given agent (called on agent removal).
func (s *sessionTokenStore) RevokeAgent(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, rec := range s.tokens {
		if rec.agentID == agentID {
			delete(s.bySess, rec.sessionKey)
			delete(s.tokens, tok)
		}
	}
}

// generateSessionToken returns "SST" + 64 lowercase hex characters (32 random bytes).
func generateSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("sessiontoken: crypto/rand read: %w", err)
	}
	return sessionTokenPrefix + hex.EncodeToString(raw), nil
}

