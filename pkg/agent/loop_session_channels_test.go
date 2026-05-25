package agent

import (
	"reflect"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestSessionChannelsForAgent_FiltersByAgentID verifies the per-agent channel
// scoping primitive used by /status. Channels bound to OTHER agents must
// never appear; channels bound to THIS agent appear once each, in binding
// order, with original casing preserved.
func TestSessionChannelsForAgent_FiltersByAgentID(t *testing.T) {
	bindings := []config.AgentBinding{
		{AgentID: "amber", Match: config.BindingMatch{Channel: "telegram-Amber"}},
		{AgentID: "dawn", Match: config.BindingMatch{Channel: "telegram-Dawn"}},
		{AgentID: "karen", Match: config.BindingMatch{Channel: "telegram-Karen"}},
		{AgentID: "dawn", Match: config.BindingMatch{Channel: "slack"}},
		{AgentID: "dawn", Match: config.BindingMatch{Channel: "webui"}},
		// Duplicate channel for same agent (different peer) — must be deduped.
		{AgentID: "dawn", Match: config.BindingMatch{Channel: "Slack"}},
	}

	got := sessionChannelsForAgent(bindings, "dawn")
	want := []string{"telegram-Dawn", "slack", "webui"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionChannelsForAgent(dawn) = %v, want %v", got, want)
	}

	// Single-channel agent — only its one channel returned, no leakage.
	gotAmber := sessionChannelsForAgent(bindings, "amber")
	wantAmber := []string{"telegram-Amber"}
	if !reflect.DeepEqual(gotAmber, wantAmber) {
		t.Errorf("sessionChannelsForAgent(amber) = %v, want %v", gotAmber, wantAmber)
	}

	// Unknown agent — empty (no fallback to global list).
	if got := sessionChannelsForAgent(bindings, "ghost"); len(got) != 0 {
		t.Errorf("sessionChannelsForAgent(ghost) = %v, want empty", got)
	}
}

// TestSessionChannelsForAgent_NormalizesAgentID confirms that the comparison
// uses the routing package's normaliser, so case/whitespace differences
// between binding.AgentID and the live agent.ID don't cause false negatives
// (which would silently widen the leak window).
func TestSessionChannelsForAgent_NormalizesAgentID(t *testing.T) {
	bindings := []config.AgentBinding{
		{AgentID: "  DAWN ", Match: config.BindingMatch{Channel: "slack"}},
	}
	got := sessionChannelsForAgent(bindings, "dawn")
	if len(got) != 1 || got[0] != "slack" {
		t.Errorf("normalised match failed: got %v, want [slack]", got)
	}
}
