// ClawEh
// License: MIT

package agent

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// cliMockProvider is a CLIProvider-implementing mock used to stand in for the
// shared agent.Provider in the regression tests below. If any of the dispatcher
// resolution paths incorrectly return this provider, the type-assertion checks
// in runLLMIteration would mis-classify the run as CLI-backed.
type cliMockProvider struct{}

func (m *cliMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "cli-response"}, nil
}
func (m *cliMockProvider) GetDefaultModel() string { return "cli-mock" }
func (m *cliMockProvider) IsCLI() bool             { return true }

// TestResolveRunProvider_UsesDispatcherForActiveCandidate is the regression
// test for the loop.go:1614/1620 defect: type assertions for CLIProvider /
// ThinkingCapable must run against the per-iteration provider resolved through
// the dispatcher, not the shared agent.Provider (which on the shipped default
// config is claude-cli for every agent). Mutation evidence: revert
// resolveRunProvider's body to `return agent.Provider, activeModel` and this
// test fails on the dispatcher-bypass assertion below.
func TestResolveRunProvider_UsesDispatcherForActiveCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "non-cli-primary",
				Model:     "openai/some-non-cli-model",
				APIBase:   "http://127.0.0.1:0/v1",
				APIKey:    "dummy",
				Enabled:   true,
			},
		},
	}
	dispatcher := providers.NewProviderDispatcher(cfg)

	cliShared := &cliMockProvider{}
	al := &AgentLoop{
		cfg:        cfg,
		dispatcher: dispatcher,
	}
	agent := &AgentInstance{
		ID:       "primary-dispatch-test",
		Provider: cliShared,
		Model:    "claude-cli/sonnet-4-5",
	}
	activeCandidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "some-non-cli-model"},
	}

	p, model := al.resolveRunProvider(agent, activeCandidates, "some-non-cli-model")
	if p == nil {
		t.Fatal("resolveRunProvider returned nil provider")
	}
	if p == providers.LLMProvider(cliShared) {
		t.Fatal("resolveRunProvider returned shared agent.Provider — dispatcher was bypassed")
	}
	if _, isCLI := p.(providers.CLIProvider); isCLI {
		t.Fatal("resolveRunProvider returned a CLI provider for a non-CLI candidate — " +
			"type assertions would incorrectly skip temperature/thinking_level llmOpts")
	}
	if model != "some-non-cli-model" {
		t.Fatalf("resolved model = %q; want %q", model, "some-non-cli-model")
	}
}

// TestResolveRunProvider_EmptyCandidatesDispatchesPrimary is the regression
// test for the loop.go:1688 defect: when activeCandidates is empty (or the
// fallback chain is nil) the dispatch path must still route through the
// per-model dispatcher resolved against agent.Model, not directly through
// the shared agent.Provider. Mutation evidence: revert resolveRunProvider's
// body to `return agent.Provider, activeModel` and this test fails on the
// dispatcher-bypass assertion below — pre-fix code at loop.go:1688 had
// exactly that behavior (`return agent.Provider.Chat(...)`), routing non-CLI
// primaries through the shared claude-cli provider on the default config.
func TestResolveRunProvider_EmptyCandidatesDispatchesPrimary(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "Grok-4.3",
				Model:     "xai/grok-4.3",
				APIBase:   "http://127.0.0.1:0/v1",
				APIKey:    "dummy",
				Enabled:   true,
			},
		},
	}
	dispatcher := providers.NewProviderDispatcher(cfg)

	cliShared := &cliMockProvider{}
	al := &AgentLoop{
		cfg:        cfg,
		dispatcher: dispatcher,
	}
	agent := &AgentInstance{
		ID:       "empty-candidates-test",
		Provider: cliShared,
		Model:    "Grok-4.3",
	}

	p, model := al.resolveRunProvider(agent, nil, "Grok-4.3")
	if p == nil {
		t.Fatal("resolveRunProvider returned nil provider")
	}
	if p == providers.LLMProvider(cliShared) {
		t.Fatal("resolveRunProvider returned shared agent.Provider — dispatcher " +
			"was bypassed for the agent primary when activeCandidates was empty")
	}
	if _, isCLI := p.(providers.CLIProvider); isCLI {
		t.Fatal("resolveRunProvider returned a CLI provider for a non-CLI primary " +
			"(xai/grok-4.3) — dispatch would mis-route through claude-cli")
	}
	if model != "grok-4.3" {
		t.Fatalf("resolved model = %q; want %q", model, "grok-4.3")
	}
}

// TestResolveRunProvider_FallbackToAgentProvider confirms the last-resort
// safety net: when the dispatcher cannot resolve a (protocol, model) pair —
// either because the candidate references a missing model_list entry or
// because agent.Model is an unknown alias — fall back to agent.Provider so
// existing single-provider configurations keep working.
func TestResolveRunProvider_FallbackToAgentProvider(t *testing.T) {
	cfg := &config.Config{ModelList: []config.ModelConfig{}}
	dispatcher := providers.NewProviderDispatcher(cfg)

	cliShared := &cliMockProvider{}
	al := &AgentLoop{cfg: cfg, dispatcher: dispatcher}
	agent := &AgentInstance{
		ID:       "fallback-test",
		Provider: cliShared,
		Model:    "no-such-model",
	}

	p, model := al.resolveRunProvider(agent, nil, "no-such-model")
	if p != providers.LLMProvider(cliShared) {
		t.Fatal("expected fallback to agent.Provider when dispatcher cannot resolve primary")
	}
	if model != "no-such-model" {
		t.Fatalf("model = %q; want activeModel passthrough %q", model, "no-such-model")
	}
}

// TestResolveRunProvider_NilDispatcher protects test setups that construct an
// AgentLoop without a dispatcher: fallback to agent.Provider rather than
// panic on a nil dereference.
func TestResolveRunProvider_NilDispatcher(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}, dispatcher: nil}
	cliShared := &cliMockProvider{}
	agent := &AgentInstance{ID: "nil-d", Provider: cliShared, Model: "anything"}

	p, model := al.resolveRunProvider(agent, nil, "anything")
	if p != providers.LLMProvider(cliShared) {
		t.Fatal("expected agent.Provider when dispatcher is nil")
	}
	if model != "anything" {
		t.Fatalf("model = %q; want activeModel passthrough %q", model, "anything")
	}
}
