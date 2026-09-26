// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/providers"
)

// storeWriteFailingContextManager is a ContextManager whose store refuses one
// kind of write: the user message on the way in, or the assistant reply on the
// way out. Everything else behaves like capturingContextManager.
type storeWriteFailingContextManager struct {
	capturingContextManager
	failUser, failAssistant error
}

func (m *storeWriteFailingContextManager) AddUserMessage(ctx context.Context, msg providers.Message) (int64, error) {
	if m.failUser != nil {
		return 0, m.failUser
	}
	return m.capturingContextManager.AddUserMessage(ctx, msg)
}

func (m *storeWriteFailingContextManager) AddAssistantMessage(ctx context.Context, msg providers.Message) (int64, error) {
	if m.failAssistant != nil {
		return 0, m.failAssistant
	}
	return m.capturingContextManager.AddAssistantMessage(ctx, msg)
}

// TestRunAgentLoop_SessionStoreWriteFailureFailsTurn checks that a message the
// session store refuses ends the turn with an error naming the message, raises
// the "Session store write failed" alert, and — for the inbound user message —
// never reaches the model, so no reply is produced from a history the next turn
// will not see.
func TestRunAgentLoop_SessionStoreWriteFailureFailsTurn(t *testing.T) {
	storeErr := errors.New("disk full")
	cases := []struct {
		name          string
		cm            *storeWriteFailingContextManager
		wantInError   string
		wantAssembled bool
	}{
		{"user message", &storeWriteFailingContextManager{failUser: storeErr}, "session store rejected the user message", false},
		{"assistant reply", &storeWriteFailingContextManager{failAssistant: storeErr}, "session store rejected the assistant reply", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			al := newTestAgentLoop(t).al
			rec := &alertRecorder{}
			al.SetAlerter(rec)

			agent := al.registry.GetDefaultAgent()
			if agent == nil {
				t.Fatal("no default agent")
			}
			agent.Provider = &finalLLMProvider{}

			sessionKey := "store-write-" + strings.ReplaceAll(tc.name, " ", "-")
			entry := &cmEntry{cm: tc.cm, sessionKey: sessionKey}
			entry.touch()
			al.contextManagers.Store(agent.ID+":"+sessionKey, entry)

			reply, err := al.runAgentLoop(context.Background(), agent, processOptions{
				SessionKey:  sessionKey,
				Channel:     "cli",
				ChatID:      "direct",
				UserMessage: "hello",
			})
			if err == nil {
				t.Fatalf("runAgentLoop returned %q and no error; want the turn to fail", reply)
			}
			if reply != "" {
				t.Errorf("reply = %q, want none", reply)
			}
			if !strings.Contains(err.Error(), tc.wantInError) || !errors.Is(err, storeErr) {
				t.Errorf("error = %q, want it to contain %q and wrap the store error", err, tc.wantInError)
			}
			if got := tc.cm.assembleAgentID != ""; got != tc.wantAssembled {
				t.Errorf("model request assembled = %v, want %v", got, tc.wantAssembled)
			}

			rec.mu.Lock()
			alerts := append([]alerter.Alert(nil), rec.alerts...)
			rec.mu.Unlock()
			if len(alerts) != 1 {
				t.Fatalf("got %d alerts, want 1: %+v", len(alerts), alerts)
			}
			a := alerts[0]
			if a.Title != "Session store write failed" || a.EventID != "session-store" || a.Priority != alerter.Normal {
				t.Errorf("alert = %+v; want title \"Session store write failed\", id session-store, Normal", a)
			}
			if !strings.Contains(a.Description, tc.name) || a.Details != storeErr.Error() {
				t.Errorf("alert description/details = %q / %q; want the message kind and the store error", a.Description, a.Details)
			}
		})
	}
}
