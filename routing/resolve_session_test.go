package routing

import "testing"

func TestAgentIDFromSessionKey(t *testing.T) {
	cases := map[string]string{
		"agent:alice:main":          "alice",
		"agent:bob:device:abc123":   "bob",
		"agent:carol:slack:profile": "carol",
		"main":                      "", // node-client sentinel
		"agent:main:device:x":       "", // the sentinel in agent position
		"":                          "",
		"not-a-key":                 "",
		"agent:":                    "",
	}
	for in, want := range cases {
		if got := AgentIDFromSessionKey(in); got != want {
			t.Errorf("AgentIDFromSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveAgentSessionKey: an agent has one persistent conversation. Every
// requested key resolves to agent:<id>:main except the agent's own sub-agent
// session, which is kept.
func TestResolveAgentSessionKey(t *testing.T) {
	tests := []struct {
		name      string
		agentID   string
		requested string
		want      string
	}{
		{"empty", "alice", "", "agent:alice:main"},
		{"main", "alice", "agent:alice:main", "agent:alice:main"},
		{"not agent-scoped", "alice", "cli:default", "agent:alice:main"},
		{"node sentinel", "alice", "main", "agent:alice:main"},
		{"old direct key", "alice", "agent:alice:direct:carol", "agent:alice:main"},
		{"old platform key", "alice", "agent:alice:telegram:direct:555", "agent:alice:main"},
		{"old group key", "alice", "agent:alice:slack:channel:c1", "agent:alice:main"},
		{"old device key", "alice", "agent:alice:device:dev1", "agent:alice:main"},
		{"old service key", "alice", "agent:alice:service", "agent:alice:main"},
		{"another agent's main", "alice", "agent:bob:main", "agent:alice:main"},
		{"another agent's sub-agent", "alice", "agent:bob:subagent:u1", "agent:alice:main"},
		{"own sub-agent", "alice", "agent:alice:subagent:u1", "agent:alice:subagent:u1"},
		{"agent id normalized", "Sales Bot", "agent:sales-bot:telegram:direct:1", "agent:sales-bot:main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAgentSessionKey(tt.agentID, tt.requested); got != tt.want {
				t.Errorf("ResolveAgentSessionKey(%q, %q) = %q, want %q", tt.agentID, tt.requested, got, tt.want)
			}
		})
	}
}
