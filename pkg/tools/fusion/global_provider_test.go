// ClawEh
// License: MIT

package fusion

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// TestProvider_GatingOff verifies the per-agent gate: an agent without the fusion
// flag gets no tools, and the enumeration pass (nil Cfg) returns nil. Both paths
// return before the shared engine is built, so no config folder is needed.
func TestProvider_GatingOff(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{
		{ID: "bob"}, // fusion off
	}}}

	if got := GlobalProvider.RegisterTools(global.Deps{Cfg: cfg, AgentID: "bob"}); got != nil {
		t.Errorf("bob (fusion off) should get no tools, got %d", len(got))
	}

	// Enumeration pass: no live config.
	if got := GlobalProvider.RegisterTools(global.Deps{AgentID: "bob"}); got != nil {
		t.Errorf("enumeration pass should return nil, got %d", len(got))
	}
}

func TestProvider_Metadata(t *testing.T) {
	if GlobalProvider.Namespace() != "fusion" {
		t.Errorf("Namespace = %q, want fusion", GlobalProvider.Namespace())
	}
	if GlobalProvider.Suite() != "fusion" {
		t.Errorf("Suite = %q, want fusion", GlobalProvider.Suite())
	}
	if ok, _ := GlobalProvider.Available(nil); !ok {
		t.Error("Available should be true")
	}
}
