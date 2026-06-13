// ClawEh
// License: MIT

package config

import "testing"

func TestPruneInvalid(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{Name: "good", Protocol: "openai-chat", BaseURL: "https://api.example.com/v1"},
			{Name: "stale", Protocol: "openai", BaseURL: "https://api.example.com/v1"}, // unknown protocol
			{Name: "nobase", Protocol: "openai-chat"},                                  // http protocol missing base_url
			{Name: "", Protocol: "openai-chat", BaseURL: "https://x/v1"},               // empty name
		},
		Models: []ModelConfig{
			{ModelName: "ok", Model: "gpt-4o", Provider: "good", Enabled: true},
			{ModelName: "orphan", Model: "m", Provider: "stale", Enabled: true},  // provider pruned
			{ModelName: "missing", Model: "m", Provider: "ghost", Enabled: true}, // provider never existed
		},
	}

	dp, dm := cfg.PruneInvalid()
	if dp != 3 {
		t.Errorf("droppedProviders = %d, want 3", dp)
	}
	if dm != 2 {
		t.Errorf("droppedModels = %d, want 2", dm)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "good" {
		t.Errorf("survivor providers = %+v, want [good]", cfg.Providers)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ModelName != "ok" {
		t.Errorf("survivor models = %+v, want [ok]", cfg.Models)
	}

	// Idempotent: a clean config prunes nothing.
	if dp2, dm2 := cfg.PruneInvalid(); dp2 != 0 || dm2 != 0 {
		t.Errorf("second prune dropped %d providers / %d models, want 0/0", dp2, dm2)
	}
}
