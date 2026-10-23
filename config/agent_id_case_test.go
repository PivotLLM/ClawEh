// ClawEh
// License: MIT

package config

import (
	"path/filepath"
	"slices"
	"testing"
)

// An agent listed as "Bob" is the agent every lower-case reference names:
// bindings, allow_agents, the human-agent rules and the default binding all
// compare identities, so mixing cases breaks nothing.
func TestAgentIDCaseInsensitiveReferences(t *testing.T) {
	cfg := humanTestConfig()
	cfg.Agents.List[0].ID = "Alice"
	cfg.Agents.List[0].Subagents = &SubagentsConfig{AllowAgents: []string{"bob"}}
	cfg.Agents.List[1].ID = "Bob"
	cfg.Bindings[0].AgentID = "alice"
	cfg.Bindings[1].AgentID = "bob"

	if errs := cfg.AgentIDErrors(); len(errs) != 0 {
		t.Fatalf("AgentIDErrors = %v", errs)
	}
	if ps := cfg.HumanProblems(); len(ps) != 0 {
		t.Fatalf("HumanProblems = %v, want none", ps)
	}
	if err := cfg.ValidateBindings(); err != nil {
		t.Fatalf("ValidateBindings = %v", err)
	}
	for _, id := range []string{"Bob", "bob", "BOB"} {
		if a := cfg.AgentByID(id); a == nil || a.ID != "Bob" {
			t.Errorf("AgentByID(%q) = %v, want Bob", id, a)
		}
		if !cfg.IsHumanAgent(id) {
			t.Errorf("IsHumanAgent(%q) = false", id)
		}
		if ch, chat, _, ok := cfg.CronTarget(id); !ok || ch != "telegram-main" || chat != "4242" {
			t.Errorf("CronTarget(%q) = %q %q %v", id, ch, chat, ok)
		}
		if !cfg.Agents.List[0].Subagents.Allows(id) {
			t.Errorf("allow_agents [bob] does not allow %q", id)
		}
	}
	if cfg.AgentByID("") != nil {
		t.Error("AgentByID(\"\") found an agent")
	}

	// A second default binding for the same agent in another case is refused.
	cfg.Bindings = append(cfg.Bindings, AgentBinding{
		AgentID: "BOB", Default: true,
		Match: BindingMatch{Channel: "telegram-main", Peer: &PeerMatch{Kind: "direct", ID: "4343"}},
	})
	if err := cfg.ValidateBindings(); err == nil {
		t.Error("two default bindings for Bob and BOB accepted")
	}
}

// The session folders are the agent's identity: "Bob" keeps its sessions in
// <base>/bob, where a lower-case install kept them, and "Main" in
// <base>/default.
func TestAgentSessionDirsAreLowerCase(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.List = []AgentConfig{{ID: "Bob"}, {ID: "Main"}}
	base := cfg.BaseDir()
	want := []string{filepath.Join(base, "bob", "sessions"), filepath.Join(base, "default", "sessions")}
	if got := cfg.AgentSessionDirs(); !slices.Equal(got, want) {
		t.Errorf("AgentSessionDirs = %v, want %v", got, want)
	}
}
