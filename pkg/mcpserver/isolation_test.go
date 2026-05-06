// ClawEh
// License: MIT

package mcpserver

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// resolverFor builds an AgentResolver from a static map.
func resolverFor(m map[string]*tools.ToolRegistry) AgentResolver {
	return func(name string) (*tools.ToolRegistry, bool) {
		r, ok := m[name]
		return r, ok
	}
}

// TestDispatch_ValidTokenRoutesToAgentRegistry confirms that a call with
// the correct agent_token executes against the matching agent's registry,
// and that the agent_token is stripped from args before reaching the tool.
func TestDispatch_ValidTokenRoutesToAgentRegistry(t *testing.T) {
	aliceReadFile := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("alice-content")}
	bobReadFile := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("bob-content")}

	regs := map[string]*tools.ToolRegistry{
		"alice": newRegistryWith(aliceReadFile),
		"bob":   newRegistryWith(bobReadFile),
	}

	tm := agenttoken.NewManager()
	aliceTok := tm.Issue("alice")
	bobTok := tm.Issue("bob")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": aliceTok, "path": "x"}, tm, resolverFor(regs), nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if out != "alice-content" {
		t.Errorf("expected alice-content, got %q", out)
	}
	if aliceReadFile.calls != 1 || bobReadFile.calls != 0 {
		t.Errorf("dispatch called wrong registry: alice=%d bob=%d", aliceReadFile.calls, bobReadFile.calls)
	}
	if _, present := aliceReadFile.gotArg["agent_token"]; present {
		t.Error("agent_token leaked through to the tool's args")
	}

	out, isErr = dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": bobTok, "path": "x"}, tm, resolverFor(regs), nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if out != "bob-content" {
		t.Errorf("expected bob-content, got %q", out)
	}
	if bobReadFile.calls != 1 {
		t.Errorf("bob registry not called: %d", bobReadFile.calls)
	}
}

func TestDispatch_EmptyTokenRejected(t *testing.T) {
	tm := agenttoken.NewManager()
	tm.Issue("alice")
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{}, tm, resolverFor(nil), nil)
	if !isErr {
		t.Fatal("expected error for missing token")
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
}

func TestDispatch_UnknownTokenRejected(t *testing.T) {
	tm := agenttoken.NewManager()
	tm.Issue("alice")
	bogus := agenttoken.Prefix + strings.Repeat("a", 64)
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": bogus}, tm, resolverFor(nil), nil)
	if !isErr {
		t.Fatal("expected error for unknown token")
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
}

func TestDispatch_MalformedTokenRejected(t *testing.T) {
	tm := agenttoken.NewManager()
	tm.Issue("alice")

	cases := []string{
		"not-a-token",
		"AGTzzzz",
		"agt" + strings.Repeat("0", 64),
	}
	for _, c := range cases {
		out, isErr := dispatchToolCall(context.Background(), "read_file",
			map[string]any{"agent_token": c}, tm, resolverFor(nil), nil)
		if !isErr {
			t.Errorf("token %q expected to be rejected", c)
			continue
		}
		if out != invalidTokenMessage {
			t.Errorf("token %q: expected %q, got: %s", c, invalidTokenMessage, out)
		}
	}
}

func TestDispatch_SubagentSentinelReturnsHelpfulError(t *testing.T) {
	tm := agenttoken.NewManager()
	tm.Issue("alice")
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": agenttoken.SubagentSentinel}, tm, resolverFor(nil), nil)
	if !isErr {
		t.Fatal("expected error for sentinel token")
	}
	if !strings.Contains(out, "sub-agents are not granted") {
		t.Errorf("unexpected sentinel error: %s", out)
	}
}

func TestDispatch_RedactsTokensInOutput(t *testing.T) {
	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	leaky := &mockTool{
		name:   "read_file",
		params: map[string]any{},
		result: tools.NewToolResult("here is your token: " + tok + " — keep it safe"),
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(leaky)}

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok}, tm, resolverFor(regs), nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if strings.Contains(out, tok) {
		t.Errorf("redactor failed: token leaked in output: %s", out)
	}
	if !strings.Contains(out, agenttoken.Redaction) {
		t.Errorf("redactor placeholder missing: %s", out)
	}
}

// TestDispatch_RelativePathOutsideWorkspaceRejected confirms that a relative
// path which would escape the resolved workspace fails closed via the
// existing restrict_to_workspace check (no silent fallback to a shared root).
func TestDispatch_RelativePathOutsideWorkspaceRejected(t *testing.T) {
	tmp := t.TempDir()
	aliceWs := filepath.Join(tmp, "alice")
	if err := mkdirs(aliceWs); err != nil {
		t.Fatal(err)
	}

	rf := tools.NewReadFileTool(aliceWs, true, 0)
	reg := tools.NewToolRegistry()
	reg.Register(rf)

	tm := agenttoken.NewManager()
	tok := tm.Issue("alice")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"agent_token": tok, "path": "../etc/passwd"},
		tm, resolverFor(map[string]*tools.ToolRegistry{"alice": reg}), nil)
	if !isErr {
		t.Fatalf("expected workspace-escape rejection, got success: %s", out)
	}
}

// TestFirstCallTracker_LogsOncePerAgent confirms the boot-log debounce: the
// "MCP call from agent=<name> workspace=<path>" entry fires exactly once
// per agent per server lifetime.
func TestFirstCallTracker_LogsOncePerAgent(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	workspaces := map[string]string{
		"alice": "/var/agents/alice",
		"bob":   "/var/agents/bob",
	}
	tracker := newFirstCallTracker(workspaces)

	tracker.record("alice")
	tracker.record("alice")
	tracker.record("bob")
	tracker.record("alice")

	out := buf.String()
	if got := strings.Count(out, `"agent":"alice"`); got != 1 {
		t.Errorf("expected exactly 1 alice log line, got %d in:\n%s", got, out)
	}
	if got := strings.Count(out, `"agent":"bob"`); got != 1 {
		t.Errorf("expected exactly 1 bob log line, got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, "/var/agents/alice") {
		t.Error("expected alice workspace in log output")
	}
	if !strings.Contains(out, "/var/agents/bob") {
		t.Error("expected bob workspace in log output")
	}
}

// TestNew_BootLogEmitsAgentBindings confirms each registered agent's name
// and workspace are logged at MCP server startup.
func TestNew_BootLogEmitsAgentBindings(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	r := newRegistryWith()
	tm := agenttoken.NewManager()
	tm.Issue("alice")
	tm.Issue("bob")

	_, err := New(
		WithRegistry(r),
		WithAgentRegistries(map[string]*tools.ToolRegistry{
			"alice": r,
			"bob":   r,
		}),
		WithAgentTokens(tm),
		WithAgentWorkspaces(map[string]string{
			"alice": "/ws/alice",
			"bob":   "/ws/bob",
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "/ws/alice") {
		t.Errorf("alice workspace missing from boot log:\n%s", out)
	}
	if !strings.Contains(out, "/ws/bob") {
		t.Errorf("bob workspace missing from boot log:\n%s", out)
	}
}

func TestInjectAgentTokenParam_AddsRequiredField(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
	out := injectAgentTokenParam(in)

	props, _ := out["properties"].(map[string]any)
	if _, ok := props["agent_token"]; !ok {
		t.Error("agent_token not added to properties")
	}
	if _, ok := props["path"]; !ok {
		t.Error("existing path property dropped")
	}

	required := stringSliceFromAny(out["required"])
	if !containsString(required, "agent_token") {
		t.Error("agent_token not added to required")
	}
	if !containsString(required, "path") {
		t.Error("existing required field dropped")
	}

	// Original schema map must remain untouched.
	origReq := stringSliceFromAny(in["required"])
	if containsString(origReq, "agent_token") {
		t.Error("injectAgentTokenParam mutated input schema's required slice")
	}
	origProps, _ := in["properties"].(map[string]any)
	if _, ok := origProps["agent_token"]; ok {
		t.Error("injectAgentTokenParam mutated input schema's properties")
	}
}

func TestInjectAgentTokenParam_HandlesNilSchema(t *testing.T) {
	out := injectAgentTokenParam(nil)
	if out["type"] != "object" {
		t.Errorf("expected synthetic type=object, got %v", out["type"])
	}
	props, _ := out["properties"].(map[string]any)
	if _, ok := props["agent_token"]; !ok {
		t.Error("agent_token not added in nil-input case")
	}
}

func TestInjectAgentTokenParam_HandlesAnyRequiredSlice(t *testing.T) {
	in := map[string]any{
		"required": []any{"path", "content"},
	}
	out := injectAgentTokenParam(in)
	required := stringSliceFromAny(out["required"])
	if !containsString(required, "agent_token") || !containsString(required, "path") || !containsString(required, "content") {
		t.Errorf("unexpected required slice: %v", required)
	}
}

// mkdirs is a tiny test helper that recursively creates p with mode 0o755.
func mkdirs(p string) error {
	return os.MkdirAll(p, 0o755)
}
