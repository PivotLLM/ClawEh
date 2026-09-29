// ClawEh
// License: MIT

package fusion

import (
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
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

// allowedDefs keeps only the tools the agent's mcp_tools entries admit: Fusion
// on with no entry yields none, a service entry yields that service, a group
// entry yields that group.
func TestAllowedDefs(t *testing.T) {
	defs := []global.ToolDefinition{
		{Name: "wxca_city_get"},
		{Name: "wxca_city_hourly"},
		{Name: "microsoft365_calendar_read_summary"},
		{Name: "microsoft365_mail_search"},
		{Name: "google_auth_setup"},
	}
	names := func(ds []global.ToolDefinition) []string {
		out := make([]string, 0, len(ds))
		for _, d := range ds {
			out = append(out, d.Name)
		}
		return out
	}
	tests := []struct {
		name  string
		agent *config.AgentConfig
		want  []string
	}{
		{"nil agent", nil, []string{}},
		{"fusion on, nothing listed", &config.AgentConfig{ID: "a", Fusion: true}, []string{}},
		{"fusion off, services listed", &config.AgentConfig{ID: "a", MCPTools: []string{"wxca"}}, []string{}},
		{
			"one service", &config.AgentConfig{ID: "a", Fusion: true, MCPTools: []string{"wxca"}},
			[]string{"wxca_city_get", "wxca_city_hourly"},
		},
		{
			"a group and a service", &config.AgentConfig{ID: "a", Fusion: true, MCPTools: []string{"microsoft365_calendar", "google"}},
			[]string{"microsoft365_calendar_read_summary", "google_auth_setup"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(allowedDefs(defs, tc.agent)); !slices.Equal(got, tc.want) {
				t.Errorf("allowedDefs = %v, want %v", got, tc.want)
			}
		})
	}
}

// Fusion on with nothing granted alerts once per agent per process, naming the
// agent and the Fusion services list.
func TestAlertNoServices_OncePerAgent(t *testing.T) {
	rec := testalerts.Install(t)
	noServicesAlerted.Clear()
	alertNoServices("Amber")
	alertNoServices("amber")
	alertNoServices("Karen")
	got := rec.Alerts()
	if len(got) != 2 {
		t.Fatalf("alerts = %d, want 2: %+v", len(got), got)
	}
	if got[0].EventID != "fusion-empty:Amber" || !strings.Contains(got[0].Title, "Amber") || !strings.Contains(got[0].Description, "Fusion services") {
		t.Errorf("alert = %+v", got[0])
	}
	if got[1].EventID != "fusion-empty:Karen" {
		t.Errorf("second alert = %+v", got[1])
	}
}
