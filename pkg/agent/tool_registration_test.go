// ClawEh
// License: MIT

package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// toolRegTestConfig builds a single-agent config that allows all tools.
func toolRegTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true, Tools: []string{"*"}},
			},
		},
	}
}

func agentToolNames(t *testing.T, al *AgentLoop) []string {
	t.Helper()
	ag, ok := al.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("agent 'main' not found in registry")
	}
	return ag.Tools.List()
}

func assertHasTool(t *testing.T, names []string, want string) {
	t.Helper()
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("expected tool %q to be registered; got %v", want, names)
}

// TestRegisterTools_NoDuplicateRegistration guards the consolidation: tools are
// registered exactly once (no phase-1 + runtime double pass), so no "overwrites
// existing tool" warnings are emitted, and both a deps-free tool (file_read_bytes) and
// a runtime-only tool (session_compact, which needs the CompactFn closure) are
// present.
func TestRegisterTools_NoDuplicateRegistration(t *testing.T) {
	cfg := toolRegTestConfig(t)

	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	restore()

	if strings.Contains(buf.String(), "overwrites existing tool") {
		t.Errorf("tool registration emitted overwrite warnings (double registration):\n%s", buf.String())
	}

	names := agentToolNames(t, al)
	assertHasTool(t, names, "file_read_bytes")       // deps-free provider
	assertHasTool(t, names, "session_compact") // runtime-only (CompactFn closure)

	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("tool %q registered more than once: %v", n, names)
		}
		seen[n] = true
	}
}

// TestReloadProviderAndConfig_RegistersRuntimeTools guards that a config reload
// rebuilds the full tool set on the new registry (it previously never re-ran the
// runtime registration, leaving reloaded agents with degraded/missing tools).
func TestReloadProviderAndConfig_RegistersRuntimeTools(t *testing.T) {
	cfg := toolRegTestConfig(t)
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	before := agentToolNames(t, al)
	if len(before) == 0 {
		t.Fatal("no tools registered at construction")
	}

	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, toolRegTestConfig(t)); err != nil {
		t.Fatalf("ReloadProviderAndConfig: %v", err)
	}

	after := agentToolNames(t, al)
	// Runtime tools must survive the reload (the regression this fixes).
	assertHasTool(t, after, "session_compact")
	assertHasTool(t, after, "file_read_bytes")
	if len(after) != len(before) {
		t.Errorf("tool set changed across reload: before=%d (%v) after=%d (%v)", len(before), before, len(after), after)
	}
}

// TestGetModelInfo_ReflectsActiveSelection guards that /status (via GetModelInfo)
// reports the session's selected model, not always the first candidate.
func TestGetModelInfo_ReflectsActiveSelection(t *testing.T) {
	cfg := toolRegTestConfig(t)
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	ag, ok := al.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("agent 'main' not found")
	}
	ag.Candidates = []providers.FallbackCandidate{
		{Alias: "m0", Provider: "P"},
		{Alias: "m1", Provider: "P"},
		{Alias: "m2", Provider: "P"},
	}
	const sk = "sess-1"
	if err := al.setActiveModelIndex(ag, sk, 2); err != nil {
		t.Fatalf("setActiveModelIndex: %v", err)
	}

	rt := al.buildCommandsRuntime(ag, &processOptions{SessionKey: sk}, bus.InboundMessage{})
	name, _, _, _ := rt.GetModelInfo()
	if name != "m2" {
		t.Errorf("GetModelInfo name = %q, want m2 (the active selection)", name)
	}
}
