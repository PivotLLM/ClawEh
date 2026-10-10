package routing

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestReaches: a sender reaches an agent its message routes to (binding or
// default), or one a matching binding names as its agent or mention target;
// never one bound only to another chat, channel or account.
func TestReaches(t *testing.T) {
	agents := []config.AgentConfig{{ID: "main", Default: true}, {ID: "alice"}, {ID: "bob"}, {ID: "agent3"}}
	bindings := []config.AgentBinding{
		{AgentID: "alice", Match: config.BindingMatch{Channel: "telegram", AccountID: "*", Peer: &config.PeerMatch{Kind: "direct", ID: "u1"}}},
		{AgentID: "bob", Match: config.BindingMatch{Channel: "telegram", AccountID: "*", Peer: &config.PeerMatch{Kind: "group", ID: "g1"}}},
		{AgentID: "main", AgentMentions: []string{"agent3"}, Match: config.BindingMatch{Channel: "slack", AccountID: "*"}},
		{AgentID: "main", AgentMentions: []string{"*"}, Match: config.BindingMatch{Channel: "discord", GuildID: "guild1"}},
	}
	r := NewRouteResolver(testConfig(agents, bindings))
	direct := func(ch, id string) RouteInput {
		return RouteInput{Channel: ch, Peer: &RoutePeer{Kind: "direct", ID: id}}
	}

	for _, tc := range []struct {
		name  string
		input RouteInput
		agent string
		want  bool
	}{
		{"peer binding", direct("telegram", "u1"), "alice", true},
		{"peer binding, other agent", direct("telegram", "u1"), "bob", false},
		{"other sender falls to default", direct("telegram", "u2"), "main", true},
		{"other sender, peer-bound agent", direct("telegram", "u2"), "alice", false},
		{"group binding via parent peer", RouteInput{Channel: "telegram", Peer: &RoutePeer{Kind: "direct", ID: "u9"}, ParentPeer: &RoutePeer{Kind: "group", ID: "g1"}}, "bob", true},
		{"listed mention", direct("slack", "u1"), "agent3", true},
		{"unlisted mention", direct("slack", "u1"), "bob", false},
		{"wildcard mention in matching guild", RouteInput{Channel: "discord", GuildID: "guild1"}, "bob", true},
		{"wildcard mention, other guild", RouteInput{Channel: "discord", GuildID: "guild2"}, "bob", false},
		{"mentioned agent is ignored", RouteInput{Channel: "telegram", Peer: &RoutePeer{Kind: "direct", ID: "u2"}, MentionedAgent: "alice"}, "alice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.Reaches(tc.input, tc.agent); got != tc.want {
				t.Fatalf("Reaches(%+v, %q) = %v, want %v", tc.input, tc.agent, got, tc.want)
			}
		})
	}
}

// Agent ids are case-insensitive: an agent listed as "Bob" is the one a
// binding, a mention or a Reaches target names in any case, and routes to its
// lower-case identity.
func TestRouting_CaseInsensitiveAgentIDs(t *testing.T) {
	agents := []config.AgentConfig{{ID: "Main", Default: true}, {ID: "Alice"}, {ID: "Bob"}}
	bindings := []config.AgentBinding{
		{AgentID: "alice", Match: config.BindingMatch{Channel: "telegram", AccountID: "*"}},
		{AgentID: "MAIN", AgentMentions: []string{"bob"}, Match: config.BindingMatch{Channel: "slack", AccountID: "*"}},
	}
	r := NewRouteResolver(testConfig(agents, bindings))

	route := r.ResolveRoute(RouteInput{Channel: "telegram", Peer: &RoutePeer{Kind: "direct", ID: "u1"}})
	if route.AgentID != "alice" || route.SessionKey != "agent:alice:main" {
		t.Errorf("telegram route = %q %q, want alice", route.AgentID, route.SessionKey)
	}
	route = r.ResolveRoute(RouteInput{Channel: "slack", Peer: &RoutePeer{Kind: "direct", ID: "u1"}, MentionedAgent: "Bob"})
	if route.AgentID != "bob" {
		t.Errorf("mention of Bob routed to %q, want bob", route.AgentID)
	}
	route = r.ResolveRoute(RouteInput{Channel: "discord"})
	if route.AgentID != "main" {
		t.Errorf("default route = %q, want main", route.AgentID)
	}
	if !r.Reaches(RouteInput{Channel: "slack"}, "BOB") {
		t.Error("slack does not reach BOB through agent_mentions [bob]")
	}
}
