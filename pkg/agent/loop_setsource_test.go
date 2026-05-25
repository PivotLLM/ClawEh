package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// recordingSTI is a SessionTokenIssuer that records SetSource calls so the
// test can assert the inbound-user-message path correctly records the
// originating channel + chatID on the session record.
type recordingSTI struct {
	mu      sync.Mutex
	sources []sourceCall
}

type sourceCall struct {
	sessionKey string
	channel    string
	chatID     string
}

func (r *recordingSTI) Issue(_ /*agentID*/, _ /*sessionKey*/, _ /*archiveDir*/ string) string {
	// Returning empty is fine for this test — context_manager.go only stores
	// the token when non-empty, and runAgentLoop's SetSource path does not
	// require the token to exist.
	return ""
}
func (r *recordingSTI) Revoke(_ string)      {}
func (r *recordingSTI) RevokeAgent(_ string) {}

func (r *recordingSTI) SetSource(sessionKey, channel, chatID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, sourceCall{
		sessionKey: sessionKey,
		channel:    channel,
		chatID:     chatID,
	})
}

func (r *recordingSTI) calls() []sourceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sourceCall, len(r.sources))
	copy(out, r.sources)
	return out
}

// TestRunAgentLoop_RecordsInboundSourceOnSession verifies that running the
// agent loop for an inbound user message writes the message's channel +
// chatID to the session record via SessionTokenIssuer.SetSource. This is the
// per-session origin signal that the MCP-routed tool dispatch path reads
// when publishing ForUser payloads back to the originating user.
func TestRunAgentLoop_RecordsInboundSourceOnSession(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}

	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)

	// One-shot LLM response so runAgentLoop returns quickly.
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{{Content: "done"}},
		errors:    []error{nil},
	}

	opts := processOptions{
		SessionKey:  "agent:main:test-source",
		Channel:     "slack",
		ChatID:      "C123",
		UserMessage: "hello",
	}

	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
		t.Fatalf("runAgentLoop returned error: %v", err)
	}

	calls := rec.calls()
	var found bool
	for _, c := range calls {
		if c.sessionKey == opts.SessionKey && c.channel == "slack" && c.chatID == "C123" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected SetSource(%q, slack, C123) to be called; got %+v",
			opts.SessionKey, calls)
	}
}

// TestRunAgentLoop_SourceOverwritesAcrossTurns confirms that a second turn on
// the same session updates source — modelling unified-mode where a single
// session follows the user across channels (e.g. Slack → Telegram).
func TestRunAgentLoop_SourceOverwritesAcrossTurns(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}

	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)

	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{Content: "ack1"},
			{Content: "ack2"},
		},
		errors: []error{nil, nil},
	}

	opts1 := processOptions{
		SessionKey:  "agent:main:multi",
		Channel:     "slack",
		ChatID:      "C123",
		UserMessage: "hello from slack",
	}
	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts1); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	opts2 := opts1
	opts2.Channel = "telegram"
	opts2.ChatID = "789"
	opts2.UserMessage = "hello from telegram"
	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts2); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	calls := rec.calls()
	// We require both turns' source-record calls to have landed in order.
	var sawSlack, sawTelegram bool
	for _, c := range calls {
		if c.sessionKey != opts1.SessionKey {
			continue
		}
		if c.channel == "slack" && c.chatID == "C123" {
			sawSlack = true
		}
		if c.channel == "telegram" && c.chatID == "789" {
			sawTelegram = true
		}
	}
	if !sawSlack || !sawTelegram {
		t.Fatalf("expected source updates for both turns; sawSlack=%v sawTelegram=%v calls=%+v",
			sawSlack, sawTelegram, calls)
	}
}

// TestRunAgentLoop_SkipsSetSourceForInternalChannels verifies that runAgentLoop
// does NOT record the inbound source for internal channels (cli, subagent,
// recovery). Those channels have no user-facing channel handler, so recording
// them on the session record would cause the MCP-loopback ForUser publish path
// to dispatch to a nonexistent handler instead of silently dropping at the
// empty-channel guard. The control case (slack) confirms SetSource still fires
// for real external channels.
func TestRunAgentLoop_SkipsSetSourceForInternalChannels(t *testing.T) {
	internalChannels := []string{"cli", "subagent", "recovery"}
	for _, channel := range internalChannels {
		t.Run(channel, func(t *testing.T) {
			al, _, _, _, cleanup := newTestAgentLoop(t)
			defer cleanup()

			agentInstance := al.registry.GetDefaultAgent()
			if agentInstance == nil {
				t.Fatal("no default agent")
			}

			rec := &recordingSTI{}
			al.SetSessionTokenIssuer(rec)

			agentInstance.Provider = &sequenceProvider{
				responses: []*providers.LLMResponse{{Content: "done"}},
				errors:    []error{nil},
			}

			opts := processOptions{
				SessionKey:  "agent:main:internal-" + channel,
				Channel:     channel,
				ChatID:      "chat-" + channel,
				UserMessage: "hello",
			}

			if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
				t.Fatalf("runAgentLoop returned error: %v", err)
			}

			calls := rec.calls()
			if len(calls) != 0 {
				t.Fatalf("expected no SetSource calls for internal channel %q; got %+v",
					channel, calls)
			}
		})
	}

	// Control: external channel must still record source.
	t.Run("slack_control", func(t *testing.T) {
		al, _, _, _, cleanup := newTestAgentLoop(t)
		defer cleanup()

		agentInstance := al.registry.GetDefaultAgent()
		if agentInstance == nil {
			t.Fatal("no default agent")
		}

		rec := &recordingSTI{}
		al.SetSessionTokenIssuer(rec)

		agentInstance.Provider = &sequenceProvider{
			responses: []*providers.LLMResponse{{Content: "done"}},
			errors:    []error{nil},
		}

		opts := processOptions{
			SessionKey:  "agent:main:external-slack",
			Channel:     "slack",
			ChatID:      "C-external",
			UserMessage: "hello",
		}

		if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
			t.Fatalf("runAgentLoop returned error: %v", err)
		}

		calls := rec.calls()
		var found bool
		for _, c := range calls {
			if c.sessionKey == opts.SessionKey && c.channel == "slack" && c.chatID == "C-external" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected SetSource(%q, slack, C-external) to be called for control case; got %+v",
				opts.SessionKey, calls)
		}
	})
}
