// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package providers_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestProviderDispatcher_PerAliasState is the regression-lock for the bug
// where multiple model_list entries sharing the same wire model (e.g.
// xai/grok-4.3) had all-but-the-first entry shadowed by the dispatcher
// cache, so per-entry openai_compat state (reasoning_effort,
// response_log_file, extra_body, ...) was silently ignored.
//
// Three entries share xai/grok-4.3 as the wire model but differ in
// model_name, response_log_file, and reasoning_effort. The dispatcher must
// return a distinct provider instance for each alias and each instance must
// carry the per-entry state from its own ModelList entry.
//
// Mutation evidence: revert pkg/providers/dispatch.go to key the cache by
// "protocol/modelID" (the pre-fix behaviour) and re-run this test — the
// three Get calls collapse onto the first entry's instance, so the response
// log file and reasoning_effort assertions for the medium/high aliases fail.
func TestProviderDispatcher_PerAliasState(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName:       "Grok-4.3",
				Model:           "xai/grok-4.3",
				APIKey:          "k",
				APIBase:         "http://127.0.0.1:0/v1",
				ResponseLogFile: "/tmp/grok-low.log",
				ReasoningEffort: "low",
				Enabled:         true,
			},
			{
				ModelName:       "Grok-4.3-Medium",
				Model:           "xai/grok-4.3",
				APIKey:          "k",
				APIBase:         "http://127.0.0.1:0/v1",
				ResponseLogFile: "/tmp/grok-medium.log",
				ReasoningEffort: "medium",
				Enabled:         true,
			},
			{
				ModelName:       "Grok-4.3-High",
				Model:           "xai/grok-4.3",
				APIKey:          "k",
				APIBase:         "http://127.0.0.1:0/v1",
				ResponseLogFile: "/tmp/grok-high.log",
				ReasoningEffort: "high",
				Enabled:         true,
			},
		},
	}
	d := providers.NewProviderDispatcher(cfg)

	cases := []struct {
		alias     string
		wantLog   string
		wantEffrt string
	}{
		{"Grok-4.3", "/tmp/grok-low.log", "low"},
		{"Grok-4.3-Medium", "/tmp/grok-medium.log", "medium"},
		{"Grok-4.3-High", "/tmp/grok-high.log", "high"},
	}

	providersByAlias := make(map[string]providers.LLMProvider, len(cases))
	for _, tc := range cases {
		tc := tc
		t.Run(tc.alias, func(t *testing.T) {
			p, err := d.Get(tc.alias)
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.alias, err)
			}
			if p == nil {
				t.Fatalf("Get(%q): nil provider", tc.alias)
			}
			hp, ok := p.(*providers.HTTPProvider)
			if !ok {
				t.Fatalf("Get(%q): provider type = %T, want *providers.HTTPProvider", tc.alias, p)
			}
			oc := hp.Delegate()
			if oc == nil {
				t.Fatalf("Get(%q): HTTPProvider.Delegate() returned nil", tc.alias)
			}
			if got := oc.ResponseLogFile(); got != tc.wantLog {
				t.Errorf("Get(%q).ResponseLogFile = %q, want %q", tc.alias, got, tc.wantLog)
			}
			if got := oc.ReasoningEffort(); got != tc.wantEffrt {
				t.Errorf("Get(%q).ReasoningEffort = %q, want %q", tc.alias, got, tc.wantEffrt)
			}
			providersByAlias[tc.alias] = p
		})
	}

	// Each alias must resolve to a distinct provider instance — the bug was
	// the cache key collapsing them onto one.
	seen := make(map[providers.LLMProvider]string, len(providersByAlias))
	for alias, p := range providersByAlias {
		if other, dup := seen[p]; dup {
			t.Errorf("alias %q shares provider instance with %q (cache collision); pointer %p",
				alias, other, p)
		}
		seen[p] = alias
	}
}

// TestProviderDispatcher_WireModelFallback verifies the backwards-compatible
// path where a caller still passes a "protocol/modelID" string instead of a
// model_name. The dispatcher matches the first enabled entry whose wire
// model equals the input. Documented as a DBG-logged fallback in dispatch.go.
func TestProviderDispatcher_WireModelFallback(t *testing.T) {
	cfg := &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName:       "named-alias",
				Model:           "xai/grok-4.3",
				APIKey:          "k",
				APIBase:         "http://127.0.0.1:0/v1",
				ResponseLogFile: "/tmp/wire-fallback.log",
				Enabled:         true,
			},
		},
	}
	d := providers.NewProviderDispatcher(cfg)

	p, err := d.Get("xai/grok-4.3")
	if err != nil {
		t.Fatalf("Get by wire model: %v", err)
	}
	hp, ok := p.(*providers.HTTPProvider)
	if !ok {
		t.Fatalf("provider type = %T, want *providers.HTTPProvider", p)
	}
	if got := hp.Delegate().ResponseLogFile(); got != "/tmp/wire-fallback.log" {
		t.Errorf("ResponseLogFile = %q, want /tmp/wire-fallback.log", got)
	}
}
