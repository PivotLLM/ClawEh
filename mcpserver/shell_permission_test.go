// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/shell"
)

// TestDispatch_ShellFollowsTheTokensAgent: a shell_exec call over MCP runs
// when the token's agent has shell_exec, and is refused with one sentence
// naming the agent when it does not, for a session token and a service
// token alike. The channel the session last heard from plays no part.
func TestDispatch_ShellFollowsTheTokensAgent(t *testing.T) {
	exec, err := shell.NewExecTool("", false)
	if err != nil {
		t.Fatal(err)
	}
	alice := newRegistryWith(exec)
	alice.SetOwner("Alice")
	bob := newRegistryWith() // Bob's tool permissions leave out shell_exec
	bob.SetOwner("Bob")
	resolve := resolverFor(map[string]*tools.ToolRegistry{"alice": alice, "bob": bob})

	st := newSessionTokenStore()
	aliceTok := st.Issue("alice", "agent:alice:main", "/ws")
	bobTok := st.Issue("bob", "agent:bob:main", "/ws")
	st.SetSource("agent:alice:main", "telegram", "chat-1")
	st.SetSource("agent:bob:main", "telegram", "chat-2")
	aliceSvc := "SST" + strings.Repeat("ab", 32)
	bobSvc := "SST" + strings.Repeat("cd", 32)
	st.RegisterService(aliceSvc, "alice", "/ws")
	st.RegisterService(bobSvc, "bob", "/ws")

	for _, tc := range []struct {
		name, token, want string
		wantErr           bool
	}{
		{"alice session token", aliceTok, "shell-ok", false},
		{"alice service token", aliceSvc, "shell-ok", false},
		{"bob session token", bobTok, "Bob is not allowed to run shell commands.", true},
		{"bob service token", bobSvc, "Bob is not allowed to run shell commands.", true},
	} {
		out, isErr := dispatchToolCall(context.Background(), "shell_exec",
			map[string]any{"session_token": tc.token, "command": "echo shell-ok"},
			st, resolve, nil, nil, nil, nil)
		if isErr != tc.wantErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: (%q, isErr=%v), want %q isErr=%v", tc.name, out, isErr, tc.want, tc.wantErr)
		}
		if tc.wantErr && out != tc.want {
			t.Errorf("%s: refusal %q, want exactly %q", tc.name, out, tc.want)
		}
	}
}
