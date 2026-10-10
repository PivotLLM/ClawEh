package agent

import (
	"reflect"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestSessionChannelsForAgent_FiltersByAgentID verifies the per-agent channel
// scoping primitive used by /status. Channels bound to OTHER agents must
// never appear; channels bound to THIS agent appear once each, in binding
// order, with original casing preserved.
func TestSessionChannelsForAgent_FiltersByAgentID(t *testing.T) {
	bindings := []config.AgentBinding{
		{AgentID: "alice", Match: config.BindingMatch{Channel: "telegram-Alice"}},
		{AgentID: "bob", Match: config.BindingMatch{Channel: "telegram-Bob"}},
		{AgentID: "agent3", Match: config.BindingMatch{Channel: "telegram-Agent3"}},
		{AgentID: "bob", Match: config.BindingMatch{Channel: "slack"}},
		{AgentID: "bob", Match: config.BindingMatch{Channel: "webui"}},
		// Duplicate channel for same agent (different peer) — must be deduped.
		{AgentID: "bob", Match: config.BindingMatch{Channel: "Slack"}},
	}

	got := sessionChannelsForAgent(bindings, "bob")
	want := []string{"telegram-Bob", "slack", "webui"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionChannelsForAgent(bob) = %v, want %v", got, want)
	}

	// Single-channel agent — only its one channel returned, no leakage.
	gotAlice := sessionChannelsForAgent(bindings, "alice")
	wantAlice := []string{"telegram-Alice"}
	if !reflect.DeepEqual(gotAlice, wantAlice) {
		t.Errorf("sessionChannelsForAgent(alice) = %v, want %v", gotAlice, wantAlice)
	}

	// Unknown agent — empty (no fallback to global list).
	if got := sessionChannelsForAgent(bindings, "ghost"); len(got) != 0 {
		t.Errorf("sessionChannelsForAgent(ghost) = %v, want empty", got)
	}
}
