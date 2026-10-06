// ClawEh
// License: MIT

package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/PivotLLM/ClawEh/internal/tokenhash"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// sessionTokenPrefix is the magic literal at the start of every session token.
// Chosen to be distinct from the agent token prefix (AGT) and redactable by regex.
const sessionTokenPrefix = "SST"

// sessionTokenParam is the snake_case parameter name added to session-scoped tool
// schemas. The MCP server strips it before dispatching to the tool implementation.
const sessionTokenParam = "session_token"

// sessionRecord holds the mapping from a session token to its session.
//
// channel/chatID are the "most recent inbound source for this session" used by
// MCP-routed tool dispatch to publish a tool's ForUser payload back to the
// originating user. They are populated by the agent loop on every inbound user
// message via SetSource. Empty values mean no user channel is bound — the
// MCP publish step silently drops in that case.
type sessionRecord struct {
	agentID    string
	sessionKey string
	archiveDir string
	channel    string
	chatID     string
	// homeID, when set, is the agent a late async result of this session is
	// delivered to instead of agentID: a temporary clone's source, resolved
	// when the token is issued, so it holds after the clone is deleted.
	homeID string
	// depth is the sub-agent depth of the session's current turn, recorded by
	// the agent loop when the turn starts (SetDepth). Tool calls presented with
	// this token run at it, so a CLI provider's spawns stay within
	// max_subagent_depth. Service tokens have no turn and stay at 0.
	depth int
	// askChain is the ask chain of the session's current turn
	// (SetTurnScope), applied to tool calls presented with this token like
	// depth. Service tokens have none.
	askChain []string
	// pinned marks a token that Issue() must never rotate away: registered test
	// tokens (Register) and long-lived per-agent service tokens (RegisterService).
	pinned bool
	// hashed marks a record keyed by tokenhash.Hash(token) rather than by the
	// token itself: service tokens loaded from disk, where only the hash is
	// kept. Resolve hashes the presented token to reach these, and never lets
	// the stored hash string itself authenticate.
	hashed bool
}

// SessionTokenStore maps SST<64hex> tokens to session records.
// Tokens are generated on first session use and rotated on clear.
//
// Session-scoped tools (get_session_messages, search_session_messages) require a
// session_token so the MCP server can inject the correct session key into the
// tool's execution context regardless of which HTTP request carries the call.
type SessionTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]sessionRecord // token → record
	bySess map[string]string        // conversation sessionKey → token (rotation/revocation)
	// bySvc indexes long-lived service tokens by agent id. They are tracked
	// separately from bySess because a service token resolves to the agent's
	// main session, which already has a conversation token: several tokens may
	// name one session, and rotating the conversation token must not disturb
	// the service token (or vice versa).
	bySvc map[string]string // agentID → service token
	// home maps an agent to the agent its late results go to (a clone's
	// source); nil keeps every agent's own. Set once at startup.
	home func(agentID string) string
}

// SetHomeResolver installs the lookup from an agent to the agent its late
// async results are delivered to (a temporary clone's source). Tokens issued
// afterwards record it.
func (s *SessionTokenStore) SetHomeResolver(fn func(agentID string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.home = fn
}

// NewSessionTokenStore returns an empty store. The gateway creates one for the
// life of the process and hands it to every MCP server it builds (see
// WithSessionTokenStore), so a config reload does not invalidate the tokens
// already rendered into running prompts.
func NewSessionTokenStore() *SessionTokenStore { return newSessionTokenStore() }

func newSessionTokenStore() *SessionTokenStore {
	return &SessionTokenStore{
		tokens: make(map[string]sessionRecord),
		bySess: make(map[string]string),
		bySvc:  make(map[string]string),
	}
}

// Issue generates a new token for the given session and stores the mapping.
// If a token already exists for this sessionKey, it is revoked first.
// Returns the new SST<64hex> token.
func (s *SessionTokenStore) Issue(agentID, sessionKey, archiveDir string) string {
	tok, err := generateSessionToken()
	if err != nil {
		// crypto/rand failure is catastrophic; return empty so callers can fail
		// closed rather than binding to a predictable value.
		return ""
	}

	// Resolve the home agent outside the store's lock: the resolver reads the
	// agent registry.
	s.mu.RLock()
	home := s.home
	s.mu.RUnlock()
	homeID := ""
	if home != nil {
		if h := home(agentID); h != "" && h != agentID {
			homeID = h
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := sessionRecord{agentID: agentID, sessionKey: sessionKey, archiveDir: archiveDir, homeID: homeID}

	// If a pinned token is already registered for this session key, preserve it —
	// pinned tokens (test + service tokens) must not be rotated by normal session
	// activity. Return the existing token so the caller (getContextManager) can
	// store it in the system prompt if needed.
	if old, ok := s.bySess[sessionKey]; ok {
		if s.tokens[old].pinned {
			return old
		}
		// Preserve the last-known inbound source (channel/chatID) across rotation
		// so MCP-routed tools — notably cron_schedule, which delivers to the
		// session's own channel — keep their target. Otherwise a reissue between
		// the inbound SetSource and the tool call would wipe it.
		rec.channel = s.tokens[old].channel
		rec.chatID = s.tokens[old].chatID
		rec.depth = s.tokens[old].depth
		rec.askChain = s.tokens[old].askChain
		delete(s.tokens, old)
	}
	s.tokens[tok] = rec
	s.bySess[sessionKey] = tok
	logger.InfoCF("mcpserver", "session token issued",
		map[string]any{"agent": agentID, "session": sessionKey})
	return tok
}

// Register stores a pre-specified token → session mapping. Unlike Issue,
// it does not generate a new token — the caller supplies the exact token
// string. If a token already exists for this sessionKey, it is revoked first.
// Intended for test setups where a known token must be registered before
// any LLM session has started.
func (s *SessionTokenStore) Register(token, agentID, sessionKey, archiveDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Revoke any existing token for this session key.
	if old, ok := s.bySess[sessionKey]; ok {
		delete(s.tokens, old)
	}

	rec := sessionRecord{agentID: agentID, sessionKey: sessionKey, archiveDir: archiveDir, pinned: true}
	s.tokens[token] = rec
	s.bySess[sessionKey] = token
}

// RegisterService registers a long-lived per-agent service token bound to the
// agent's main session (agent:<id>:main): an agent has one conversation,
// whatever is driving it. Like Register it is pinned (never rotated by Issue)
// and it is indexed apart from the conversation token, so neither disturbs the
// other. The caller supplies the exact token (minted/persisted by the
// `claw token` CLI).
func (s *SessionTokenStore) RegisterService(token, agentID, archiveDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionKey := routing.BuildAgentMainSessionKey(agentID)
	// Replace this agent's previous service token, and only that: the session's
	// conversation token (bySess) is a separate credential and stays put, so
	// registering a service token can never revoke the agent's own token.
	if old, ok := s.bySvc[agentID]; ok {
		delete(s.tokens, old)
	}
	s.tokens[token] = sessionRecord{
		agentID:    agentID,
		sessionKey: sessionKey,
		archiveDir: archiveDir,
		pinned:     true,
	}
	s.bySvc[agentID] = token
	logger.InfoCF("mcpserver", "service token registered",
		map[string]any{"agent": agentID, "session": sessionKey})
}

// SyncServiceTokens reconciles the live store to the given agentID→token set:
// agents present have their service token (re)registered; service tokens for
// agents no longer present are revoked. archiveDirFor maps an agentID to its
// archive dir; agents it returns "" for (unknown) are skipped. This is what lets
// `claw token` changes take effect without a restart. The values are the
// stored form from servicetoken.Load — hashes — so the record is keyed by the
// hash and Resolve hashes the presented token to find it; a plaintext value
// (tests) is keyed as-is.
func (s *SessionTokenStore) SyncServiceTokens(tokens map[string]string, archiveDirFor func(agentID string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Drop every existing service token. They are indexed by agent, so this never
	// touches a conversation token, though both name the agent's main session.
	for agentID, tok := range s.bySvc {
		delete(s.tokens, tok)
		delete(s.bySvc, agentID)
	}
	// Register the current set.
	for agentID, tok := range tokens {
		archiveDir := archiveDirFor(agentID)
		if archiveDir == "" {
			continue
		}
		s.tokens[tok] = sessionRecord{
			agentID:    agentID,
			sessionKey: routing.BuildAgentMainSessionKey(agentID),
			archiveDir: archiveDir,
			pinned:     true,
			hashed:     tokenhash.IsHashed(tok),
		}
		s.bySvc[agentID] = tok
	}
}

// SetSource records the most recent inbound user-message source (channel +
// chatID) on the session record identified by sessionKey. Called from the
// agent loop on every inbound user message so MCP-routed tool dispatch can
// publish a tool's ForUser payload back to the originating user. No-op if
// the sessionKey is unknown — Issue() may not yet have been called for this
// session, which is normal during early startup.
func (s *SessionTokenStore) SetSource(sessionKey, channel, chatID string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.bySess[sessionKey]
	if !ok {
		return
	}
	rec, ok := s.tokens[tok]
	if !ok {
		return
	}
	rec.channel = channel
	rec.chatID = chatID
	s.tokens[tok] = rec
}

// SetDepth records the sub-agent depth of the turn now running on sessionKey
// on its conversation token, so MCP tool calls made with that token (a CLI
// provider's) run at the turn's depth. Called by the agent loop at the start of
// every turn, so a later turn at a lower depth lowers it again. Service tokens
// are indexed apart and never carry a depth. No-op if the session has no
// token.
func (s *SessionTokenStore) SetDepth(sessionKey string, depth int) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.bySess[sessionKey]
	if !ok {
		return
	}
	rec, ok := s.tokens[tok]
	if !ok {
		return
	}
	rec.depth = depth
	s.tokens[tok] = rec
}

// SetTurnScope records the ask chain of the turn now running on sessionKey on
// its conversation token, so MCP tool calls made with it (a CLI provider's)
// carry it as in-process tool calls do: an agent waiting in the exchange
// cannot be asked. Called by the agent loop at the start of every turn.
// No-op if the session has no token.
func (s *SessionTokenStore) SetTurnScope(sessionKey string, askChain []string) {
	if sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.bySess[sessionKey]
	if !ok {
		return
	}
	rec, ok := s.tokens[tok]
	if !ok {
		return
	}
	rec.askChain = slices.Clone(askChain)
	s.tokens[tok] = rec
}

// Source returns the inbound source SetSource last recorded for sessionKey
// (empty when none, or the session has no token).
func (s *SessionTokenStore) Source(sessionKey string) (channel, chatID string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tok, ok := s.bySess[sessionKey]
	if !ok {
		return "", ""
	}
	rec := s.tokens[tok]
	return rec.channel, rec.chatID
}

// Resolve looks up a presented token. Returns the record and true if found.
// A record stored under the token's hash is reached by hashing the presented
// value; presenting the hash string itself matches nothing.
func (s *SessionTokenStore) Resolve(token string) (sessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rec, ok := s.tokens[token]; ok && !rec.hashed {
		return rec, true
	}
	if rec, ok := s.tokens[tokenhash.Hash(token)]; ok && rec.hashed {
		return rec, true
	}
	return sessionRecord{}, false
}

// Revoke removes the token for a given session key (called on clear/eviction).
func (s *SessionTokenStore) Revoke(sessionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok, ok := s.bySess[sessionKey]; ok {
		delete(s.tokens, tok)
		delete(s.bySess, sessionKey)
	}
}

// RevokeAgent removes all tokens for a given agent (called on agent removal).
func (s *SessionTokenStore) RevokeAgent(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, rec := range s.tokens {
		if rec.agentID == agentID {
			delete(s.bySess, rec.sessionKey)
			delete(s.tokens, tok)
		}
	}
	delete(s.bySvc, agentID)
}

// generateSessionToken returns "SST" + 64 lowercase hex characters (32 random bytes).
func generateSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("sessiontoken: crypto/rand read: %w", err)
	}
	return sessionTokenPrefix + hex.EncodeToString(raw), nil
}
