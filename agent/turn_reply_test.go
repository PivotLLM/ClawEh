// ClawEh
// License: MIT

package agent

import (
	"errors"
	"fmt"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/tools"
)

func TestDecideTurnReply(t *testing.T) {
	failure := errors.New("provider exploded")
	notAsked := humanNotAskedError{label: "Bob"}
	unreachable := humanUnreachableError{label: "Bob"}
	tests := []struct {
		name        string
		end         turnEnd
		wantAction  replyAction
		wantText    string
		wantOutcome string
	}{
		{
			name:        "plain reply is ok",
			end:         turnEnd{response: "hello"},
			wantAction:  replySend,
			wantText:    "hello",
			wantOutcome: bus.OutcomeOK,
		},
		{
			name:        "outcome the turn set is kept",
			end:         turnEnd{response: "Sorry", outcome: bus.OutcomeError},
			wantAction:  replySend,
			wantText:    "Sorry",
			wantOutcome: bus.OutcomeError,
		},
		{
			name:        "empty reply",
			end:         turnEnd{},
			wantAction:  replySend,
			wantOutcome: bus.OutcomeEmpty,
		},
		{
			name:        "empty outcome keeps the advice for a person",
			end:         turnEnd{response: "The model returned an empty response.", outcome: bus.OutcomeEmpty},
			wantAction:  replySend,
			wantText:    "The model returned an empty response.",
			wantOutcome: bus.OutcomeEmpty,
		},
		{
			name:        "empty outcome gives a required reply no text",
			end:         turnEnd{response: "The model returned an empty response.", outcome: bus.OutcomeEmpty, replyRequired: true},
			wantAction:  replySend,
			wantOutcome: bus.OutcomeEmpty,
		},
		{
			name:       "agent gone without a required reply is dropped",
			end:        turnEnd{err: fmt.Errorf("%w: alice", errAgentGone), agentID: "alice"},
			wantAction: replyDrop,
		},
		{
			name:        "agent gone with a required reply says so",
			end:         turnEnd{err: fmt.Errorf("%w: alice", errAgentGone), agentID: "alice", replyRequired: true},
			wantAction:  replySend,
			wantText:    "Agent alice no longer exists.",
			wantOutcome: bus.OutcomeError,
		},
		{
			name:       "human not asked from claw is dropped",
			end:        turnEnd{err: notAsked},
			wantAction: replyDrop,
		},
		{
			name:        "human not asked from a person is told",
			end:         turnEnd{err: notAsked, fromPerson: true},
			wantAction:  replySend,
			wantText:    "Bob only answers questions from agents.",
			wantOutcome: bus.OutcomeError,
		},
		{
			name:        "human not asked with a required reply is told",
			end:         turnEnd{err: notAsked, replyRequired: true},
			wantAction:  replySend,
			wantText:    "Bob only answers questions from agents.",
			wantOutcome: bus.OutcomeError,
		},
		{
			name:       "asked turn cancelled because the asker stopped",
			end:        turnEnd{err: failure, ask: true, cause: errAskerStopped},
			wantAction: replyDropAskCancelled,
		},
		{
			name:       "asker stopped waiting for a person",
			end:        turnEnd{err: errAskerStopped, ask: true},
			wantAction: replyDropPersonAskerStopped,
		},
		{
			name:        "person cancelled",
			end:         turnEnd{err: errHumanCancelled},
			wantAction:  replySend,
			wantText:    errHumanCancelled.Error(),
			wantOutcome: bus.OutcomeCancelled,
		},
		{
			name:        "person unreachable",
			end:         turnEnd{err: unreachable},
			wantAction:  replySend,
			wantText:    "Couldn't reach Bob's chat.",
			wantOutcome: bus.OutcomeError,
		},
		{
			name:        "cancelled by /cancel",
			end:         turnEnd{err: failure, cause: errCancelledByUser},
			wantAction:  replySend,
			wantText:    "⚠️ Cancelled by /cancel. Some steps may have completed — ask me to continue if needed.",
			wantOutcome: bus.OutcomeCancelled,
		},
		{
			name:        "asker stopped on a turn that is not an ask is a failure",
			end:         turnEnd{err: failure, cause: errAskerStopped},
			wantAction:  replyTurnError,
			wantOutcome: bus.OutcomeError,
		},
		{
			name:        "failure",
			end:         turnEnd{err: failure},
			wantAction:  replyTurnError,
			wantOutcome: bus.OutcomeError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, text, outcome := decideTurnReply(tt.end)
			if action != tt.wantAction || text != tt.wantText || outcome != tt.wantOutcome {
				t.Errorf("decideTurnReply() = (%d, %q, %q), want (%d, %q, %q)",
					action, text, outcome, tt.wantAction, tt.wantText, tt.wantOutcome)
			}
		})
	}
}

func TestAskReplyOutcome(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome string
		want    string
	}{
		{name: "ok", outcome: bus.OutcomeOK, want: bus.OutcomeOK},
		{name: "failure keeps the turn's outcome", err: errors.New("boom"), outcome: bus.OutcomeError, want: bus.OutcomeError},
		{name: "person cancelled", err: errHumanCancelled, outcome: bus.OutcomeCancelled, want: tools.OutcomePersonCancelled},
		{name: "person unreachable", err: humanUnreachableError{label: "Bob"}, outcome: bus.OutcomeError, want: tools.OutcomePersonUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := askReplyOutcome(tt.err, tt.outcome); got != tt.want {
				t.Errorf("askReplyOutcome() = %q, want %q", got, tt.want)
			}
		})
	}
}
