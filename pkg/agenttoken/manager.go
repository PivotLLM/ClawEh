// ClawEh
// License: MIT
//
// Package agenttoken issues per-agent identity tokens used to scope
// mcp__claw__* tool calls to a specific agent's workspace. Tokens are
// generated at claw startup, kept in memory only, and regenerated on
// every restart.
//
// Tokens follow the format `AGT` + 64 hex characters (32 random bytes),
// giving 67 characters total. The literal `AGT` prefix lets the gateway
// redact tokens trivially via regex `AGT[0-9a-f]{64}`.
package agenttoken

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
)

// Prefix is the magic literal at the start of every agent token. It is
// chosen so a simple regex can find and redact tokens in any text.
const Prefix = "AGT"

// HexLen is the number of hex characters that follow the prefix.
const HexLen = 64

// TokenLen is the total length of a valid agent token (Prefix + HexLen).
const TokenLen = len(Prefix) + HexLen

// SubagentSentinel is the reserved token value used by sub-agents that
// should NOT be granted claw MCP access. It is not secret.
const SubagentSentinel = Prefix + "0000000000000000000000000000000000000000000000000000000000000000"

// tokenRegex matches a syntactically valid agent token.
var tokenRegex = regexp.MustCompile(`^AGT[0-9a-f]{64}$`)

// Manager tracks agent → token bindings. Tokens are stored only in memory.
type Manager struct {
	mu     sync.RWMutex
	byName map[string]string // agent name → token
	byTok  map[string]string // token → agent name
}

// NewManager returns an empty token manager.
func NewManager() *Manager {
	return &Manager{
		byName: make(map[string]string),
		byTok:  make(map[string]string),
	}
}

// Issue generates and registers a fresh token for the named agent. If the
// agent already has a token, the existing one is revoked first.
func (m *Manager) Issue(agentName string) string {
	if agentName == "" {
		return ""
	}

	tok, err := generateToken()
	if err != nil {
		// crypto/rand failures are catastrophic; return empty so callers
		// can fail closed rather than silently bind to a predictable value.
		return ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if old, ok := m.byName[agentName]; ok {
		delete(m.byTok, old)
	}
	m.byName[agentName] = tok
	m.byTok[tok] = agentName
	return tok
}

// Resolve returns the agent name bound to the given token. It returns
// ("", false) for empty, malformed, unknown, or sentinel tokens.
func (m *Manager) Resolve(token string) (string, bool) {
	if token == "" || token == SubagentSentinel || !IsValidFormat(token) {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	name, ok := m.byTok[token]
	return name, ok
}

// IsSubagentSentinel reports whether the token is the reserved sub-agent
// sentinel value.
func IsSubagentSentinel(token string) bool {
	return token == SubagentSentinel
}

// IsValidFormat reports whether the token matches the agent-token regex.
// The sentinel value also matches this format.
func IsValidFormat(token string) bool {
	if len(token) != TokenLen {
		return false
	}
	return tokenRegex.MatchString(token)
}

// Revoke clears the token for the named agent.
func (m *Manager) Revoke(agentName string) {
	if agentName == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if tok, ok := m.byName[agentName]; ok {
		delete(m.byTok, tok)
		delete(m.byName, agentName)
	}
}

// RevokeAll clears every issued token.
func (m *Manager) RevokeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byName = make(map[string]string)
	m.byTok = make(map[string]string)
}

// TokenFor returns the current token for the named agent, or "" if none.
func (m *Manager) TokenFor(agentName string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byName[agentName]
}

func generateToken() (string, error) {
	raw := make([]byte, HexLen/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("agenttoken: rand: %w", err)
	}
	return Prefix + hex.EncodeToString(raw), nil
}
