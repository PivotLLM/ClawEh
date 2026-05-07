// ClawEh
// License: MIT

package acl_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
)

func TestDefault_AllowsAnyAgentAnyTool(t *testing.T) {
	cases := []struct{ agent, tool string }{
		{"alice", "read_file"},
		{"alice", "write_file"},
		{"bob", "read_file"},
		{"unknown", "any"},
		{"", ""},
	}
	for _, c := range cases {
		if !acl.Default.IsAllowed(c.agent, c.tool) {
			t.Errorf("Default policy denied (%q, %q); expected allow", c.agent, c.tool)
		}
	}
}

func TestPolicyFunc_DelegatesToFunction(t *testing.T) {
	calls := 0
	p := acl.PolicyFunc(func(agent, tool string) bool {
		calls++
		return agent == "alice" && tool == "read_file"
	})

	if !p.IsAllowed("alice", "read_file") {
		t.Error("expected (alice, read_file) to be allowed")
	}
	if p.IsAllowed("bob", "read_file") {
		t.Error("expected (bob, read_file) to be denied")
	}
	if calls != 2 {
		t.Errorf("expected 2 invocations of underlying func, got %d", calls)
	}
}
