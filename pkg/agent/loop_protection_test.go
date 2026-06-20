package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestLoopProtection_BreaksOnRepeatedToolCall verifies the agent loop aborts when
// the model requests the identical tool-call batch on consecutive iterations
// (the degenerate memory-rewrite loop that hit Wendy).
func TestLoopProtection_BreaksOnRepeatedToolCall(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&noopWriteFile{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}

	mkResp := func() *providers.LLMResponse {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				ID: "tc", Type: "function", Name: "file_write",
				Function: &providers.FunctionCall{
					Name:      "file_write",
					Arguments: `{"path":"files/x.txt","content":"same"}`,
				},
			}},
		}
	}
	// Same tool call far more times than the breaker threshold.
	resps := make([]*providers.LLMResponse, 8)
	errs := make([]error, 8)
	for i := range resps {
		resps[i] = mkResp()
	}
	agentInstance.Provider = &sequenceProvider{responses: resps, errors: errs}

	opts := processOptions{
		SessionKey:   "loopguard",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "go",
		SendResponse: false,
	}
	cm, release := al.getContextManager(agentInstance, opts.SessionKey)
	defer release()

	finalContent, _, _, finishReason, iteration, err := al.runLLMIteration(
		context.Background(), agentInstance,
		[]providers.Message{{Role: "user", Content: "go"}}, opts, cm,
	)
	if err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}
	if finishReason != "loop_detected" {
		t.Fatalf("finishReason = %q, want loop_detected", finishReason)
	}
	// Abort message names the repeated tool and that it's a loop.
	if !strings.Contains(finalContent, "file_write") || !strings.Contains(finalContent, "avoid a loop") {
		t.Fatalf("finalContent = %q", finalContent)
	}
	// Must have broken well before MaxIterations (at the 4th identical batch).
	if iteration >= agentInstance.MaxIterations {
		t.Fatalf("did not break early: iteration=%d max=%d", iteration, agentInstance.MaxIterations)
	}
	// Before aborting, the model is steered: a later dispatch must carry the
	// generic "exact same tool call N times" guidance so it can self-correct.
	sp := agentInstance.Provider.(*sequenceProvider)
	var steer string
	for _, m := range sp.lastMessages {
		if strings.Contains(m.Content, "exact same tool call") {
			steer = m.Content
		}
	}
	if steer == "" {
		t.Fatal("expected a steering message injected before the loop abort")
	}
	// The repeated tool here is file_write (not file_edit), so the steering must
	// be the generic one — no file_edit/old_text wording leaking in.
	if strings.Contains(steer, "old_text") || strings.Contains(steer, "file_edit specifically") {
		t.Fatalf("non-file_edit loop got file-specific steering text: %q", steer)
	}
}
