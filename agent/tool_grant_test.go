// ClawEh
// License: MIT

package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

// TestAgentTools_OnlyWhatTheListNames: an agent gets only the native tools its
// tools list names. No tools key and an empty list both give none, so its
// registry, which the loop offers the model and the MCP host serves to its
// session and service tokens, is empty. Suites are off here: they have their
// own switches.
func TestAgentTools_OnlyWhatTheListNames(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	off := false
	cfg := messagingConfig(t)
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Cogmem = &off
	}
	cfg.Agents.List[0].Tools = nil        // alice: no tools key
	cfg.Agents.List[1].Tools = []string{} // bob: empty list
	cfg.Agents.List[2].Tools = []string{"agent_message"}
	al, _ := messagingLoop(t, cfg, &countingProvider{})

	for _, id := range []string{"alice", "bob"} {
		inst, _ := al.GetRegistry().Get(id)
		if n := inst.Tools.Count(); n != 0 {
			t.Errorf("%s has %d tools, want none: %v", id, n, inst.Tools.List())
		}
		if _, ok := inst.Tools.GetForHost("agent_message"); ok {
			t.Errorf("%s reaches agent_message through the MCP host", id)
		}
		res := inst.Tools.ExecuteForHost(context.Background(), "agent_message",
			map[string]any{"agent": "bob", "message": "hi"}, "cli", "direct", nil)
		if !res.IsError {
			t.Errorf("%s ran agent_message through the MCP host: %+v", id, res)
		}
	}
	helper, _ := al.GetRegistry().Get("helper")
	if got := helper.Tools.List(); !slices.Equal(got, []string{"agent_message"}) {
		t.Errorf("helper tools = %v, want exactly [agent_message]", got)
	}
}
