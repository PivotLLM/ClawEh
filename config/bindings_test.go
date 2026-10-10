package config

import "testing"

func peerMatch(id string) *PeerMatch { return &PeerMatch{Kind: "channel", ID: id} }

func cronTestConfig() *Config {
	return &Config{
		Bindings: []AgentBinding{
			{AgentID: "alice", Default: true, Match: BindingMatch{Channel: "telegram-Alice", Peer: peerMatch("c-alice")}},
			{AgentID: "alice", Match: BindingMatch{Channel: "slack", Peer: peerMatch("c-alice2")}},
			{AgentID: "nodefault", Match: BindingMatch{Channel: "slack", Peer: peerMatch("c1")}},
		},
		Agents: AgentsConfig{List: []AgentConfig{
			{ID: "alice"}, {ID: "boss", GlobalCron: true}, {ID: "nodefault"},
		}},
	}
}

func TestDefaultBindingAndCronTarget(t *testing.T) {
	c := cronTestConfig()

	if _, ok := c.DefaultBinding("alice"); !ok {
		t.Fatal("alice should have a default binding")
	}
	ch, chat, kind, ok := c.CronTarget("alice")
	if !ok || ch != "telegram-Alice" || chat != "c-alice" || kind != "channel" {
		t.Fatalf("alice CronTarget = %q/%q/%q ok=%v", ch, chat, kind, ok)
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

func TestCronTargetDeliverTo(t *testing.T) {
	// A Telegram-style default: channel-only binding (no peer) + explicit DeliverTo.
	c := &Config{Bindings: []AgentBinding{
		{
			AgentID: "agent3", Default: true, DeliverTo: "12345",
			Match: BindingMatch{Channel: "telegram-Agent3"},
		},
	}}
	ch, chat, kind, ok := c.CronTarget("agent3")
	if !ok || ch != "telegram-Agent3" || chat != "12345" || kind != "direct" {
		t.Fatalf("agent3 CronTarget = %q/%q/%q ok=%v (want telegram-Agent3/12345/direct)", ch, chat, kind, ok)
	}

	// DeliverPeerKind override is honored.
	c.Bindings[0].DeliverPeerKind = "channel"
	if _, _, kind, _ := c.CronTarget("agent3"); kind != "channel" {
		t.Fatalf("DeliverPeerKind override not honored, got %q", kind)
	}

	// A concrete peer takes precedence over DeliverTo.
	c.Bindings[0].Match.Peer = &PeerMatch{Kind: "channel", ID: "C9"}
	if _, chat, _, _ := c.CronTarget("agent3"); chat != "C9" {
		t.Fatalf("concrete peer should win over DeliverTo, got chat %q", chat)
	}
}

func TestValidateBindings_DeliverToSatisfiesDefault(t *testing.T) {
	// A default with no peer but a DeliverTo is valid.
	ok := &Config{Bindings: []AgentBinding{
		{AgentID: "agent3", Default: true, DeliverTo: "12345", Match: BindingMatch{Channel: "telegram-Agent3"}},
	}}
	if err := ok.ValidateBindings(); err != nil {
		t.Fatalf("default with deliver_to should be valid: %v", err)
	}
	// A default with neither peer nor DeliverTo is rejected.
	bad := &Config{Bindings: []AgentBinding{
		{AgentID: "agent3", Default: true, Match: BindingMatch{Channel: "telegram-Agent3"}},
	}}
	if err := bad.ValidateBindings(); err == nil {
		t.Error("default with neither peer nor deliver_to should be rejected")
	}
}

// TestCronTargetCaseInsensitive: the caller's agent id is matched
// case-insensitively against the (normal-form) config ids.
func TestCronTargetCaseInsensitive(t *testing.T) {
	c := &Config{
		Bindings: []AgentBinding{
			{
				AgentID: "bob", Default: true,
				Match: BindingMatch{Channel: "slack", Peer: &PeerMatch{Kind: "channel", ID: "C0AMNPSSQRK"}},
			},
		},
		Agents: AgentsConfig{List: []AgentConfig{{ID: "bob", GlobalCron: true}}},
	}
	if _, _, _, ok := c.CronTarget("Bob"); !ok {
		t.Error("CronTarget should match a 'bob' binding for caller 'Bob'")
	}
	if !c.AgentHasGlobalCron("Bob") {
		t.Error("AgentHasGlobalCron should match 'bob' for caller 'Bob'")
	}
}

func TestAgentHasGlobalCron(t *testing.T) {
	c := cronTestConfig()
	if !c.AgentHasGlobalCron("boss") {
		t.Error("boss should have global_cron")
	}
	if c.AgentHasGlobalCron("alice") {
		t.Error("alice should not have global_cron")
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

	// webui has no durable delivery address → cannot be a default, even with a
	// concrete peer (its peer id is an ephemeral per-browser session).
	webui := &Config{Bindings: []AgentBinding{
		{AgentID: "a", Default: true, Match: BindingMatch{Channel: "webui", Peer: &PeerMatch{Kind: "direct", ID: "webui:abc"}}},
	}}
	if err := webui.ValidateBindings(); err == nil {
		t.Error("expected error for webui default binding")
	}
}
