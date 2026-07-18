package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// driveEmptyResponse runs one turn with the given provider responses and returns
// the final content and the degenerate flag from runLLMIteration.
func driveEmptyResponse(t *testing.T, responses []*providers.LLMResponse) (string, bool) {
	t.Helper()
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	errs := make([]error, len(responses))
	agentInstance.Provider = &sequenceProvider{responses: responses, errors: errs}

	opts := processOptions{
		SessionKey:   "empty-resp",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "go",
		SendResponse: false,
	}
	cm, release := al.getContextManager(agentInstance, opts.SessionKey)
	defer release()

	finalContent, _, degenerate, _, _, err := al.runLLMIteration(
		context.Background(), agentInstance,
		[]providers.Message{{Role: "user", Content: "go"}}, opts, cm,
	)
	if err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}
	return finalContent, degenerate
}

// A poke after an empty-but-reasoning turn recovers a real reply on retry.
func TestEmptyResponse_PokeRecovers(t *testing.T) {
	got, degenerate := driveEmptyResponse(t, []*providers.LLMResponse{
		{Content: "", ReasoningContent: "thinking...", FinishReason: "stop", Normal: true},
		{Content: "here is the answer", FinishReason: "stop", Normal: true},
	})
	if got != "here is the answer" {
		t.Fatalf("poke did not recover the reply: %q", got)
	}
	if degenerate {
		t.Fatalf("should not be flagged degenerate after a successful poke")
	}
}

// When the model keeps returning empty-with-reasoning past the retry cap, the
// turn is flagged degenerate (so the caller sends a fallback) with empty content.
func TestEmptyResponse_DegenerateAfterRetries(t *testing.T) {
	empty := func() *providers.LLMResponse {
		return &providers.LLMResponse{Content: "", ReasoningContent: "thinking...", FinishReason: "stop", Normal: true}
	}
	// initial + maxEmptyRetries pokes all empty → degenerate.
	got, degenerate := driveEmptyResponse(t, []*providers.LLMResponse{empty(), empty(), empty()})
	if got != "" {
		t.Fatalf("expected empty final content, got %q", got)
	}
	if !degenerate {
		t.Fatalf("expected degenerate=true after exhausting empty-response retries")
	}
}

// The no-response sentinel is treated as intentional silence: nothing is sent
// and the model is NOT poked (one provider call only).
func TestNoResponseSentinel_SilentNoPoke(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	provider := &sequenceProvider{
		responses: []*providers.LLMResponse{
			{Content: "!none", Reasoning: "this message is for someone else", FinishReason: "stop", Normal: true},
		},
		errors: []error{nil},
	}
	agentInstance.Provider = provider

	resp, err := al.ProcessDirectWithChannel(
		context.Background(), "hello everyone", "sentinel-test", "test", "test-chat", "channel",
	)
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel: %v", err)
	}
	if resp != "" {
		t.Fatalf("expected silent (empty) response for sentinel, got %q", resp)
	}
	if provider.idx != 1 {
		t.Fatalf("expected exactly 1 provider call (no poke), got %d", provider.idx)
	}
}

// Reasoning may arrive in the `reasoning` field (OpenRouter/DeepSeek) rather
// than `reasoning_content` (OpenAI) — the empty-response handling must treat
// either as "the model thought but didn't reply" and poke/flag accordingly.
func TestEmptyResponse_ReasoningFieldAlsoTriggers(t *testing.T) {
	empty := func() *providers.LLMResponse {
		return &providers.LLMResponse{Content: "", Reasoning: "thinking...", FinishReason: "stop", Normal: true}
	}
	got, degenerate := driveEmptyResponse(t, []*providers.LLMResponse{empty(), empty(), empty()})
	if got != "" {
		t.Fatalf("expected empty final content, got %q", got)
	}
	if !degenerate {
		t.Fatalf("reasoning in the `reasoning` field must also flag degenerate after retries")
	}
}

// driveEmptyViaLoop runs a full turn through runAgentLoop with a single empty,
// normal response and returns the user-facing reply.
func driveEmptyViaLoop(t *testing.T, isGroup bool) string {
	t.Helper()
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{{Content: "", FinishReason: "stop", Normal: true}},
		errors:    []error{nil},
	}

	resp, err := al.runAgentLoop(context.Background(), agentInstance, processOptions{
		SessionKey:   "empty-loop",
		Channel:      "test",
		ChatID:       "test-chat",
		UserMessage:  "go",
		SendResponse: false,
		IsGroup:      isGroup,
	})
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return resp
}

// A direct (non-group) empty response is surfaced to the user rather than
// swallowed — a degraded fallback model returning nothing must not look like a hang.
func TestEmptyResponse_DirectAdvisesUser(t *testing.T) {
	resp := driveEmptyViaLoop(t, false)
	if !strings.Contains(resp, "empty response") {
		t.Fatalf("direct empty response must advise the user, got %q", resp)
	}
}

// A group empty response stays silent (the message wasn't for this agent).
func TestEmptyResponse_GroupStaysSilent(t *testing.T) {
	if resp := driveEmptyViaLoop(t, true); resp != "" {
		t.Fatalf("group empty response must stay silent, got %q", resp)
	}
}

// Genuinely empty (no content AND no reasoning) is not degenerate and is not
// poked — it stays silent (e.g. a message @-directed at another agent).
func TestEmptyResponse_NoReasoningStaysSilent(t *testing.T) {
	got, degenerate := driveEmptyResponse(t, []*providers.LLMResponse{
		{Content: "", ReasoningContent: "", FinishReason: "stop", Normal: true},
	})
	if got != "" {
		t.Fatalf("expected empty final content, got %q", got)
	}
	if degenerate {
		t.Fatalf("empty content with no reasoning must not be flagged degenerate")
	}
}
