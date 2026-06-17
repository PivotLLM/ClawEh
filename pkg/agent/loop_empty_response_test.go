package agent

import (
	"context"
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
