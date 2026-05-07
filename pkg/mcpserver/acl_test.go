// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// TestDispatch_ACLAllowExecutesAndLogsAuthorized confirms the success path
// when the policy returns true: the tool runs, the INFO authorized log
// fires, and no rejection WARN is emitted.
func TestDispatch_ACLAllowExecutesAndLogsAuthorized(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	var seen atomic.Int32
	policy := acl.PolicyFunc(func(agent, tool string) bool {
		if agent == "alice" && tool == "read_file" {
			seen.Add(1)
			return true
		}
		return false
	})

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(regs), nil, policy)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if rf.calls != 1 {
		t.Errorf("expected tool to execute once, got %d", rf.calls)
	}
	if seen.Load() != 1 {
		t.Errorf("expected policy to be consulted exactly once, got %d", seen.Load())
	}

	logs := buf.String()
	if !strings.Contains(logs, `"message":"MCP tool authorized"`) {
		t.Errorf("expected authorized INFO entry in:\n%s", logs)
	}
	if strings.Contains(logs, `"message":"MCP tool denied"`) {
		t.Errorf("did not expect denied WARN on allow path:\n%s", logs)
	}
}

// TestDispatch_ACLDenyBlocksAndEmitsWarn confirms the deny path: the tool
// is NOT executed, a clear JSON-RPC error message is returned, the new
// "MCP tool denied" WARN is logged with reason="acl_denied", and no
// agent token leaks into the log line.
func TestDispatch_ACLDenyBlocksAndEmitsWarn(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	denyAll := acl.PolicyFunc(func(_, _ string) bool { return false })

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(regs), nil, denyAll)
	if !isErr {
		t.Fatalf("expected ACL deny, got success: %s", out)
	}
	if rf.calls != 0 {
		t.Errorf("tool ran despite ACL deny: calls=%d", rf.calls)
	}
	if !strings.Contains(out, "agent not authorized") {
		t.Errorf("unexpected deny message: %s", out)
	}

	logs := buf.String()
	if got := strings.Count(logs, `"level":"warn"`); got != 1 {
		t.Errorf("expected exactly 1 WARN on ACL deny, got %d in:\n%s", got, logs)
	}
	if !strings.Contains(logs, `"message":"MCP tool denied"`) {
		t.Errorf("missing denied WARN in:\n%s", logs)
	}
	if !strings.Contains(logs, `"reason":"acl_denied"`) {
		t.Errorf("missing reason=acl_denied in:\n%s", logs)
	}
	if !strings.Contains(logs, `"agent":"alice"`) {
		t.Errorf("missing agent field in:\n%s", logs)
	}
	if !strings.Contains(logs, `"tool":"read_file"`) {
		t.Errorf("missing tool field in:\n%s", logs)
	}
	if strings.Contains(logs, tok) {
		t.Errorf("token leaked into ACL-denied log:\n%s", logs)
	}
	if strings.Contains(logs, `"message":"MCP tool authorized"`) {
		t.Errorf("authorized INFO must not fire when ACL denies:\n%s", logs)
	}
}

// TestDispatch_NilPolicyDefaultsToAllow confirms passing a nil acl.Policy
// is treated as the default open policy: the tool executes normally.
func TestDispatch_NilPolicyDefaultsToAllow(t *testing.T) {
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(regs), nil, nil)
	if isErr {
		t.Fatalf("nil policy should default to allow, got error: %s", out)
	}
	if rf.calls != 1 {
		t.Errorf("expected tool to execute once under default policy, got %d", rf.calls)
	}
}

// TestACL_DefaultAllowsEverything pins the default-open contract: every
// (agent, tool) pair returns true, including unknown agents/tools.
func TestACL_DefaultAllowsEverything(t *testing.T) {
	cases := []struct{ agent, tool string }{
		{"alice", "read_file"},
		{"", ""},
		{"unknown", "made_up"},
	}
	for _, c := range cases {
		if !acl.Default.IsAllowed(c.agent, c.tool) {
			t.Errorf("acl.Default denied (%q, %q); default policy must allow everything",
				c.agent, c.tool)
		}
	}
}

// TestACL_PolicyFuncSatisfiesPolicy is a compile-time-ish guard: the
// PolicyFunc adapter must satisfy the Policy interface.
func TestACL_PolicyFuncSatisfiesPolicy(t *testing.T) {
	var _ acl.Policy = acl.PolicyFunc(func(_, _ string) bool { return true })
}

// TestNew_WithACLPolicyInjectsCustomPolicy confirms that WithACLPolicy
// installs a server-wide policy and that calls go through it on dispatch.
func TestNew_WithACLPolicyInjectsCustomPolicy(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	var observed atomic.Int32
	policy := acl.PolicyFunc(func(_, _ string) bool {
		observed.Add(1)
		return false
	})

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	r := newRegistryWith(rf)

	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": r}),
		WithAgentTokens(tm),
		WithAllowlist([]string{"*"}),
		WithACLPolicy(policy),
	)
	if err != nil {
		t.Fatal(err)
	}

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(map[string]*tools.ToolRegistry{"alice": r}), nil, srv.policy)
	if !isErr {
		t.Fatalf("expected ACL deny via injected policy, got success: %s", out)
	}
	if observed.Load() != 1 {
		t.Errorf("expected injected policy to be consulted once, got %d", observed.Load())
	}
	if rf.calls != 0 {
		t.Errorf("tool ran despite injected deny policy: calls=%d", rf.calls)
	}

	logs := buf.String()
	if !strings.Contains(logs, `"reason":"acl_denied"`) {
		t.Errorf("expected acl_denied reason in:\n%s", logs)
	}
}
