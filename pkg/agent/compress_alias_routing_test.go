// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestBuildCompressLLMClient_PerAliasRouting is the regression-lock for the
// dispatcher cache-key fix on the compress path.
//
// Three models entries share the same wire model xai/grok-4.3 but differ
// in model_name and response_log_file. An agent whose compress_model points
// at the medium alias must compress through a provider built from the
// medium entry's state — not the first entry's (the pre-fix bug). The test
// asserts the provider instance the compress client targets matches the
// alias-specific instance returned by the dispatcher.
//
// Mutation evidence: revert pkg/providers/dispatch.go cache key to the wire
// model and re-run: the compress client for "Grok-4.3-Medium" reuses the
// "Grok-4.3" entry's instance with the low-tier response_log_file.
func TestBuildCompressLLMClient_PerAliasRouting(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "xai", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"},
		},
		Models: []config.ModelConfig{
			{
				ModelName:       "Grok-4.3",
				Model:           "grok-4.3",
				Provider:        "xai",
				ResponseLogFile: "/tmp/grok-low.log",
				ReasoningEffort: "low",
				Enabled:         true,
			},
			{
				ModelName:       "Grok-4.3-Medium",
				Model:           "grok-4.3",
				Provider:        "xai",
				ResponseLogFile: "/tmp/grok-medium.log",
				ReasoningEffort: "medium",
				Enabled:         true,
			},
			{
				ModelName:       "Grok-4.3-High",
				Model:           "grok-4.3",
				Provider:        "xai",
				ResponseLogFile: "/tmp/grok-high.log",
				ReasoningEffort: "high",
				Enabled:         true,
			},
		},
	}
	dispatcher := providers.NewProviderDispatcher(cfg)

	al := &AgentLoop{cfg: cfg, dispatcher: dispatcher}
	agent := &AgentInstance{
		ID:       "compress-alias-routing",
		Provider: &mockProvider{},
		Model:    "sonnet-4-5",
	}

	client := al.buildCompressLLMClient(agent, "Grok-4.3-Medium", "sess-medium")
	plc, ok := client.(*providerLLMClient)
	if !ok {
		t.Fatalf("expected *providerLLMClient, got %T", client)
	}
	hp, ok := plc.provider.(*providers.HTTPProvider)
	if !ok {
		t.Fatalf("compress provider type = %T, want *providers.HTTPProvider", plc.provider)
	}
	if got := hp.Delegate().ResponseLogFile(); got != "/tmp/grok-medium.log" {
		t.Errorf("ResponseLogFile = %q, want /tmp/grok-medium.log (per-entry state ignored)", got)
	}
	if got := hp.Delegate().ReasoningEffort(); got != "medium" {
		t.Errorf("ReasoningEffort = %q, want medium (per-entry state ignored)", got)
	}

	// The compress provider must be the same instance the dispatcher hands
	// out for the matching alias, and a different instance from the other
	// aliases sharing the same wire model.
	wantMedium, err := dispatcher.Get("Grok-4.3-Medium")
	if err != nil {
		t.Fatalf("dispatcher.Get(Grok-4.3-Medium): %v", err)
	}
	if plc.provider != wantMedium {
		t.Errorf("compress provider is not the dispatcher's Grok-4.3-Medium instance")
	}
	other, err := dispatcher.Get("Grok-4.3")
	if err != nil {
		t.Fatalf("dispatcher.Get(Grok-4.3): %v", err)
	}
	if plc.provider == other {
		t.Errorf("compress provider for Grok-4.3-Medium collapsed onto Grok-4.3 instance")
	}
}
