// ClawEh
// License: MIT

package mcpserver

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// captureLogs redirects the logger to a buffer for the duration of the test.
// The returned func must be deferred to restore the prior logger.
func captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	return &buf, restore
}

func TestDispatch_SuccessEmitsAuthorizedInfoPerCall(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")
	tracker := newFirstCallTracker(map[string]string{"alice": "/ws/alice"})

	for i := 0; i < 2; i++ {
		out, isErr := dispatchToolCall(context.Background(), "read_file",
			map[string]any{"agent_token": tok, "path": "x"}, tm, resolverFor(regs), tracker)
		if isErr {
			t.Fatalf("call %d unexpected error: %s", i, out)
		}
	}

	out := buf.String()
	if got := strings.Count(out, `"message":"MCP tool authorized"`); got != 2 {
		t.Errorf("expected 2 'MCP tool authorized' INFO entries, got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, `"agent":"alice"`) {
		t.Errorf("expected agent field in authorized log:\n%s", out)
	}
	if !strings.Contains(out, `"tool":"read_file"`) {
		t.Errorf("expected tool field in authorized log:\n%s", out)
	}
	if !strings.Contains(out, `"workspace":"/ws/alice"`) {
		t.Errorf("expected workspace field in authorized log:\n%s", out)
	}
	// First-call breadcrumb still fires once.
	if got := strings.Count(out, `"message":"MCP call from agent"`); got != 1 {
		t.Errorf("expected exactly 1 'MCP call from agent' boot breadcrumb, got %d", got)
	}
	// Token must never appear in any captured log line.
	if strings.Contains(out, tok) {
		t.Errorf("token leaked into log output:\n%s", out)
	}
}

func TestDispatch_SubagentSentinelEmitsWarn(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	tm := agenttoken.NewManager()
	tm.Issue("alice")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": agenttoken.SubagentSentinel}, tm, resolverFor(nil), nil)
	if !isErr {
		t.Fatalf("expected rejection, got: %s", out)
	}

	logs := buf.String()
	if got := strings.Count(logs, `"level":"warn"`); got != 1 {
		t.Errorf("expected exactly 1 WARN entry, got %d in:\n%s", got, logs)
	}
	if !strings.Contains(logs, `"message":"MCP token rejected: subagent sentinel"`) {
		t.Errorf("expected sentinel-rejection WARN, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"reason":"subagent_sentinel"`) {
		t.Errorf("expected reason=subagent_sentinel, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"tool":"read_file"`) {
		t.Errorf("expected tool field, got:\n%s", logs)
	}
}

func TestDispatch_InvalidTokenEmitsWarn(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	tm := agenttoken.NewManager()
	realTok := tm.Issue("alice")

	cases := []struct {
		name string
		tok  string
	}{
		{"empty", ""},
		{"malformed", "not-a-token"},
		{"unknown", agenttoken.Prefix + strings.Repeat("a", 64)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf.Reset()
			out, isErr := dispatchToolCall(context.Background(), "read_file",
				map[string]any{"agent_token": c.tok}, tm, resolverFor(nil), nil)
			if !isErr {
				t.Fatalf("expected rejection, got: %s", out)
			}
			logs := buf.String()
			if got := strings.Count(logs, `"level":"warn"`); got != 1 {
				t.Errorf("expected 1 WARN entry, got %d in:\n%s", got, logs)
			}
			if !strings.Contains(logs, `"message":"MCP token rejected"`) {
				t.Errorf("expected invalid-token WARN, got:\n%s", logs)
			}
			if !strings.Contains(logs, `"reason":"invalid_token"`) {
				t.Errorf("expected reason=invalid_token, got:\n%s", logs)
			}
			// token_len must reflect what was supplied.
			marker := `"token_len":` + strconv.Itoa(len(c.tok))
			if !strings.Contains(logs, marker) {
				t.Errorf("expected %s in log, got:\n%s", marker, logs)
			}
			// Ensure no real token leaks (defence-in-depth across cases).
			if strings.Contains(logs, realTok) {
				t.Errorf("issued token leaked into invalid-token rejection log:\n%s", logs)
			}
			// And the supplied (malformed) token must not be logged either.
			if c.tok != "" && strings.Contains(logs, c.tok) {
				t.Errorf("supplied token %q leaked into rejection log:\n%s", c.tok, logs)
			}
		})
	}
}

func TestDispatch_NoRegistryEmitsWarn(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")
	// resolverFor(nil) returns ok=false for every name -> no_registry path.
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(nil), nil)
	if !isErr {
		t.Fatalf("expected rejection, got: %s", out)
	}
	logs := buf.String()
	if got := strings.Count(logs, `"level":"warn"`); got != 1 {
		t.Errorf("expected 1 WARN entry, got %d in:\n%s", got, logs)
	}
	if !strings.Contains(logs, `"message":"MCP token rejected: no registry for agent"`) {
		t.Errorf("expected no-registry WARN, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"reason":"no_registry"`) {
		t.Errorf("expected reason=no_registry, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"agent":"alice"`) {
		t.Errorf("expected agent field, got:\n%s", logs)
	}
	if strings.Contains(logs, tok) {
		t.Errorf("token leaked into no-registry rejection log:\n%s", logs)
	}
}

// TestRegistry_PanicsOnNilToolResult pins the contract that makes the
// `result == nil` branch in dispatchToolCall a defence-in-depth guard rather
// than a reachable path: today *tools.ToolRegistry.Execute panics if the
// underlying tool returns nil (it dereferences result.IsError before
// returning). If that contract ever changes, dispatchToolCall is ready to
// log a WARN instead of NPE'ing — but until then the path is unreachable
// from a unit test that goes through a real registry.
func TestRegistry_PanicsOnNilToolResult(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected registry.Execute to panic on nil tool result; if it now returns nil, " +
				"replace this test with one that exercises dispatchToolCall's nil-result WARN path")
		}
	}()

	nilOut := &mockTool{name: "read_file", params: map[string]any{}, result: nil}
	reg := newRegistryWith(nilOut)
	_ = reg.Execute(context.Background(), "read_file", map[string]any{})
}
