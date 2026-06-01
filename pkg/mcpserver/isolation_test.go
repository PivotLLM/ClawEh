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
	toolsfiles "github.com/PivotLLM/ClawEh/pkg/tools/files"
)

// resolverFor builds an AgentResolver from a static map.
func resolverFor(m map[string]*tools.ToolRegistry) AgentResolver {
	return func(name string) (*tools.ToolRegistry, bool) {
		r, ok := m[name]
		return r, ok
	}
}

// TestDispatch_ValidTokenRoutesToAgentRegistry confirms that a call with
// the correct session_token executes against the matching agent's registry,
// and that the session_token is stripped from args before reaching the tool.
func TestDispatch_ValidTokenRoutesToAgentRegistry(t *testing.T) {
	aliceReadFile := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("alice-content")}
	bobReadFile := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("bob-content")}

	regs := map[string]*tools.ToolRegistry{
		"alice": newRegistryWith(aliceReadFile),
		"bob":   newRegistryWith(bobReadFile),
	}

	st := newSessionTokenStore()
	aliceTok := st.Issue("alice", "test:alice:main", "/tmp/archive/alice")
	bobTok := st.Issue("bob", "test:bob:main", "/tmp/archive/bob")

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": aliceTok, "path": "x"}, st, resolverFor(regs), nil, nil, nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if out != "alice-content" {
		t.Errorf("expected alice-content, got %q", out)
	}
	if aliceReadFile.calls != 1 || bobReadFile.calls != 0 {
		t.Errorf("dispatch called wrong registry: alice=%d bob=%d", aliceReadFile.calls, bobReadFile.calls)
	}
	if _, present := aliceReadFile.gotArg["session_token"]; present {
		t.Error("session_token leaked through to the tool's args")
	}

	out, isErr = dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": bobTok, "path": "x"}, st, resolverFor(regs), nil, nil, nil)
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
	st := newSessionTokenStore()
	st.Issue("alice", "test:alice:main", "/tmp/archive/alice")
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{}, st, resolverFor(nil), nil, nil, nil)
	if !isErr {
		t.Fatal("expected error for missing token")
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
}

func TestDispatch_UnknownTokenRejected(t *testing.T) {
	st := newSessionTokenStore()
	st.Issue("alice", "test:alice:main", "/tmp/archive/alice")
	bogus := sessionTokenPrefix + strings.Repeat("a", 64)
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": bogus}, st, resolverFor(nil), nil, nil, nil)
	if !isErr {
		t.Fatal("expected error for unknown token")
	}
	if out != invalidTokenMessage {
		t.Errorf("expected %q, got: %s", invalidTokenMessage, out)
	}
}

func TestDispatch_MalformedTokenRejected(t *testing.T) {
	st := newSessionTokenStore()
	st.Issue("alice", "test:alice:main", "/tmp/archive/alice")

	cases := []string{
		"not-a-token",
		"SSTzzzz",
		"sst" + strings.Repeat("0", 64),
	}
	for _, c := range cases {
		out, isErr := dispatchToolCall(context.Background(), "read_file",
			map[string]any{"session_token": c}, st, resolverFor(nil), nil, nil, nil)
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
	st := newSessionTokenStore()
	st.Issue("alice", "test:alice:main", "/tmp/archive/alice")
	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": agenttoken.SubagentSentinel}, st, resolverFor(nil), nil, nil, nil)
	if !isErr {
		t.Fatal("expected error for sentinel token")
	}
	if !strings.Contains(out, "sub-agents are not granted") {
		t.Errorf("unexpected sentinel error: %s", out)
	}
}

func TestDispatch_RedactsTokensInOutput(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "test:alice:main", "/tmp/archive/alice")

	leaky := &mockTool{
		name:   "read_file",
		params: map[string]any{},
		result: tools.NewToolResult("here is your token: " + tok + " — keep it safe"),
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(leaky)}

	out, isErr := dispatchToolCall(context.Background(), "read_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, nil)
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

	rf := toolsfiles.NewReadFileTool(aliceWs, true, 0)
	reg := tools.NewToolRegistry()
	reg.Register(rf)

	st, tok := seedSessionToken("alice")

	out, isErr := dispatchToolCall(context.Background(), "files_read",
		map[string]any{"session_token": tok, "path": "../etc/passwd"},
		st, resolverFor(map[string]*tools.ToolRegistry{"alice": reg}), nil, nil, nil)
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

func TestInjectSessionTokenParam_AddsRequiredField(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
	out := injectSessionTokenParam(in)

	props, _ := out["properties"].(map[string]any)
	if _, ok := props["session_token"]; !ok {
		t.Error("session_token not added to properties")
	}
	if _, ok := props["path"]; !ok {
		t.Error("existing path property dropped")
	}

	required := stringSliceFromAny(out["required"])
	if !containsString(required, "session_token") {
		t.Error("session_token not added to required")
	}
	if !containsString(required, "path") {
		t.Error("existing required field dropped")
	}

	// Original schema map must remain untouched.
	origReq := stringSliceFromAny(in["required"])
	if containsString(origReq, "session_token") {
		t.Error("injectSessionTokenParam mutated input schema's required slice")
	}
	origProps, _ := in["properties"].(map[string]any)
	if _, ok := origProps["session_token"]; ok {
		t.Error("injectSessionTokenParam mutated input schema's properties")
	}
}

func TestInjectSessionTokenParam_HandlesNilSchema(t *testing.T) {
	out := injectSessionTokenParam(nil)
	if out["type"] != "object" {
		t.Errorf("expected synthetic type=object, got %v", out["type"])
	}
	props, _ := out["properties"].(map[string]any)
	if _, ok := props["session_token"]; !ok {
		t.Error("session_token not added in nil-input case")
	}
}

func TestInjectSessionTokenParam_HandlesAnyRequiredSlice(t *testing.T) {
	in := map[string]any{
		"required": []any{"path", "content"},
	}
	out := injectSessionTokenParam(in)
	required := stringSliceFromAny(out["required"])
	if !containsString(required, "session_token") || !containsString(required, "path") || !containsString(required, "content") {
		t.Errorf("unexpected required slice: %v", required)
	}
}

// mkdirs is a tiny test helper that recursively creates p with mode 0o755.
func mkdirs(p string) error {
	return os.MkdirAll(p, 0o755)
}
