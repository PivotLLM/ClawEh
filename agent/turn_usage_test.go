// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/providers"
)

func TestAddTurnUsage_NilTargetOrResponse(t *testing.T) {
	// Must not panic when the caller did not ask for usage or the call failed.
	addTurnUsage(nil, &providers.LLMResponse{}, "anthropic", "m")
	var u global.TurnUsage
	addTurnUsage(&u, nil, "anthropic", "m")
	if u != (global.TurnUsage{}) {
		t.Fatalf("nil response must not change usage, got %+v", u)
	}
}

func TestAddTurnUsage_AccumulatesAcrossIterations(t *testing.T) {
	var u global.TurnUsage
	addTurnUsage(&u, &providers.LLMResponse{Status: &providers.DispatchStatus{
		Model: "claude-a", InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheCreationTokens: 2, CostUSD: 0.01,
	}}, "anthropic", "requested")
	addTurnUsage(&u, &providers.LLMResponse{Status: &providers.DispatchStatus{
		Model: "claude-a", InputTokens: 200, OutputTokens: 20, CacheReadTokens: 1, CacheCreationTokens: 0, CostUSD: 0.02,
	}}, "anthropic", "requested")

	want := global.TurnUsage{Model: "claude-a", Provider: "anthropic",
		InputTokens: 300, OutputTokens: 30, CacheReadTokens: 6, CacheCreationTokens: 2, CostUSD: 0.03}
	if u.Model != want.Model || u.Provider != want.Provider || u.InputTokens != want.InputTokens ||
		u.OutputTokens != want.OutputTokens || u.CacheReadTokens != want.CacheReadTokens ||
		u.CacheCreationTokens != want.CacheCreationTokens || u.CostUSD < 0.0299 || u.CostUSD > 0.0301 {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
}

func TestAddTurnUsage_ServedModelWinsOverRequested(t *testing.T) {
	var u global.TurnUsage
	// A provider that reports no status still records the requested model.
	addTurnUsage(&u, &providers.LLMResponse{}, "openai", "gpt-req")
	if u.Model != "gpt-req" || u.Provider != "openai" {
		t.Fatalf("without status: model/provider = %q/%q, want gpt-req/openai", u.Model, u.Provider)
	}
	// A later call that reports the served model overrides it (failover case).
	addTurnUsage(&u, &providers.LLMResponse{Status: &providers.DispatchStatus{Model: "gpt-served"}}, "openai", "gpt-req")
	if u.Model != "gpt-served" {
		t.Fatalf("served model must win, got %q", u.Model)
	}
}
