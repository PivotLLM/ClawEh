// ClawEh
// License: MIT

package tools

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

type fakeSuiteProvider struct{ suite string }

func (f fakeSuiteProvider) RegisterTools(global.Deps) []global.ToolDefinition {
	// Default-deny tools (no DefaultAllow): a non-suite provider would have these
	// filtered out, but a suite registers them as a unit.
	return []global.ToolDefinition{{Name: "alpha"}, {Name: "beta"}}
}
func (f fakeSuiteProvider) Suite() string { return f.suite }

func cfgWithAgent(a config.AgentConfig) *config.Config {
	return &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{a}}}
}

func TestNamespacedProvider_SuiteGating(t *testing.T) {
	p := NamespacedProvider("maestro", fakeSuiteProvider{suite: "maestro"})

	// Suite off (maestro default off) → no tools, even though they're default-deny
	// the gate is the flag, not ToolEnabled.
	off := cfgWithAgent(config.AgentConfig{ID: "amber", Maestro: false})
	if got := p.Build(ToolDeps{Cfg: off, AgentID: "amber"}); len(got) != 0 {
		t.Errorf("disabled suite should yield no tools, got %d", len(got))
	}
	// Suite on → ALL tools (bypassing the per-tool default-deny filter).
	on := cfgWithAgent(config.AgentConfig{ID: "amber", Maestro: true})
	if got := p.Build(ToolDeps{Cfg: on, AgentID: "amber"}); len(got) != 2 {
		t.Errorf("enabled suite should register all tools, got %d", len(got))
	}
	// Describe collapses the whole suite to one catalog entry, marked.
	d := p.Describe()
	if len(d) != 1 || d[0].Suite != "maestro" || d[0].Name != "maestro" {
		t.Errorf("suite should describe as a single marked entry, got %+v", d)
	}
}

func TestSuiteDefaults(t *testing.T) {
	c := cfgWithAgent(config.AgentConfig{ID: "amber"}) // no flags set
	// cogmem defaults ON; maestro defaults OFF.
	if !c.AgentSuiteEnabled("amber", "cogmem") {
		t.Error("cogmem should default on")
	}
	if c.AgentSuiteEnabled("amber", "maestro") {
		t.Error("maestro should default off")
	}
	// Unknown agent: cogmem still on, others off.
	if !c.AgentSuiteEnabled("ghost", "cogmem") || c.AgentSuiteEnabled("ghost", "maestro") {
		t.Error("unknown-agent suite defaults wrong")
	}
	// cogmem explicitly disabled.
	no := false
	c2 := cfgWithAgent(config.AgentConfig{ID: "amber", Cogmem: &no})
	if c2.AgentSuiteEnabled("amber", "cogmem") {
		t.Error("cogmem:false should disable")
	}
}

// denyAllChecker refuses every per-tool allowlist query, modelling an agent
// whose allowlist does not list the suite's tools.
type denyAllChecker struct{}

func (denyAllChecker) IsToolAllowed(string) bool { return false }

// TestRegisterSuite_BypassesExecutionAllowlist verifies a suite-registered tool
// executes even when the context allowlist denies it, while a normally-registered
// tool is still blocked by the same checker. This guards the bug where maestro
// tools were visible but "denied by agent allowlist" at execution.
func TestRegisterSuite_BypassesExecutionAllowlist(t *testing.T) {
	r := NewToolRegistry()
	suiteTool := &mockContextAwareTool{mockRegistryTool: *newMockTool("maestro_health", "suite tool")}
	plainTool := &mockContextAwareTool{mockRegistryTool: *newMockTool("web_fetch", "ordinary tool")}
	r.RegisterSuite(suiteTool)
	r.Register(plainTool)

	ctx := WithToolAllowChecker(context.Background(), denyAllChecker{})

	res := r.ExecuteWithContext(ctx, "maestro_health", nil, "", "", nil)
	if res == nil || res.IsError {
		t.Fatalf("suite tool should bypass the allowlist, got %+v", res)
	}
	if suiteTool.lastCtx == nil {
		t.Error("suite tool was not executed")
	}

	blocked := r.ExecuteWithContext(ctx, "web_fetch", nil, "", "", nil)
	if blocked == nil || !blocked.IsError {
		t.Fatalf("ordinary tool should be denied by the allowlist, got %+v", blocked)
	}
	if plainTool.lastCtx != nil {
		t.Error("ordinary tool ran despite allowlist deny")
	}
}
