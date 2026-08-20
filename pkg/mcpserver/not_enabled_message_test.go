// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// TestDispatch_ToolNotInAgentRegistryReturnsNotEnabled covers the case that
// actually fires in production: tools/list publishes the union of every
// agent's registry, so a caller can hold a perfectly valid token and invoke a
// tool its own agent never registered. That must read as a permission state,
// not a malfunction.
func TestDispatch_ToolNotInAgentRegistryReturnsNotEnabled(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	// alice's registry has read_file only; trello_search belongs to another agent.
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	st, tok := seedSessionToken("alice")

	out, isErr := dispatchToolCall(context.Background(), "trello_search",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, acl.Default, nil, nil)
	if !isErr {
		t.Fatalf("expected refusal for unregistered tool, got success: %s", out)
	}
	if out != tools.NotEnabledMessage("trello_search") {
		t.Errorf("unexpected refusal message: %s", out)
	}
	if logs := buf.String(); !strings.Contains(logs, `"reason":"tool_not_in_registry"`) {
		t.Errorf("expected reason=tool_not_in_registry in:\n%s", logs)
	}
}

// TestDispatch_DenialCausesAreIndistinguishable pins the non-disclosure
// property: an ACL refusal and a tool missing from the agent's registry return
// byte-identical text, so the caller cannot probe which agents hold which
// tools. The two causes remain separable in the logs.
func TestDispatch_DenialCausesAreIndistinguishable(t *testing.T) {
	denyAll := acl.PolicyFunc(func(_, _ string) bool { return false })

	// Case 7: tool IS registered, policy refuses it.
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regsWith := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	stA, tokA := seedSessionToken("alice")
	aclOut, aclErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": tokA}, stA, resolverFor(regsWith), nil, denyAll, nil, nil)

	// Case 6: tool is absent from the registry, policy would allow it.
	other := &mockTool{name: "list_dir", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regsWithout := map[string]*tools.ToolRegistry{"alice": newRegistryWith(other)}
	stB, tokB := seedSessionToken("alice")
	missOut, missErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": tokB}, stB, resolverFor(regsWithout), nil, acl.Default, nil, nil)

	if !aclErr || !missErr {
		t.Fatalf("both causes must be errors: acl=%v missing=%v", aclErr, missErr)
	}
	if aclOut != missOut {
		t.Errorf("denial causes are distinguishable:\n acl:     %s\n missing: %s", aclOut, missOut)
	}
}

// TestNotEnabledMessage_TellsAgentToStopAndAsk pins the properties the wording
// exists for. A refusal that merely says "not authorized" invites a retry or a
// workaround; this message must name the cause as configuration, rule out a
// fault, and hand the agent the remedy.
func TestNotEnabledMessage_TellsAgentToStopAndAsk(t *testing.T) {
	msg := tools.NotEnabledMessage("trello_search")

	if !strings.Contains(msg, "trello_search") {
		t.Errorf("message should name the tool: %s", msg)
	}
	for _, want := range []string{
		"Permission denied",      // names it as permission
		"not enabled",            // ...specifically a config state
		"not a bug or an outage", // rules out a fault
		"Do not retry",           // forbids the retry
		"work around",            // forbids the workaround
		"tell the user",          // gives the remedy
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

// TestDispatch_InvalidTokenIsNotAPermissionMessage guards the authn/authz
// split: a bad token must keep its own instructive wording (the caller should
// re-send a valid token, which IS worth retrying) and must never be reported
// as a permission problem.
func TestDispatch_InvalidTokenIsNotAPermissionMessage(t *testing.T) {
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	st, _ := seedSessionToken("alice")

	for _, tc := range []struct {
		name  string
		token any
	}{
		{"no token", nil},
		{"unknown token", "SST" + strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.token != nil {
				args["session_token"] = tc.token
			}
			out, isErr := dispatchToolCall(context.Background(), "read_file",
				args, st, resolverFor(regs), nil, acl.Default, nil, nil)
			if !isErr {
				t.Fatalf("expected rejection, got success: %s", out)
			}
			if out != invalidTokenMessage {
				t.Errorf("expected invalidTokenMessage, got: %s", out)
			}
			if strings.Contains(out, "Permission denied") {
				t.Errorf("auth failure reported as a permission problem: %s", out)
			}
		})
	}
}
