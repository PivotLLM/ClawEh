package agent

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
)

// runReasoningOnlyTurn drives one iteration where the model returns empty content
// but populated reasoning_content (the degenerate "reasoning-only" case that hit
// Wendy), and returns the user-facing finalContent.
func runReasoningOnlyTurn(t *testing.T, showAsContent bool) string {
	t.Helper()
	tl := newTestAgentLoop(t)
	al, cfg := tl.al, tl.cfg
	cfg.Agents.Defaults.ShowReasoningAsContent = showAsContent

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{Content: "", ReasoningContent: "SECRET-THINKING", FinishReason: "stop", Normal: true},
		},
		errors: []error{nil},
	}

	opts := processOptions{
		SessionKey:   "reasoning-gate",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "go",
		SendResponse: false,
	}
	cm, release := al.getContextManager(agentInstance, opts.SessionKey)
	defer release()

	return runIteration(t, al, agentInstance, []providers.Message{{Role: "user", Content: "go"}}, opts, cm).content
}

// Default (flag off): reasoning_content must never reach the user-facing reply.
func TestReasoningContent_NotShownByDefault(t *testing.T) {
	if got := runReasoningOnlyTurn(t, false); strings.Contains(got, "SECRET-THINKING") {
		t.Fatalf("reasoning leaked into reply with flag off: %q", got)
	}
}

// Opt-in (flag on): preserve the old fallback behavior.
func TestReasoningContent_ShownWhenEnabled(t *testing.T) {
	if got := runReasoningOnlyTurn(t, true); got != "SECRET-THINKING" {
		t.Fatalf("reasoning not used as reply with flag on: %q", got)
	}
}
