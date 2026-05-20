// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// --- sessionTokenStore unit tests ---

func TestSessionTokenStore_IssueProducesUniqueSSTTokens(t *testing.T) {
	s := newSessionTokenStore()

	tok1 := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	tok2 := s.Issue("bob", "agent:bob:main", "/ws/bob/sessions")

	if tok1 == "" {
		t.Fatal("expected non-empty token for alice")
	}
	if tok2 == "" {
		t.Fatal("expected non-empty token for bob")
	}
	if tok1 == tok2 {
		t.Errorf("expected unique tokens, both equal: %s", tok1)
	}
	if !strings.HasPrefix(tok1, sessionTokenPrefix) {
		t.Errorf("token %q must start with %q", tok1, sessionTokenPrefix)
	}
	if !strings.HasPrefix(tok2, sessionTokenPrefix) {
		t.Errorf("token %q must start with %q", tok2, sessionTokenPrefix)
	}
	// Prefix (3 chars) + 64 hex chars = 67 total
	if len(tok1) != len(sessionTokenPrefix)+64 {
		t.Errorf("token %q: expected length %d, got %d", tok1, len(sessionTokenPrefix)+64, len(tok1))
	}
}

func TestSessionTokenStore_ReissueRotatesToken(t *testing.T) {
	s := newSessionTokenStore()

	tok1 := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	tok2 := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	if tok1 == tok2 {
		t.Errorf("re-issue should produce a different token, both: %s", tok1)
	}

	// Old token must no longer resolve.
	if _, ok := s.Resolve(tok1); ok {
		t.Error("old token should not resolve after re-issue")
	}

	// New token must resolve.
	rec, ok := s.Resolve(tok2)
	if !ok {
		t.Fatal("new token should resolve")
	}
	if rec.agentID != "alice" || rec.sessionKey != "agent:alice:main" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

func TestSessionTokenStore_ResolveReturnsCorrectRecord(t *testing.T) {
	s := newSessionTokenStore()

	tok := s.Issue("alice", "agent:alice:main", "/archive/alice")

	rec, ok := s.Resolve(tok)
	if !ok {
		t.Fatal("expected resolve to succeed")
	}
	if rec.agentID != "alice" {
		t.Errorf("expected agentID=alice, got %q", rec.agentID)
	}
	if rec.sessionKey != "agent:alice:main" {
		t.Errorf("expected sessionKey=agent:alice:main, got %q", rec.sessionKey)
	}
	if rec.archiveDir != "/archive/alice" {
		t.Errorf("expected archiveDir=/archive/alice, got %q", rec.archiveDir)
	}
}

func TestSessionTokenStore_ResolveUnknownReturnsFalse(t *testing.T) {
	s := newSessionTokenStore()

	if _, ok := s.Resolve("SSTnotarealtoken"); ok {
		t.Error("unknown token should not resolve")
	}
	if _, ok := s.Resolve(""); ok {
		t.Error("empty token should not resolve")
	}
}

func TestSessionTokenStore_RevokeRemovesToken(t *testing.T) {
	s := newSessionTokenStore()

	tok := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	s.Revoke("agent:alice:main")

	if _, ok := s.Resolve(tok); ok {
		t.Error("token should not resolve after Revoke")
	}
}

func TestSessionTokenStore_RevokeNonexistentIsNoop(t *testing.T) {
	s := newSessionTokenStore()
	// Should not panic or error.
	s.Revoke("no-such-session")
}

func TestSessionTokenStore_RevokeAgentRemovesAllTokensForAgent(t *testing.T) {
	s := newSessionTokenStore()

	aliceTok1 := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	aliceTok2 := s.Issue("alice", "agent:alice:secondary", "/ws/alice/sessions")
	bobTok := s.Issue("bob", "agent:bob:main", "/ws/bob/sessions")

	s.RevokeAgent("alice")

	if _, ok := s.Resolve(aliceTok1); ok {
		t.Error("alice token 1 should not resolve after RevokeAgent")
	}
	if _, ok := s.Resolve(aliceTok2); ok {
		t.Error("alice token 2 should not resolve after RevokeAgent")
	}
	if _, ok := s.Resolve(bobTok); !ok {
		t.Error("bob token should still resolve after alice's RevokeAgent")
	}
}

// --- dispatchToolCall session token tests ---

// sessionToolMockWithCtx is a mock tool that captures the context passed to Execute.
type sessionToolMock struct {
	mockTool
	capturedCtx context.Context
}

func (m *sessionToolMock) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	m.capturedCtx = ctx
	m.calls++
	m.gotArg = args
	return m.result
}

func TestDispatch_SessionScopedToolValidToken(t *testing.T) {
	st := newSessionTokenStore()
	tm := agenttoken.NewManager()
	agentTok := tm.Issue("alice")
	sessTok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	sessionTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("messages"),
		},
	}
	reg := newRegistryWith(sessionTool)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{
			"agent_token":   agentTok,
			"session_token": sessTok,
			"seq":           float64(1),
		},
		tm, st, resolverFor(regs), nil, nil)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if sessionTool.calls != 1 {
		t.Fatalf("expected tool to be called once, got %d", sessionTool.calls)
	}

	// session_token must be stripped from args before reaching the tool.
	if _, present := sessionTool.gotArg["session_token"]; present {
		t.Error("session_token leaked through to the tool's args")
	}
	// agent_token must be stripped from args before reaching the tool.
	if _, present := sessionTool.gotArg["agent_token"]; present {
		t.Error("agent_token leaked through to the tool's args")
	}

	// The tool's execution context must carry the session key.
	if got := tools.ToolSessionKey(sessionTool.capturedCtx); got != "agent:alice:main" {
		t.Errorf("expected session key %q in ctx, got %q", "agent:alice:main", got)
	}
}

func TestDispatch_SessionScopedToolMissingToken(t *testing.T) {
	st := newSessionTokenStore()
	tm := agenttoken.NewManager()
	agentTok := tm.Issue("alice")

	sessionTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("messages"),
		},
	}
	reg := newRegistryWith(sessionTool)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"agent_token": agentTok},
		tm, st, resolverFor(regs), nil, nil)
	if !isErr {
		t.Fatalf("expected rejection for missing session_token, got: %s", out)
	}
	if out != invalidSessionTokenMessage {
		t.Errorf("expected %q, got: %s", invalidSessionTokenMessage, out)
	}
	if sessionTool.calls != 0 {
		t.Errorf("tool must not execute when session_token is missing, calls=%d", sessionTool.calls)
	}
}

func TestDispatch_SessionScopedToolInvalidToken(t *testing.T) {
	st := newSessionTokenStore()
	tm := agenttoken.NewManager()
	agentTok := tm.Issue("alice")

	sessionTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("messages"),
		},
	}
	reg := newRegistryWith(sessionTool)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	bogus := sessionTokenPrefix + strings.Repeat("a", 64)
	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"agent_token": agentTok, "session_token": bogus},
		tm, st, resolverFor(regs), nil, nil)
	if !isErr {
		t.Fatalf("expected rejection for invalid session_token, got: %s", out)
	}
	if out != invalidSessionTokenMessage {
		t.Errorf("expected %q, got: %s", invalidSessionTokenMessage, out)
	}
}

func TestDispatch_SessionScopedToolCrossAgentRejected(t *testing.T) {
	st := newSessionTokenStore()
	tm := agenttoken.NewManager()
	aliceTok := tm.Issue("alice")
	bobTok := tm.Issue("bob")

	// Issue a session token for bob.
	bobSessTok := st.Issue("bob", "agent:bob:main", "/ws/bob/sessions")

	sessionTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("messages"),
		},
	}
	reg := newRegistryWith(sessionTool)
	regs := map[string]*tools.ToolRegistry{
		"alice": reg,
		"bob":   reg,
	}

	// Alice uses bob's session token — must be rejected.
	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"agent_token": aliceTok, "session_token": bobSessTok},
		tm, st, resolverFor(regs), nil, nil)
	if !isErr {
		t.Fatalf("expected cross-agent rejection, got: %s", out)
	}
	if out != sessionTokenCrossAgentMessage {
		t.Errorf("expected %q, got: %s", sessionTokenCrossAgentMessage, out)
	}
	if sessionTool.calls != 0 {
		t.Errorf("tool must not execute on cross-agent token, calls=%d", sessionTool.calls)
	}

	// Bob using his own token must succeed.
	st.Issue("bob", "agent:bob:main", "/ws/bob/sessions") // re-issue for bob
	bobSessTok2 := st.Issue("bob", "agent:bob:main", "/ws/bob/sessions")
	out, isErr = dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"agent_token": bobTok, "session_token": bobSessTok2},
		tm, st, resolverFor(regs), nil, nil)
	if isErr {
		t.Fatalf("expected success for bob with own token, got: %s", out)
	}
	_ = bobTok // used in the call above
}

func TestDispatch_NonSessionScopedToolNoSessionTokenNeeded(t *testing.T) {
	st := newSessionTokenStore()
	tm := agenttoken.NewManager()
	agentTok := tm.Issue("alice")

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("content")}
	reg := newRegistryWith(rf)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	// No session_token supplied — non-session-scoped tool should succeed.
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": agentTok, "path": "file.txt"},
		tm, st, resolverFor(regs), nil, nil)
	if isErr {
		t.Fatalf("expected success for non-session-scoped tool, got error: %s", out)
	}
	if rf.calls != 1 {
		t.Errorf("expected read_file to be called once, got %d", rf.calls)
	}
}
