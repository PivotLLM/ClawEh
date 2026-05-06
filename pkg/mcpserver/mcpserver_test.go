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

// --- mock tool ---

type mockTool struct {
	name   string
	desc   string
	params map[string]any
	result *tools.ToolResult
	calls  int
	gotArg map[string]any
}

func (m *mockTool) Name() string               { return m.name }
func (m *mockTool) Description() string        { return m.desc }
func (m *mockTool) Parameters() map[string]any { return m.params }
func (m *mockTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	m.calls++
	m.gotArg = args
	return m.result
}

func newRegistryWith(toolList ...tools.Tool) *tools.ToolRegistry {
	r := tools.NewToolRegistry()
	for _, t := range toolList {
		r.Register(t)
	}
	return r
}

// minimalOpts returns the option set required for any New() call to succeed:
// a registry, a token manager, and at least one agent registry.
func minimalOpts(reg *tools.ToolRegistry) []Option {
	tm := agenttoken.NewManager()
	tm.Issue("alice")
	return []Option{
		WithRegistry(reg),
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": reg}),
		WithAgentTokens(tm),
	}
}

// --- New() validation ---

func TestNew_RequiresRegistry(t *testing.T) {
	tm := agenttoken.NewManager()
	_, err := New(WithAgentTokens(tm), WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": newRegistryWith()}))
	if err == nil {
		t.Fatal("expected error when registry is missing")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("expected error to mention registry, got %q", err.Error())
	}
}

func TestNew_RequiresAgentTokens(t *testing.T) {
	r := newRegistryWith()
	_, err := New(WithRegistry(r), WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": r}))
	if err == nil {
		t.Fatal("expected error when agent-token manager is missing")
	}
	if !strings.Contains(err.Error(), "agent-token") {
		t.Errorf("expected error to mention agent-token manager, got %q", err.Error())
	}
}

func TestNew_RequiresAgentRegistries(t *testing.T) {
	r := newRegistryWith()
	tm := agenttoken.NewManager()
	_, err := New(WithRegistry(r), WithAgentTokens(tm))
	if err == nil {
		t.Fatal("expected error when agent registries are missing")
	}
	if !strings.Contains(err.Error(), "agent registry") {
		t.Errorf("expected error to mention agent registries, got %q", err.Error())
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	r := newRegistryWith()
	srv, err := New(minimalOpts(r)...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Listen() != DefaultListen {
		t.Errorf("expected default listen %q, got %q", DefaultListen, srv.Listen())
	}
	if srv.EndpointPath() != DefaultEndpointPath {
		t.Errorf("expected default endpoint %q, got %q", DefaultEndpointPath, srv.EndpointPath())
	}
}

func TestNew_OptionsOverrideDefaults(t *testing.T) {
	r := newRegistryWith()
	opts := append(minimalOpts(r),
		WithListen("127.0.0.1:9999"),
		WithEndpointPath("/custom"),
	)
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Listen() != "127.0.0.1:9999" {
		t.Errorf("expected listen override, got %q", srv.Listen())
	}
	if srv.EndpointPath() != "/custom" {
		t.Errorf("expected endpoint override, got %q", srv.EndpointPath())
	}
}

func TestWithListen_EmptyKeepsDefault(t *testing.T) {
	r := newRegistryWith()
	opts := append(minimalOpts(r), WithListen(""))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Listen() != DefaultListen {
		t.Errorf("expected empty WithListen to be ignored, got %q", srv.Listen())
	}
}

// --- allowlist filtering ---

func TestAddTools_AllowlistFiltersTools(t *testing.T) {
	allowed := &mockTool{name: "read_file", desc: "reads", params: map[string]any{"type": "object"}, result: tools.SilentResult("ok")}
	denied := &mockTool{name: "exec_shell", desc: "dangerous", params: map[string]any{"type": "object"}, result: tools.SilentResult("ok")}
	r := newRegistryWith(allowed, denied)

	opts := append(minimalOpts(r), WithAllowlist([]string{"read_file"}))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	listed := srv.srv.ListTools()
	if _, ok := listed["read_file"]; !ok {
		t.Error("expected allowed tool 'read_file' to be registered")
	}
	if _, ok := listed["exec_shell"]; ok {
		t.Error("did not expect denied tool 'exec_shell' to be registered")
	}
}

func TestAddTools_WildcardAllowlistExposesAll(t *testing.T) {
	a := &mockTool{name: "read_file", params: map[string]any{}, result: tools.SilentResult("ok")}
	b := &mockTool{name: "web_fetch", params: map[string]any{}, result: tools.SilentResult("ok")}
	c := &mockTool{name: "message", params: map[string]any{}, result: tools.SilentResult("ok")}
	r := newRegistryWith(a, b, c)

	opts := append(minimalOpts(r), WithAllowlist([]string{"*"}))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listed := srv.srv.ListTools()
	if _, ok := listed["read_file"]; !ok {
		t.Error("expected 'read_file' exposed by wildcard")
	}
	if _, ok := listed["web_fetch"]; !ok {
		t.Error("expected 'web_fetch' exposed by wildcard")
	}
	if _, ok := listed["message"]; ok {
		t.Error("'message' must never be exposed even with wildcard")
	}
}

func TestAddTools_PrefixWildcardAllowlist(t *testing.T) {
	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.SilentResult("ok")}
	rd := &mockTool{name: "read_dir", params: map[string]any{}, result: tools.SilentResult("ok")}
	wf := &mockTool{name: "write_file", params: map[string]any{}, result: tools.SilentResult("ok")}
	r := newRegistryWith(rf, rd, wf)

	opts := append(minimalOpts(r), WithAllowlist([]string{"read_*"}))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listed := srv.srv.ListTools()
	if _, ok := listed["read_file"]; !ok {
		t.Error("expected 'read_file' exposed by 'read_*'")
	}
	if _, ok := listed["read_dir"]; !ok {
		t.Error("expected 'read_dir' exposed by 'read_*'")
	}
	if _, ok := listed["write_file"]; ok {
		t.Error("did not expect 'write_file' exposed by 'read_*'")
	}
}

func TestAddTools_EmptyAllowlistRegistersNothing(t *testing.T) {
	r := newRegistryWith(
		&mockTool{name: "a", params: map[string]any{}, result: tools.SilentResult("ok")},
		&mockTool{name: "b", params: map[string]any{}, result: tools.SilentResult("ok")},
	)
	srv, err := New(minimalOpts(r)...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(srv.srv.ListTools()); got != 0 {
		t.Errorf("expected no tools registered with empty allowlist, got %d", got)
	}
}

func TestAddTools_MessageToolNeverRegistered(t *testing.T) {
	msg := &mockTool{name: "message", desc: "agent-internal", params: map[string]any{"type": "object"}, result: tools.SilentResult("ok")}
	r := newRegistryWith(msg)

	opts := append(minimalOpts(r), WithAllowlist([]string{"message"}))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := srv.srv.ListTools()["message"]; ok {
		t.Fatal("'message' tool must never be exposed via MCP, even when allowlisted")
	}
}

func TestAddTools_NilParametersStillRegisters(t *testing.T) {
	t1 := &mockTool{name: "noparams", desc: "no schema", params: nil, result: tools.SilentResult("ok")}
	r := newRegistryWith(t1)

	opts := append(minimalOpts(r), WithAllowlist([]string{"noparams"}))
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := srv.srv.ListTools()["noparams"]; !ok {
		t.Error("expected tool with nil parameters to register with synthetic empty-object schema")
	}
}
