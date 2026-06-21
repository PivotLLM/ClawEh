// ClawEh
// License: MIT

package tools

import (
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
