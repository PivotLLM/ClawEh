// ClawEh
// License: MIT

package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

func TestIdentity_MaestroRuleOnlyWhenEnabled(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	if strings.Contains(cb.getIdentity(), global.MaestroEntryTool) {
		t.Fatal("Maestro rule present for an agent without Maestro")
	}
	cb.WithMaestro(true)
	id := cb.getIdentity()
	if !strings.Contains(id, global.MaestroPromptRule) {
		t.Fatal("Maestro rule missing for an agent with Maestro")
	}
	// It is a numbered rule inside the Important Rules block.
	start := strings.Index(id, "## Important Rules")
	if start < 0 {
		t.Fatal("identity lacks the Important Rules block")
	}
	rules := id[start:]
	if !strings.Contains(rules, ". "+global.MaestroPromptRule) {
		t.Errorf("Maestro rule is not numbered with the others:\n%s", rules)
	}
}

func TestDiscoveryPins_MaestroEntryTool(t *testing.T) {
	on := &config.Config{}
	on.Tools.Discovery.Enabled = true
	on.Tools.Discovery.AlwaysShownNamespaces = []string{"fusion"}
	off := &config.Config{}
	off.Tools.Discovery.AlwaysShownNamespaces = []string{"fusion"}
	withMaestro := &config.AgentConfig{ID: "a", Maestro: &config.MaestroConfig{Enabled: true}}
	without := &config.AgentConfig{ID: "b"}

	if pins := discoveryPins(on, withMaestro); !slices.Contains(pins, global.MaestroEntryTool) || !slices.Contains(pins, "fusion") {
		t.Errorf("discovery on + maestro: pins = %v", pins)
	}
	if pins := discoveryPins(on, without); slices.Contains(pins, global.MaestroEntryTool) {
		t.Errorf("discovery on, no maestro: pins = %v", pins)
	}
	if pins := discoveryPins(off, withMaestro); slices.Contains(pins, global.MaestroEntryTool) {
		t.Errorf("discovery off: pins = %v", pins)
	}
	if pins := discoveryPins(on, nil); slices.Contains(pins, global.MaestroEntryTool) {
		t.Errorf("nil agent: pins = %v", pins)
	}
	// The configured list is not mutated by the per-agent pin.
	if len(on.Tools.Discovery.AlwaysShownNamespaces) != 1 {
		t.Error("configured always_shown_namespaces was mutated")
	}

	// The pin keeps exactly the entry tool visible; the rest stays hidden.
	pins := discoveryPins(on, withMaestro)
	if discoveryHidesTool(true, pins, global.MaestroEntryTool) {
		t.Error("entry tool hidden despite the pin")
	}
	if !discoveryHidesTool(true, pins, "maestro_file_list") {
		t.Error("other maestro tools must stay hidden under discovery")
	}
}
