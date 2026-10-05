// ClawEh
// License: MIT

package report

import (
	"strings"
	"testing"
)

// An agent's deny_tools list is shown in its Settings table, and the
// per-agent tool checks the assessment relies on honour it.
func TestCollectAgents_DenyTools(t *testing.T) {
	cfg, env := fixtureConfig(t)
	for i := range cfg.Agents.List {
		if cfg.Agents.List[i].ID == "bob" {
			cfg.Agents.List[i].DenyTools = []string{"shell_exec", "file_*"}
		}
	}

	s := collectAgents(t.Context(), cfg, env)

	bob := agentSub(t, s, "bob")
	_, r := findRow(t, findTable(t, bob, "Settings"), "Denied tools")
	if r[1] != "shell_exec\nfile_*" {
		t.Errorf("denied tools cell = %q", r[1])
	}

	alice := agentSub(t, s, "alice")
	for _, r := range findTable(t, alice, "Settings").Rows {
		if strings.HasPrefix(r[0], "Denied tools") {
			t.Errorf("alice has no deny_tools; row must be absent, got %v", r)
		}
	}

	bobCfg := cfg.Agents.List[1]
	if bobCfg.ID != "bob" {
		t.Fatalf("fixture order changed: %q", bobCfg.ID)
	}
	if agentHasTool(&bobCfg, "shell_exec") {
		t.Error("agentHasTool must honour deny_tools for shell_exec")
	}
	if agentHasTool(&bobCfg, "file_read") {
		t.Error("agentHasTool must honour a deny_tools prefix pattern")
	}
}
