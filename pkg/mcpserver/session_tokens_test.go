// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

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

func TestRegister_PrespecifiedToken(t *testing.T) {
	s := newSessionTokenStore()

	token := sessionTokenPrefix + strings.Repeat("a", 64)

	s.Register(token, "agent1", "sess1", "/tmp/archive")

	rec, ok := s.Resolve(token)
	if !ok {
		t.Fatal("expected registered token to resolve")
	}
	if rec.agentID != "agent1" {
		t.Errorf("expected agentID=agent1, got %q", rec.agentID)
	}
	if rec.sessionKey != "sess1" {
		t.Errorf("expected sessionKey=sess1, got %q", rec.sessionKey)
	}
	if rec.archiveDir != "/tmp/archive" {
		t.Errorf("expected archiveDir=/tmp/archive, got %q", rec.archiveDir)
	}
	if !rec.isTestToken {
		t.Error("expected isTestToken=true on registered token")
	}

	// Register a different token for the same session key — old token must be gone.
	token2 := sessionTokenPrefix + strings.Repeat("b", 64)
	s.Register(token2, "agent1", "sess1", "/tmp/archive")

	if _, ok := s.Resolve(token); ok {
		t.Error("old token should not resolve after re-registration for same session key")
	}

	rec2, ok := s.Resolve(token2)
	if !ok {
		t.Fatal("new token should resolve after re-registration")
	}
	if rec2.agentID != "agent1" || rec2.sessionKey != "sess1" {
		t.Errorf("unexpected record after re-registration: %+v", rec2)
	}
}

func TestIssue_PreservesTestToken(t *testing.T) {
	s := newSessionTokenStore()

	testTok := sessionTokenPrefix + strings.Repeat("c", 64)
	s.Register(testTok, "agent1", "sess1", "/tmp/archive")

	// Issue() for the same session key must NOT revoke the test token.
	returned := s.Issue("agent1", "sess1", "/tmp/archive")

	if returned != testTok {
		t.Errorf("Issue() should return the existing test token, got %q", returned)
	}

	rec, ok := s.Resolve(testTok)
	if !ok {
		t.Fatal("test token must still resolve after Issue() for the same session key")
	}
	if !rec.isTestToken {
		t.Error("isTestToken flag must be preserved")
	}
}

func TestIssue_RotatesNormalToken(t *testing.T) {
	s := newSessionTokenStore()

	// Issue a normal token first.
	first := s.Issue("agent1", "sess2", "/tmp/archive")
	if first == "" {
		t.Fatal("expected non-empty token")
	}

	// Issue again for the same session key — must rotate.
	second := s.Issue("agent1", "sess2", "/tmp/archive")
	if second == first {
		t.Error("Issue() should generate a new token for a normal session")
	}
	if _, ok := s.Resolve(first); ok {
		t.Error("old normal token must not resolve after rotation")
	}
	if _, ok := s.Resolve(second); !ok {
		t.Error("new token must resolve after rotation")
	}
}

// --- dispatchToolCall session token tests ---

// sessionToolMock is a mock tool that captures the context passed to Execute
// and implements SessionScoped so dispatchToolCall injects the session key.
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

func (m *sessionToolMock) IsSessionScoped() bool { return true }

func TestDispatch_SessionScopedToolValidToken(t *testing.T) {
	st := newSessionTokenStore()
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
			"session_token": sessTok,
			"seq":           float64(1),
		},
		st, resolverFor(regs), nil, nil, nil)
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

	// The tool's execution context must carry the session key.
	if got := tools.ToolSessionKey(sessionTool.capturedCtx); got != "agent:alice:main" {
		t.Errorf("expected session key %q in ctx, got %q", "agent:alice:main", got)
	}
}

func TestDispatch_SessionScopedToolMissingToken(t *testing.T) {
	st := newSessionTokenStore()

	sessionTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("messages"),
		},
	}
	reg := newRegistryWith(sessionTool)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	// Missing session_token — dispatch must reject.
	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{},
		st, resolverFor(regs), nil, nil, nil)
	if !isErr {
		t.Fatalf("expected rejection for missing session_token, got: %s", out)
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
	if sessionTool.calls != 0 {
		t.Errorf("tool must not execute when session_token is missing, calls=%d", sessionTool.calls)
	}
}

func TestDispatch_SessionScopedToolInvalidToken(t *testing.T) {
	st := newSessionTokenStore()

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
		map[string]any{"session_token": bogus},
		st, resolverFor(regs), nil, nil, nil)
	if !isErr {
		t.Fatalf("expected rejection for invalid session_token, got: %s", out)
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
}

// TestDispatch_SessionTokenRoutesToCorrectAgent confirms that the session_token
// alone correctly identifies and dispatches to the owning agent's registry.
func TestDispatch_SessionTokenRoutesToCorrectAgent(t *testing.T) {
	st := newSessionTokenStore()
	bobSessTok := st.Issue("bob", "agent:bob:main", "/ws/bob/sessions")
	aliceSessTok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	aliceTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("alice-messages"),
		},
	}
	bobTool := &sessionToolMock{
		mockTool: mockTool{
			name:   "get_session_messages",
			params: map[string]any{},
			result: tools.NewToolResult("bob-messages"),
		},
	}
	regs := map[string]*tools.ToolRegistry{
		"alice": newRegistryWith(aliceTool),
		"bob":   newRegistryWith(bobTool),
	}

	// Bob's token dispatches to bob's registry.
	out, isErr := dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"session_token": bobSessTok},
		st, resolverFor(regs), nil, nil, nil)
	if isErr {
		t.Fatalf("expected success for bob, got: %s", out)
	}
	if bobTool.calls != 1 || aliceTool.calls != 0 {
		t.Errorf("expected bob's registry, got alice=%d bob=%d", aliceTool.calls, bobTool.calls)
	}
	if got := tools.ToolSessionKey(bobTool.capturedCtx); got != "agent:bob:main" {
		t.Errorf("expected bob's session key in ctx, got %q", got)
	}

	// Alice's token dispatches to alice's registry.
	out, isErr = dispatchToolCall(context.Background(), "get_session_messages",
		map[string]any{"session_token": aliceSessTok},
		st, resolverFor(regs), nil, nil, nil)
	if isErr {
		t.Fatalf("expected success for alice, got: %s", out)
	}
	if aliceTool.calls != 1 {
		t.Errorf("alice's registry was not called, calls=%d", aliceTool.calls)
	}
}

// TestDispatch_SessionScopedInterfaceInjectsKey verifies that dispatchToolCall
// injects the session key for every tool that implements tools.SessionScoped,
// regardless of the tool name. This replaces the old hardcoded name check.
func TestDispatch_SessionScopedInterfaceInjectsKey(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	// Use all four real session tool names to confirm interface dispatch works.
	toolNames := []string{
		"get_session_messages",
		"search_session_messages",
		"compact_session",
		"get_session_info",
	}

	for _, name := range toolNames {
		t.Run(name, func(t *testing.T) {
			tool := &sessionToolMock{
				mockTool: mockTool{
					name:   name,
					params: map[string]any{},
					result: tools.NewToolResult("ok"),
				},
			}
			reg := newRegistryWith(tool)
			regs := map[string]*tools.ToolRegistry{"alice": reg}

			out, isErr := dispatchToolCall(context.Background(), name,
				map[string]any{"session_token": tok},
				st, resolverFor(regs), nil, nil, nil)
			if isErr {
				t.Fatalf("expected success, got error: %s", out)
			}
			if tool.calls != 1 {
				t.Fatalf("expected tool to be called once, got %d", tool.calls)
			}
			if got := tools.ToolSessionKey(tool.capturedCtx); got != "agent:alice:main" {
				t.Errorf("%s: expected session key %q in ctx, got %q", name, "agent:alice:main", got)
			}
		})
	}
}

// TestDispatch_NonSessionScopedToolDoesNotGetSessionKey verifies that a tool
// that does not implement tools.SessionScoped does NOT get a session key
// injected into its execution context, even though it receives a valid token.
func TestDispatch_NonSessionScopedToolDoesNotGetSessionKey(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	// nonScopedCaptureMock captures ctx but does NOT implement SessionScoped.
	tool := &nonScopedCaptureMock{
		mockTool: mockTool{
			name:   "read_file",
			params: map[string]any{},
			result: tools.NewToolResult("content"),
		},
	}
	reg := newRegistryWith(tool)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": tok, "path": "x"},
		st, resolverFor(regs), nil, nil, nil)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if tool.calls != 1 {
		t.Fatalf("expected tool to be called once, got %d", tool.calls)
	}
	if got := tools.ToolSessionKey(tool.capturedCtx); got != "" {
		t.Errorf("non-session-scoped tool must not receive session key, got %q", got)
	}
}

// nonScopedCaptureMock is a context-capturing mock that does NOT implement SessionScoped.
type nonScopedCaptureMock struct {
	mockTool
	capturedCtx context.Context
}

func (m *nonScopedCaptureMock) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	m.capturedCtx = ctx
	m.calls++
	m.gotArg = args
	return m.result
}

// TestDispatch_AllToolsRequireSessionToken confirms that even non-session-scoped
// tools now require a valid session_token for dispatch.
func TestDispatch_AllToolsRequireSessionToken(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "test:alice:main", "/tmp/archive/alice")

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("content")}
	reg := newRegistryWith(rf)
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	// With valid session_token — must succeed.
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": tok, "path": "file.txt"},
		st, resolverFor(regs), nil, nil, nil)
	if isErr {
		t.Fatalf("expected success with valid session_token, got error: %s", out)
	}
	if rf.calls != 1 {
		t.Errorf("expected read_file to be called once, got %d", rf.calls)
	}

	// Without session_token — must be rejected.
	out, isErr = dispatchToolCall(context.Background(), "read_file",
		map[string]any{"path": "file.txt"},
		st, resolverFor(regs), nil, nil, nil)
	if !isErr {
		t.Fatalf("expected rejection without session_token, got success: %s", out)
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
	if rf.calls != 1 {
		t.Errorf("tool must not execute on missing token, calls=%d", rf.calls)
	}
}
