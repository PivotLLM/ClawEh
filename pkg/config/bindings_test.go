package config

import "testing"

func peerMatch(id string) *PeerMatch { return &PeerMatch{Kind: "channel", ID: id} }

func cronTestConfig() *Config {
	return &Config{
		Bindings: []AgentBinding{
			{AgentID: "amber", Default: true, Match: BindingMatch{Channel: "telegram-Amber", Peer: peerMatch("c-amber")}},
			{AgentID: "amber", Match: BindingMatch{Channel: "slack", Peer: peerMatch("c-amber2")}},
			{AgentID: "nodefault", Match: BindingMatch{Channel: "slack", Peer: peerMatch("c1")}},
		},
		Agents: AgentsConfig{List: []AgentConfig{
			{ID: "amber"}, {ID: "boss", GlobalCron: true}, {ID: "nodefault"},
		}},
	}
}

func TestDefaultBindingAndCronTarget(t *testing.T) {
	c := cronTestConfig()

	if _, ok := c.DefaultBinding("amber"); !ok {
		t.Fatal("amber should have a default binding")
	}
	ch, chat, kind, ok := c.CronTarget("amber")
	if !ok || ch != "telegram-Amber" || chat != "c-amber" || kind != "channel" {
		t.Fatalf("amber CronTarget = %q/%q/%q ok=%v", ch, chat, kind, ok)
	}

	if _, ok := c.DefaultBinding("nodefault"); ok {
		t.Error("nodefault should have no default binding")
	}
	if _, _, _, ok := c.CronTarget("nodefault"); ok {
		t.Error("nodefault should not resolve a cron target")
	}
	if _, _, _, ok := c.CronTarget("unknown"); ok {
		t.Error("unknown agent should not resolve a cron target")
	}
}

func TestAgentHasGlobalCron(t *testing.T) {
	c := cronTestConfig()
	if !c.AgentHasGlobalCron("boss") {
		t.Error("boss should have global_cron")
	}
	if c.AgentHasGlobalCron("amber") {
		t.Error("amber should not have global_cron")
	}
	if c.AgentHasGlobalCron("unknown") {
		t.Error("unknown agent should not have global_cron")
	}
}

func TestValidateBindings(t *testing.T) {
	// Valid: one concrete default.
	if err := cronTestConfig().ValidateBindings(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Two defaults for one agent → error.
	dup := &Config{Bindings: []AgentBinding{
		{AgentID: "a", Default: true, Match: BindingMatch{Channel: "slack", Peer: peerMatch("c1")}},
		{AgentID: "a", Default: true, Match: BindingMatch{Channel: "slack", Peer: peerMatch("c2")}},
	}}
	if err := dup.ValidateBindings(); err == nil {
		t.Error("expected error for two defaults on one agent")
	}

	// Default without a concrete peer → error.
	noPeer := &Config{Bindings: []AgentBinding{
		{AgentID: "a", Default: true, Match: BindingMatch{Channel: "slack"}},
	}}
	if err := noPeer.ValidateBindings(); err == nil {
		t.Error("expected error for default binding without a concrete peer")
	}
}
