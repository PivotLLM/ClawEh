// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestResolveCompressModelTarget covers the alias / shorthand / fully-qualified
// resolution paths that compress_model strings can take.
func TestResolveCompressModelTarget(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "haiku",
				Model:     "anthropic/claude-haiku-4-5",
				APIKey:    "k",
				Enabled:   true,
			},
			{
				ModelName: "fast",
				Model:     "openai/gpt-4o-mini",
				APIKey:    "k",
				Enabled:   true,
			},
			{
				ModelName: "disabled",
				Model:     "openai/gpt-3.5-turbo",
				APIKey:    "k",
				Enabled:   false,
			},
		},
	}

	cases := []struct {
		name      string
		raw       string
		wantProto string
		wantModel string
		wantOK    bool
	}{
		{"alias_lookup_by_model_name", "haiku", "anthropic", "claude-haiku-4-5", true},
		{"alias_lookup_by_model_id", "gpt-4o-mini", "openai", "gpt-4o-mini", true},
		{"fully_qualified_match", "anthropic/claude-haiku-4-5", "anthropic", "claude-haiku-4-5", true},
		{"fully_qualified_without_match_keeps_prefix", "openrouter/xai-grok-3", "openrouter", "xai-grok-3", true},
		{"unknown_bare_returns_not_found", "nope-no-match", "", "", false},
		{"disabled_model_id_is_ignored", "gpt-3.5-turbo", "", "", false},
		{"empty_string_returns_not_found", "", "", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p, m, ok := resolveCompressModelTarget(cfg, tc.raw)
			if ok != tc.wantOK || p != tc.wantProto || m != tc.wantModel {
				t.Fatalf("resolveCompressModelTarget(%q) = (%q,%q,%v); want (%q,%q,%v)",
					tc.raw, p, m, ok, tc.wantProto, tc.wantModel, tc.wantOK)
			}
		})
	}
}

// TestResolveCompressModelTarget_NilConfig defends against the unlikely case
// where the loop has no config snapshot yet.
func TestResolveCompressModelTarget_NilConfig(t *testing.T) {
	_, _, ok := resolveCompressModelTarget(nil, "haiku")
	if ok {
		t.Fatalf("expected not-found on nil cfg")
	}
}

// TestBuildCompressLLMClient_UsesDispatcher is the load-bearing assertion for
// the compress_model defect fix: the compression LLM client must be built via
// the per-model dispatcher, not against the agent's shared primary provider.
// Before this fix, an agent whose primary protocol is claude-cli would shell
// out to claude-cli for compression even when compress_model points at an
// openai/anthropic/openrouter/xai model.
func TestBuildCompressLLMClient_UsesDispatcher(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "compress-target",
				Model:     "openai/test-compress-model",
				APIBase:   "http://127.0.0.1:0/v1",
				APIKey:    "dummy",
				Enabled:   true,
			},
		},
	}
	dispatcher := providers.NewProviderDispatcher(cfg)

	primary := &mockProvider{}
	al := &AgentLoop{
		cfg:        cfg,
		dispatcher: dispatcher,
	}
	agent := &AgentInstance{
		ID:       "compress-test",
		Provider: primary,
		Model:    "claude-cli/sonnet-4-5",
	}

	client := al.buildCompressLLMClient(agent, "test-compress-model", "sess-1")
	plc, ok := client.(*providerLLMClient)
	if !ok {
		t.Fatalf("expected *providerLLMClient, got %T", client)
	}
	if plc.provider == nil {
		t.Fatal("provider is nil")
	}
	if plc.provider == providers.LLMProvider(primary) {
		t.Fatal("compress provider equals agent.Provider — dispatcher was bypassed")
	}
	if plc.model != "test-compress-model" {
		t.Fatalf("model = %q; want %q", plc.model, "test-compress-model")
	}
}

// TestBuildCompressLLMClient_FallbackToAgentProvider exercises the last-resort
// path: when the compress_model name does not resolve against model_list,
// fall back to the agent's primary provider rather than fail compression.
func TestBuildCompressLLMClient_FallbackToAgentProvider(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{},
	}
	dispatcher := providers.NewProviderDispatcher(cfg)

	primary := &mockProvider{}
	al := &AgentLoop{
		cfg:        cfg,
		dispatcher: dispatcher,
	}
	agent := &AgentInstance{
		ID:       "fallback-test",
		Provider: primary,
	}

	client := al.buildCompressLLMClient(agent, "unknown-model", "sess-2")
	plc, ok := client.(*providerLLMClient)
	if !ok {
		t.Fatalf("expected *providerLLMClient, got %T", client)
	}
	if plc.provider != providers.LLMProvider(primary) {
		t.Fatal("expected fallback to agent.Provider when dispatcher cannot resolve compress_model")
	}
	if plc.model != "unknown-model" {
		t.Fatalf("model = %q; want %q", plc.model, "unknown-model")
	}
}

// TestBuildCompressLLMClient_NilDispatcher protects the path where the loop
// was constructed without a dispatcher (older test setups, smoke tests):
// fallback to agent.Provider rather than panic.
func TestBuildCompressLLMClient_NilDispatcher(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "x",
				Model:     "openai/y",
				APIKey:    "k",
				Enabled:   true,
			},
		},
	}
	primary := &mockProvider{}
	al := &AgentLoop{cfg: cfg, dispatcher: nil}
	agent := &AgentInstance{ID: "nil-d", Provider: primary}

	client := al.buildCompressLLMClient(agent, "y", "sess-3")
	plc := client.(*providerLLMClient)
	if plc.provider != providers.LLMProvider(primary) {
		t.Fatal("expected fallback to agent.Provider when dispatcher is nil")
	}
}
