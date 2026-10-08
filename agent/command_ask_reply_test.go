package agent

import (
	"errors"
	"fmt"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/tools"
)

// The /ask reply shows a person's outcome (unreachable chat, cancelled,
// timed out) as is, and prefixes an agent's answer with its name.
func TestCommandAskReply_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply tools.AgentReply
		want  string
	}{
		{"unreachable", tools.AgentReply{Outcome: tools.OutcomePersonUnreachable, Text: "Couldn't reach Bob's chat."}, "Couldn't reach Bob's chat."},
		{"person cancelled", tools.AgentReply{Outcome: tools.OutcomePersonCancelled, Text: "Bob cancelled the request."}, "Bob cancelled the request."},
		{"ok", tools.AgentReply{Outcome: bus.OutcomeOK, Text: "pong"}, "Bob: pong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandAskReply("Bob", tc.reply, nil); got != tc.want {
				t.Fatalf("commandAskReply = %q, want %q", got, tc.want)
			}
		})
	}
}

// An ask that fails is one plain sentence naming the agent; the underlying
// error is never relayed to the chat.
func TestCommandAskReply_ErrorsArePlain(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"queue failure", fmt.Errorf("queue the message: %w", errors.New("bus: context deadline exceeded")), "Could not ask Bob."},
		{"depth", fmt.Errorf("ask: %w", tools.ErrMaxDepth), "Could not ask Bob: the maximum sub-agent depth is reached."},
		{"loop", fmt.Errorf("ask: %w", tools.ErrAskLoop), "Could not ask Bob: it is waiting for a reply in this exchange."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandAskReply("Bob", tools.AgentReply{}, tc.err); got != tc.want {
				t.Fatalf("commandAskReply = %q, want %q", got, tc.want)
			}
		})
	}
}
