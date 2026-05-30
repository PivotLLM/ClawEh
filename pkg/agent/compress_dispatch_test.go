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
		wantAlias string
		wantProto string
		wantModel string
		wantOK    bool
	}{
		{"alias_lookup_by_model_name", "haiku", "haiku", "anthropic", "claude-haiku-4-5", true},
		{"alias_lookup_by_model_id", "gpt-4o-mini", "fast", "openai", "gpt-4o-mini", true},
		{"fully_qualified_match", "anthropic/claude-haiku-4-5", "haiku", "anthropic", "claude-haiku-4-5", true},
		{"fully_qualified_without_match_keeps_prefix", "openrouter/xai-grok-3", "", "openrouter", "xai-grok-3", true},
		{"unknown_bare_returns_not_found", "nope-no-match", "", "", "", false},
		{"disabled_model_id_is_ignored", "gpt-3.5-turbo", "", "", "", false},
		{"empty_string_returns_not_found", "", "", "", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			a, p, m, ok := resolveCompressModelTarget(cfg, tc.raw)
			if ok != tc.wantOK || a != tc.wantAlias || p != tc.wantProto || m != tc.wantModel {
				t.Fatalf("resolveCompressModelTarget(%q) = (%q,%q,%q,%v); want (%q,%q,%q,%v)",
					tc.raw, a, p, m, ok, tc.wantAlias, tc.wantProto, tc.wantModel, tc.wantOK)
			}
		})
	}
}

// TestResolveCompressModelTarget_NilConfig defends against the unlikely case
// where the loop has no config snapshot yet.
func TestResolveCompressModelTarget_NilConfig(t *testing.T) {
	_, _, _, ok := resolveCompressModelTarget(nil, "haiku")
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

// TestBuildDefaultCompressLLMClient_UsesDispatcherForPrimary is the
// regression test for the wendy compress-routing defect: when no
// compress_model is configured, the compression LLM client must be built
// against the agent's primary model via the per-model dispatcher, not the
// shared agent.Provider. Before this fix, an agent with
// model.primary = "openai/<x>" and an empty compress_model would still
// have compression dispatched to the shared agent.Provider (built from
// agents.defaults.model.primary = "claude-cli" in the shipped default
// config), causing claude-cli to be invoked with a non-Claude model and
// 404. If someone reverts to "shared provider when compress_model empty",
// this test fails on the dispatcher-bypass assertion below.
func TestBuildDefaultCompressLLMClient_UsesDispatcherForPrimary(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "primary-target",
				Model:     "openai/some-model",
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
		ID:       "default-compress-test",
		Provider: primary,
		Model:    "openai/some-model",
		Config: &config.AgentConfig{
			ID:    "default-compress-test",
			Model: &config.AgentModelConfig{Primary: "openai/some-model"},
			// CompressModel intentionally nil to exercise the default-to-primary path.
		},
	}

	client := al.buildDefaultCompressLLMClient(agent, "sess-default")
	plc, ok := client.(*providerLLMClient)
	if !ok {
		t.Fatalf("expected *providerLLMClient, got %T", client)
	}
	if plc.provider == nil {
		t.Fatal("provider is nil")
	}
	if plc.provider == providers.LLMProvider(primary) {
		t.Fatal("default compress provider equals agent.Provider — dispatcher was bypassed for the agent primary")
	}
	if plc.model != "some-model" {
		t.Fatalf("model = %q; want %q", plc.model, "some-model")
	}
}

// TestBuildDefaultCompressLLMClient_FallbackWhenPrimaryUnresolved confirms
// that when the agent's primary model cannot be resolved through the
// dispatcher (no matching enabled model_list entry), the default-compress
// path falls back to agent.Provider rather than failing.
func TestBuildDefaultCompressLLMClient_FallbackWhenPrimaryUnresolved(t *testing.T) {
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
		ID:       "default-fallback-test",
		Provider: primary,
		Model:    "no-such-model",
	}

	client := al.buildDefaultCompressLLMClient(agent, "sess-default-fallback")
	plc, ok := client.(*providerLLMClient)
	if !ok {
		t.Fatalf("expected *providerLLMClient, got %T", client)
	}
	if plc.provider != providers.LLMProvider(primary) {
		t.Fatal("expected fallback to agent.Provider when dispatcher cannot resolve the primary model")
	}
	if plc.model != "no-such-model" {
		t.Fatalf("model = %q; want %q", plc.model, "no-such-model")
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
