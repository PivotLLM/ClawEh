package routing

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func testConfig(agents []config.AgentConfig, bindings []config.AgentBinding) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: "/tmp/claw-test",
			Defaults: config.AgentDefaults{
				Models: []string{"gpt-4"},
			},
			List: agents,
		},
		Bindings: bindings,
	}
}

func TestResolveRoute_DefaultAgent_NoBindings(t *testing.T) {
	cfg := testConfig(nil, nil)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "telegram",
		Peer:    &RoutePeer{Kind: "direct", ID: "user1"},
	})

	if route.AgentID != DefaultAgentID {
		t.Errorf("AgentID = %q, want %q", route.AgentID, DefaultAgentID)
	}
	if route.MatchedBy != "default" {
		t.Errorf("MatchedBy = %q, want 'default'", route.MatchedBy)
	}
}

func TestResolveRoute_PeerBinding(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "sales", Default: true},
		{ID: "support"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "support",
			Match: config.BindingMatch{
				Channel:   "telegram",
				AccountID: "*",
				Peer:      &config.PeerMatch{Kind: "direct", ID: "user123"},
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "telegram",
		Peer:    &RoutePeer{Kind: "direct", ID: "user123"},
	})

	if route.AgentID != "support" {
		t.Errorf("AgentID = %q, want 'support'", route.AgentID)
	}
	if route.MatchedBy != "binding.peer" {
		t.Errorf("MatchedBy = %q, want 'binding.peer'", route.MatchedBy)
	}
}

func TestResolveRoute_GuildBinding(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "general", Default: true},
		{ID: "gaming"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "gaming",
			Match: config.BindingMatch{
				Channel:   "discord",
				AccountID: "*",
				GuildID:   "guild-abc",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "discord",
		GuildID: "guild-abc",
		Peer:    &RoutePeer{Kind: "channel", ID: "ch1"},
	})

	if route.AgentID != "gaming" {
		t.Errorf("AgentID = %q, want 'gaming'", route.AgentID)
	}
	if route.MatchedBy != "binding.guild" {
		t.Errorf("MatchedBy = %q, want 'binding.guild'", route.MatchedBy)
	}
}

func TestResolveRoute_TeamBinding(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "general", Default: true},
		{ID: "work"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "work",
			Match: config.BindingMatch{
				Channel:   "slack",
				AccountID: "*",
				TeamID:    "T12345",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "slack",
		TeamID:  "T12345",
		Peer:    &RoutePeer{Kind: "channel", ID: "C001"},
	})

	if route.AgentID != "work" {
		t.Errorf("AgentID = %q, want 'work'", route.AgentID)
	}
	if route.MatchedBy != "binding.team" {
		t.Errorf("MatchedBy = %q, want 'binding.team'", route.MatchedBy)
	}
}

func TestResolveRoute_AccountBinding(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "default-agent", Default: true},
		{ID: "premium"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "premium",
			Match: config.BindingMatch{
				Channel:   "telegram",
				AccountID: "bot2",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel:   "telegram",
		AccountID: "bot2",
		Peer:      &RoutePeer{Kind: "direct", ID: "user1"},
	})

	if route.AgentID != "premium" {
		t.Errorf("AgentID = %q, want 'premium'", route.AgentID)
	}
	if route.MatchedBy != "binding.account" {
		t.Errorf("MatchedBy = %q, want 'binding.account'", route.MatchedBy)
	}
}

func TestResolveRoute_ChannelWildcard(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "telegram-bot"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "telegram-bot",
			Match: config.BindingMatch{
				Channel:   "telegram",
				AccountID: "*",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "telegram",
		Peer:    &RoutePeer{Kind: "direct", ID: "user1"},
	})

	if route.AgentID != "telegram-bot" {
		t.Errorf("AgentID = %q, want 'telegram-bot'", route.AgentID)
	}
	if route.MatchedBy != "binding.channel" {
		t.Errorf("MatchedBy = %q, want 'binding.channel'", route.MatchedBy)
	}
}

func TestResolveRoute_PriorityOrder_PeerBeatsGuild(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "general", Default: true},
		{ID: "vip"},
		{ID: "gaming"},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "vip",
			Match: config.BindingMatch{
				Channel:   "discord",
				AccountID: "*",
				Peer:      &config.PeerMatch{Kind: "direct", ID: "user-vip"},
			},
		},
		{
			AgentID: "gaming",
			Match: config.BindingMatch{
				Channel:   "discord",
				AccountID: "*",
				GuildID:   "guild-1",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "discord",
		GuildID: "guild-1",
		Peer:    &RoutePeer{Kind: "direct", ID: "user-vip"},
	})

	if route.AgentID != "vip" {
		t.Errorf("AgentID = %q, want 'vip' (peer should beat guild)", route.AgentID)
	}
	if route.MatchedBy != "binding.peer" {
		t.Errorf("MatchedBy = %q, want 'binding.peer'", route.MatchedBy)
	}
}

func TestResolveRoute_InvalidAgentFallsToDefault(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "main", Default: true},
	}
	bindings := []config.AgentBinding{
		{
			AgentID: "nonexistent",
			Match: config.BindingMatch{
				Channel:   "telegram",
				AccountID: "*",
			},
		},
	}
	cfg := testConfig(agents, bindings)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "telegram",
	})

	if route.AgentID != "main" {
		t.Errorf("AgentID = %q, want 'main' (invalid agent should fall to default)", route.AgentID)
	}
}

func TestResolveRoute_DefaultAgentSelection(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "alpha"},
		{ID: "beta", Default: true},
		{ID: "gamma"},
	}
	cfg := testConfig(agents, nil)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "cli",
	})

	if route.AgentID != "beta" {
		t.Errorf("AgentID = %q, want 'beta' (marked as default)", route.AgentID)
	}
}

func TestResolveRoute_NoDefaultUsesFirst(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "alpha"},
		{ID: "beta"},
	}
	cfg := testConfig(agents, nil)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(RouteInput{
		Channel: "cli",
	})

	if route.AgentID != "alpha" {
		t.Errorf("AgentID = %q, want 'alpha' (first in list)", route.AgentID)
	}
}

// A peer binding with a kind and no id (the Bindings page's "Direct
// messages") matches every peer of that kind on its channel, below a binding
// naming the peer itself and above channel-wide bindings and the default.
func TestResolveRoute_PeerKindBinding(t *testing.T) {
	agents := []config.AgentConfig{{ID: "main", Default: true}, {ID: "alice"}, {ID: "bob"}}
	dmBinding := config.AgentBinding{AgentID: "alice", Match: config.BindingMatch{Channel: "slack", AccountID: "*", Peer: &config.PeerMatch{Kind: "direct", ID: ""}}}
	specific := config.AgentBinding{AgentID: "bob", Match: config.BindingMatch{Channel: "slack", AccountID: "*", Peer: &config.PeerMatch{Kind: "direct", ID: "U2"}}}
	wholeChannel := config.AgentBinding{AgentID: "bob", Match: config.BindingMatch{Channel: "slack", AccountID: "*"}}
	dm := func(id string) RouteInput {
		return RouteInput{Channel: "slack", Peer: &RoutePeer{Kind: "direct", ID: id}}
	}
	channelMsg := RouteInput{Channel: "slack", Peer: &RoutePeer{Kind: "channel", ID: "C1"}}

	for _, tc := range []struct {
		name      string
		bindings  []config.AgentBinding
		input     RouteInput
		agent     string
		matchedBy string
	}{
		{"direct message routes to the kind binding", []config.AgentBinding{dmBinding}, dm("U1"), "alice", "binding.peer.kind"},
		{"specific peer binding wins", []config.AgentBinding{dmBinding, specific}, dm("U2"), "bob", "binding.peer"},
		{"other direct message still to the kind binding", []config.AgentBinding{dmBinding, specific}, dm("U1"), "alice", "binding.peer.kind"},
		{"kind binding beats a whole-channel binding", []config.AgentBinding{wholeChannel, dmBinding}, dm("U1"), "alice", "binding.peer.kind"},
		{"channel message unaffected, falls to default", []config.AgentBinding{dmBinding}, channelMsg, "main", "default"},
		{"channel message unaffected, whole-channel binding", []config.AgentBinding{dmBinding, wholeChannel}, channelMsg, "bob", "binding.channel"},
		{"other channel unaffected", []config.AgentBinding{dmBinding}, RouteInput{Channel: "telegram", Peer: &RoutePeer{Kind: "direct", ID: "U1"}}, "main", "default"},
		{"no binding goes to default", nil, dm("U1"), "main", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := NewRouteResolver(testConfig(agents, tc.bindings)).ResolveRoute(tc.input)
			if route.AgentID != tc.agent || route.MatchedBy != tc.matchedBy {
				t.Fatalf("route = %s by %s, want %s by %s", route.AgentID, route.MatchedBy, tc.agent, tc.matchedBy)
			}
		})
	}
}

// A kind-only direct binding outranks a guild, team or account binding: a
// Discord DM sent where a guild binding also applies goes to the DM binding,
// whatever order the bindings are listed in.
func TestResolveRoute_PeerKindBeatsGuildTeamAccount(t *testing.T) {
	agents := []config.AgentConfig{{ID: "main", Default: true}, {ID: "alice"}, {ID: "bob"}}
	dmBinding := func(ch string) config.AgentBinding {
		return config.AgentBinding{AgentID: "alice", Match: config.BindingMatch{Channel: ch, AccountID: "*", Peer: &config.PeerMatch{Kind: "direct"}}}
	}
	guild := config.AgentBinding{AgentID: "bob", Match: config.BindingMatch{Channel: "discord", AccountID: "*", GuildID: "guild1"}}
	team := config.AgentBinding{AgentID: "bob", Match: config.BindingMatch{Channel: "slack", AccountID: "*", TeamID: "team1"}}
	account := config.AgentBinding{AgentID: "bob", Match: config.BindingMatch{Channel: "discord", AccountID: "acct1"}}

	for _, tc := range []struct {
		name     string
		bindings []config.AgentBinding
		input    RouteInput
	}{
		{
			"guild binding listed first",
			[]config.AgentBinding{guild, dmBinding("discord")},
			RouteInput{Channel: "discord", GuildID: "guild1", Peer: &RoutePeer{Kind: "direct", ID: "U1"}},
		},
		{
			"team binding listed first",
			[]config.AgentBinding{team, dmBinding("slack")},
			RouteInput{Channel: "slack", TeamID: "team1", Peer: &RoutePeer{Kind: "direct", ID: "U1"}},
		},
		{
			"account binding listed first",
			[]config.AgentBinding{account, {AgentID: "alice", Match: config.BindingMatch{Channel: "discord", AccountID: "acct1", Peer: &config.PeerMatch{Kind: "direct"}}}},
			RouteInput{Channel: "discord", AccountID: "acct1", Peer: &RoutePeer{Kind: "direct", ID: "U1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := NewRouteResolver(testConfig(agents, tc.bindings)).ResolveRoute(tc.input)
			if route.AgentID != "alice" || route.MatchedBy != "binding.peer.kind" {
				t.Fatalf("route = %s by %s, want alice by binding.peer.kind", route.AgentID, route.MatchedBy)
			}
		})
	}

	// The guild, team and account bindings still take a non-direct message.
	for _, tc := range []struct {
		name      string
		bindings  []config.AgentBinding
		input     RouteInput
		matchedBy string
	}{
		{
			"guild message",
			[]config.AgentBinding{dmBinding("discord"), guild},
			RouteInput{Channel: "discord", GuildID: "guild1", Peer: &RoutePeer{Kind: "channel", ID: "C1"}},
			"binding.guild",
		},
		{
			"team message",
			[]config.AgentBinding{dmBinding("slack"), team},
			RouteInput{Channel: "slack", TeamID: "team1", Peer: &RoutePeer{Kind: "channel", ID: "C1"}},
			"binding.team",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := NewRouteResolver(testConfig(agents, tc.bindings)).ResolveRoute(tc.input)
			if route.AgentID != "bob" || route.MatchedBy != tc.matchedBy {
				t.Fatalf("route = %s by %s, want bob by %s", route.AgentID, route.MatchedBy, tc.matchedBy)
			}
		})
	}
}

// Peer kinds are compared case-insensitively (both sides are lower-cased, as
// for a peer binding with an id): a "Direct" binding matches a "direct" peer
// and a "direct" binding matches a "DIRECT" peer.
func TestResolveRoute_PeerKindCaseInsensitive(t *testing.T) {
	agents := []config.AgentConfig{{ID: "main", Default: true}, {ID: "alice"}}
	for _, tc := range []struct {
		name        string
		bindingKind string
		peerKind    string
	}{
		{"binding kind capitalised", "Direct", "direct"},
		{"peer kind upper case", "direct", "DIRECT"},
		{"binding kind padded", " direct ", "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := []config.AgentBinding{{AgentID: "alice", Match: config.BindingMatch{Channel: "discord", AccountID: "*", Peer: &config.PeerMatch{Kind: tc.bindingKind}}}}
			r := NewRouteResolver(testConfig(agents, bindings))
			input := RouteInput{Channel: "discord", Peer: &RoutePeer{Kind: tc.peerKind, ID: "U1"}}
			route := r.ResolveRoute(input)
			if route.AgentID != "alice" || route.MatchedBy != "binding.peer.kind" {
				t.Fatalf("route = %s by %s, want alice by binding.peer.kind", route.AgentID, route.MatchedBy)
			}
			if !r.Reaches(input, "alice") {
				t.Fatal("Reaches = false, want true")
			}
		})
	}
}
