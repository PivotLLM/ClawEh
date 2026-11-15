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

			agent := al.registry.Default()
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

// TestRunAgentLoop_SessionStoreWriteFailureAlertsOncePerOutage: a store that
// refuses the user message refuses it on every turn, but only the first turn
// alerts; a turn whose messages are recorded and saved ends that, so the next
// refusal alerts again.
func TestRunAgentLoop_SessionStoreWriteFailureAlertsOncePerOutage(t *testing.T) {
	storeErr := errors.New("disk full")
	al := newTestAgentLoop(t).al
	rec := &alertRecorder{}
	al.SetAlerter(rec)
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &finalLLMProvider{}

	const sessionKey = "store-write-outage"
	cm := &storeWriteFailingContextManager{failUser: storeErr}
	entry := &cmEntry{cm: cm, sessionKey: sessionKey}
	entry.touch()
	al.contextManagers.Store(agent.ID+":"+sessionKey, entry)
	turn := func() error {
		t.Helper()
		_, err := al.runAgentLoop(context.Background(), agent, processOptions{
			SessionKey: sessionKey, Channel: "cli", ChatID: "direct", UserMessage: "hello",
		})
		return err
	}
	alertCount := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.alerts)
	}

	for range 3 {
		if err := turn(); !errors.Is(err, storeErr) {
			t.Fatalf("turn error = %v, want the store error", err)
		}
	}
	if n := alertCount(); n != 1 {
		t.Fatalf("three refused turns must raise one alert, got %d", n)
	}

	cm.failUser = nil
	if err := turn(); err != nil {
		t.Fatalf("turn on a working store: %v", err)
	}
	cm.failUser = storeErr
	if err := turn(); !errors.Is(err, storeErr) {
		t.Fatalf("turn error = %v, want the store error", err)
	}
	if n := alertCount(); n != 2 {
		t.Fatalf("a refusal after a turn that was saved must alert as new, got %d alerts", n)
	}
}
