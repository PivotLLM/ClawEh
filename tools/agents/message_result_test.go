// ClawEh
// License: MIT

package agents

import (
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/tools"
)

func TestAskResult_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reply       tools.AgentReply
		want        string
		wantErr     bool
		wantRefusal bool
	}{
		{"ok", tools.AgentReply{Outcome: bus.OutcomeOK, Text: "pong"}, "pong", false, false},
		{"timeout", tools.AgentReply{Outcome: tools.OutcomeTimeout, Text: "Bob did not reply within 5 seconds."}, "Bob did not reply within 5 seconds.", false, false},
		{"person cancelled", tools.AgentReply{Outcome: tools.OutcomePersonCancelled, Text: "Bob cancelled the request."}, "Bob cancelled the request.", false, false},
		{"chat unreachable", tools.AgentReply{Outcome: tools.OutcomePersonUnreachable, Text: "Couldn't reach Bob's chat."}, "Couldn't reach Bob's chat.", true, true},
		{"turn cancelled", tools.AgentReply{Outcome: bus.OutcomeCancelled, Text: "Cancelled by /cancel before it started."}, "Bob's turn was cancelled before it replied.", false, false},
		{"error", tools.AgentReply{Outcome: bus.OutcomeError, Text: "boom"}, "Bob's turn failed: boom", true, false},
		{"empty", tools.AgentReply{Outcome: bus.OutcomeEmpty}, "Bob gave no reply.", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := askResult("Bob", tc.reply)
			if got.ForLLM != tc.want || got.IsError != tc.wantErr {
				t.Fatalf("askResult = %+v, want %q (error %v)", got, tc.want, tc.wantErr)
			}
			// A person who cannot be reached is an expected refusal (logged at
			// WARN); a failed turn is an error.
			if refused := tools.IsExpectedRefusal(got.Err); refused != tc.wantRefusal {
				t.Fatalf("IsExpectedRefusal(Err) = %v, want %v", refused, tc.wantRefusal)
			}
		})
	}
}
