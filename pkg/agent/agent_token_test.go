// ClawEh
// License: MIT

package agent

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
)

func TestContextBuilder_WithAgentTokenInjectsIntoPrompt(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())

	tok := agenttoken.Prefix + strings.Repeat("a", 64)
	cb.WithAgentToken(tok)

	prompt := cb.BuildSystemPrompt()
	if !strings.Contains(prompt, "# Agent Token") {
		t.Errorf("system prompt missing Agent Token section:\n%s", prompt)
	}
	if !strings.Contains(prompt, tok) {
		t.Errorf("system prompt missing the agent token:\n%s", prompt)
	}
	if !strings.Contains(prompt, "agent_token") {
		t.Errorf("system prompt missing the agent_token parameter name:\n%s", prompt)
	}
}

func TestContextBuilder_NoAgentTokenOmitsSection(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	prompt := cb.BuildSystemPrompt()
	if strings.Contains(prompt, "# Agent Token") {
		t.Errorf("system prompt should not contain Agent Token section when none set:\n%s", prompt)
	}
}

func TestContextBuilder_WithAgentTokenInvalidatesCache(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())

	first := cb.BuildSystemPromptWithCache()
	if strings.Contains(first, "# Agent Token") {
		t.Fatal("baseline prompt should not contain token section")
	}

	tok := agenttoken.Prefix + strings.Repeat("b", 64)
	cb.WithAgentToken(tok)

	second := cb.BuildSystemPromptWithCache()
	if !strings.Contains(second, tok) {
		t.Errorf("cached prompt was not invalidated; token missing:\n%s", second)
	}
}
