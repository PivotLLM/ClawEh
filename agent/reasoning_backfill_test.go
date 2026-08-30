// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// backfillLoop builds an AgentLoop with two endpoints: one that demands
// reasoning_content (DeepSeek V4 thinking mode) and one that does not.
func backfillLoop() *AgentLoop {
	return &AgentLoop{cfg: &config.Config{
		Providers: []config.Provider{
			{Name: "ds", Protocol: "openai-chat", RequireReasoningContent: true},
			{Name: "plain", Protocol: "openai-chat"},
		},
		Models: []config.ModelConfig{
			{ModelName: "ds-pro", Provider: "ds", Enabled: true},
			{ModelName: "other", Provider: "plain", Enabled: true},
		},
	}}
}

// mixedHistory is what a session looks like after switching models: some
// assistant turns carry real reasoning, some (written by a CLI model, or by the
// same model with thinking off) carry none.
func mixedHistory() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "write chapter 3"},
		{Role: "assistant", Content: "drafting", ReasoningContent: "real reasoning"},
		{Role: "assistant", Content: "calling a tool", ToolCalls: []providers.ToolCall{{ID: "t1"}}},
		{Role: "tool", ToolCallID: "t1", Content: "result"},
		{Role: "assistant", Content: "written by a CLI model, no reasoning"},
	}
}

// TestBackfill_FillsEmptyAssistantReasoning is the case this exists for: an
// agent switched from a CLI model to DeepSeek replays a history with no
// reasoning_content, and DeepSeek rejects it with a 400 on every turn.
func TestBackfill_FillsEmptyAssistantReasoning(t *testing.T) {
	out := backfillLoop().messagesForModel(mixedHistory(), "ds-pro", true)

	for i, m := range out {
		if m.Role != "assistant" {
			continue
		}
		if m.ReasoningContent == "" {
			t.Errorf("message %d: assistant turn still has empty reasoning_content", i)
		}
	}
	// Real reasoning must survive untouched — this is a backfill, not a rewrite.
	if got := out[1].ReasoningContent; got != "real reasoning" {
		t.Errorf("existing reasoning was overwritten: %q", got)
	}
	// The placeholder must be non-empty; DeepSeek V4 Pro rejects "".
	if got := out[2].ReasoningContent; got != reasoningPlaceholder || got == "" {
		t.Errorf("tool-call turn: got %q, want the non-empty placeholder", got)
	}
	if got := out[4].ReasoningContent; got != reasoningPlaceholder {
		t.Errorf("plain assistant turn: got %q, want the placeholder", got)
	}
}

// TestBackfill_OnlyAssistantMessages keeps the placeholder off user and tool
// turns, which have no such field in the wire contract.
func TestBackfill_OnlyAssistantMessages(t *testing.T) {
	out := backfillLoop().messagesForModel(mixedHistory(), "ds-pro", true)
	for i, m := range out {
		if m.Role != "assistant" && m.ReasoningContent != "" {
			t.Errorf("message %d (%s): reasoning_content set on a non-assistant turn", i, m.Role)
		}
	}
}

// TestBackfill_RequiresTools covers the conditional half of DeepSeek's contract:
// without tools in the request the field is ignored entirely, so there is
// nothing to satisfy and nothing to add.
func TestBackfill_RequiresTools(t *testing.T) {
	out := backfillLoop().messagesForModel(mixedHistory(), "ds-pro", false)
	if out[4].ReasoningContent != "" {
		t.Errorf("no tools in the request: expected no backfill, got %q", out[4].ReasoningContent)
	}
}

// TestBackfill_OnlyWhenProviderOptsIn is the off path — an endpoint that never
// asked for this must see history exactly as stored.
func TestBackfill_OnlyWhenProviderOptsIn(t *testing.T) {
	out := backfillLoop().messagesForModel(mixedHistory(), "other", true)
	if out[4].ReasoningContent != "" {
		t.Errorf("provider did not opt in: expected no backfill, got %q", out[4].ReasoningContent)
	}
}

// TestBackfill_DoesNotMutateInput is load-bearing for the fallback chain: the
// same history slice is handed to each candidate in turn, so one candidate's
// wire quirk must not follow it to the next.
func TestBackfill_DoesNotMutateInput(t *testing.T) {
	al := backfillLoop()
	msgs := mixedHistory()

	_ = al.messagesForModel(msgs, "ds-pro", true)

	if msgs[4].ReasoningContent != "" {
		t.Errorf("input history was mutated: %q", msgs[4].ReasoningContent)
	}
	// And the next candidate, on a provider that rejects the field, sees it clean.
	out := al.messagesForModel(msgs, "other", true)
	if out[4].ReasoningContent != "" {
		t.Errorf("placeholder leaked to a provider that did not ask for it: %q", out[4].ReasoningContent)
	}
}

// TestBackfill_NoCopyWhenNothingToFill guards the fast path: once a thinking
// model has been running, every assistant turn already has reasoning and the
// slice should be returned as-is rather than copied on every dispatch.
func TestBackfill_NoCopyWhenNothingToFill(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "there", ReasoningContent: "thought"},
	}
	out := backfillLoop().messagesForModel(msgs, "ds-pro", true)
	if &out[0] != &msgs[0] {
		t.Error("expected the same backing slice when there is nothing to backfill")
	}
}

// TestBackfill_UnknownModelIsSafe covers a model key that does not resolve —
// the dispatch path must not panic or guess a provider.
func TestBackfill_UnknownModelIsSafe(t *testing.T) {
	out := backfillLoop().messagesForModel(mixedHistory(), "no-such-model", true)
	if out[4].ReasoningContent != "" {
		t.Errorf("unknown model: expected no backfill, got %q", out[4].ReasoningContent)
	}
}
