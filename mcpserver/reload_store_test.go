// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// callTool sends a tools/call through the server's internal endpoint (the one
// ClawEh's CLI providers use, session_token in the arguments) and returns the
// text of the result and whether it is an error.
func callTool(t *testing.T, srv *MCPServer, tool, token string) (string, bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": map[string]any{"session_token": token},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(srv.srv.HandleMessage(context.Background(), body))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Error  any `json:"error,omitempty"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode tools/call response: %v\nbody: %s", err, raw)
	}
	if parsed.Error != nil {
		t.Fatalf("tools/call returned a protocol error: %v\nbody: %s", parsed.Error, raw)
	}
	var text strings.Builder
	for _, c := range parsed.Result.Content {
		text.WriteString(c.Text)
	}
	return text.String(), parsed.Result.IsError
}

// rebuild shuts srv down (when non-nil) and builds a new server on the shared
// store, as the gateway does on a config reload.
func rebuild(t *testing.T, srv *MCPServer, store *SessionTokenStore, regs map[string]*tools.ToolRegistry, opts ...Option) *MCPServer {
	t.Helper()
	if srv != nil {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	}
	all := append([]Option{
		WithSessionTokenStore(store),
		WithAgentRegistries(regs),
		WithInternalAllowlist([]string{"*"}),
	}, opts...)
	next, err := New(all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if next.SessionTokens() != store {
		t.Fatal("server did not adopt the shared store")
	}
	return next
}

func aliceRegistry() (map[string]*tools.ToolRegistry, *mockTool) {
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("alice-content")}
	return map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}, rf
}

// TestSharedStore_TokenSurvivesRebuild: a token issued before a config-reload
// rebuild resolves on the new server and dispatches a tool for its agent.
func TestSharedStore_TokenSurvivesRebuild(t *testing.T) {
	store := NewSessionTokenStore()
	tok := store.Issue("alice", "agent:alice:subagent:1", "/tmp/archive/alice")
	regs, rf := aliceRegistry()

	srv1 := rebuild(t, nil, store, regs)
	srv2 := rebuild(t, srv1, store, regs, WithSessionMode("unified"))

	rec, ok := srv2.SessionTokens().Resolve(tok)
	if !ok {
		t.Fatal("token issued before the rebuild no longer resolves")
	}
	if rec.agentID != "alice" || rec.sessionKey != "agent:alice:subagent:1" {
		t.Fatalf("resolved to %s/%s", rec.agentID, rec.sessionKey)
	}
	out, isErr := callTool(t, srv2, "read_file", tok)
	if isErr || out != "alice-content" {
		t.Fatalf("dispatch through the rebuilt server: %q (error=%v)", out, isErr)
	}
	if rf.calls != 1 {
		t.Fatalf("tool ran %d times, want 1", rf.calls)
	}
}

// TestSharedStore_ConsecutiveRebuilds: tokens issued between several rebuilds
// all still resolve at the end; one revoked in between does not.
func TestSharedStore_ConsecutiveRebuilds(t *testing.T) {
	store := NewSessionTokenStore()
	regs, _ := aliceRegistry()

	var srv *MCPServer
	var kept []string
	var revoked string
	for i, key := range []string{"agent:alice:main", "agent:alice:subagent:1", "agent:alice:subagent:2"} {
		srv = rebuild(t, srv, store, regs)
		tok := store.Issue("alice", key, "/tmp/archive/alice")
		if i == 1 {
			revoked = tok
			store.Revoke(key)
			continue
		}
		kept = append(kept, tok)
	}
	srv = rebuild(t, srv, store, regs)

	for _, tok := range kept {
		if _, ok := srv.SessionTokens().Resolve(tok); !ok {
			t.Errorf("token %s… lost across rebuilds", tok[:8])
		}
		if out, isErr := callTool(t, srv, "read_file", tok); isErr {
			t.Errorf("dispatch failed after rebuilds: %s", out)
		}
	}
	if _, ok := srv.SessionTokens().Resolve(revoked); ok {
		t.Error("a token revoked between rebuilds resolves again")
	}
}

// TestSharedStore_SessionModeOptionOrder: the session mode reaches the shared
// store whichever order the options come in, and a server without the option
// gets a fresh store in the unified default.
func TestSharedStore_SessionModeOptionOrder(t *testing.T) {
	regs, _ := aliceRegistry()
	orders := map[string]func(*SessionTokenStore) []Option{
		"mode first": func(s *SessionTokenStore) []Option {
			return []Option{WithSessionMode(string(routing.SessionScopePerUser)), WithSessionTokenStore(s), WithAgentRegistries(regs)}
		},
		"store first": func(s *SessionTokenStore) []Option {
			return []Option{WithSessionTokenStore(s), WithSessionMode(string(routing.SessionScopePerUser)), WithAgentRegistries(regs)}
		},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			store := NewSessionTokenStore()
			srv, err := New(opts(store)...)
			if err != nil {
				t.Fatal(err)
			}
			if srv.SessionTokens() != store {
				t.Fatal("server did not adopt the store")
			}
			want := routing.ResolveServiceSessionKey(routing.SessionScopePerUser, "alice")
			if want == routing.ResolveServiceSessionKey(routing.SessionScopeUnified, "alice") {
				t.Fatal("precondition: per-user and unified must bind different service sessions")
			}
			if got := store.serviceSessionKey("alice"); got != want {
				t.Fatalf("service session = %q, want %q (per-user)", got, want)
			}
		})
	}

	t.Run("no store option", func(t *testing.T) {
		srv, err := New(WithAgentRegistries(regs))
		if err != nil {
			t.Fatal(err)
		}
		if srv.SessionTokens() == nil {
			t.Fatal("server without the option must get a store of its own")
		}
		want := routing.ResolveServiceSessionKey(routing.SessionScopeUnified, "alice")
		if got := srv.SessionTokens().serviceSessionKey("alice"); got != want {
			t.Fatalf("service session = %q, want %q (unified)", got, want)
		}
	})
}

// TestSharedStore_ServiceTokensAcrossRebuild: after a rebuild, the service-token
// re-sync replaces the service set without touching conversation tokens,
// Revoke removes only the conversation token, and a registered (pinned) test
// token survives.
func TestSharedStore_ServiceTokensAcrossRebuild(t *testing.T) {
	store := NewSessionTokenStore()
	regs := map[string]*tools.ToolRegistry{
		"alice": newRegistryWith(&mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("a")}),
		"bob":   newRegistryWith(&mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("b")}),
	}
	archive := func(id string) string { return "/tmp/archive/" + id }

	srv := rebuild(t, nil, store, regs)
	const aliceKey = "agent:alice:main"
	convTok := store.Issue("alice", aliceKey, archive("alice"))
	store.SyncServiceTokens(map[string]string{"alice": "SVC-alice"}, archive)
	store.Register("TEST-pinned", "alice", "test-session", archive("alice"))

	srv = rebuild(t, srv, store, regs)
	store.SyncServiceTokens(map[string]string{"bob": "SVC-bob"}, archive)

	if _, ok := srv.SessionTokens().Resolve(convTok); !ok {
		t.Error("conversation token lost by the service-token re-sync")
	}
	if _, ok := srv.SessionTokens().Resolve("SVC-alice"); ok {
		t.Error("removed service token still resolves")
	}
	if rec, ok := srv.SessionTokens().Resolve("SVC-bob"); !ok || rec.agentID != "bob" {
		t.Error("new service token does not resolve to bob")
	}
	if _, ok := srv.SessionTokens().Resolve("TEST-pinned"); !ok {
		t.Error("registered test token lost across the rebuild")
	}

	store.Revoke(aliceKey)
	if _, ok := srv.SessionTokens().Resolve(convTok); ok {
		t.Error("Revoke left the conversation token")
	}
	if _, ok := srv.SessionTokens().Resolve("SVC-bob"); !ok {
		t.Error("Revoke of a conversation took a service token with it")
	}
}

// TestSharedStore_AgentGoneAfterRebuildFailsClosed: a carried token whose agent
// has no registry on the rebuilt server is refused, not a panic.
func TestSharedStore_AgentGoneAfterRebuildFailsClosed(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	store := NewSessionTokenStore()
	tok := store.Issue("carol", "agent:carol:main", "/tmp/archive/carol")
	regs, rf := aliceRegistry()
	carolRegs := map[string]*tools.ToolRegistry{
		"alice": regs["alice"],
		"carol": newRegistryWith(&mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("c")}),
	}

	srv := rebuild(t, nil, store, carolRegs)
	srv = rebuild(t, srv, store, regs) // carol removed by the reload

	out, isErr := callTool(t, srv, "read_file", tok)
	if !isErr {
		t.Fatalf("a token for a removed agent dispatched: %q", out)
	}
	if rf.calls != 0 {
		t.Fatal("the call reached another agent's registry")
	}
	if !strings.Contains(buf.String(), `"reason":"no_registry"`) {
		t.Errorf("expected a no_registry rejection, got:\n%s", buf.String())
	}
}
