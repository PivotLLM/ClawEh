// ClawEh
// License: MIT

package config

import "testing"

func TestValidateProvider_IgnoresOtherInvalidEntries(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		{Name: "stale", Protocol: "openai", BaseURL: "https://x/v1"},     // invalid (renamed protocol)
		{Name: "good", Protocol: "openai-chat", BaseURL: "https://x/v1"}, // valid
	}}
	// The valid provider validates fine even though another entry is invalid —
	// this is what lets the WebUI fix providers one at a time.
	if err := cfg.ValidateProvider(1); err != nil {
		t.Errorf("ValidateProvider(good) = %v, want nil", err)
	}
	// The invalid provider still reports its own reason.
	if err := cfg.ValidateProvider(0); err == nil {
		t.Error("ValidateProvider(stale) = nil, want unknown-protocol error")
	}
	// Duplicate names are still rejected.
	cfg.Providers = append(cfg.Providers, Provider{Name: "good", Protocol: "openai-chat", BaseURL: "https://y/v1"})
	if err := cfg.ValidateProvider(2); err == nil {
		t.Error("ValidateProvider(dup) = nil, want duplicate-name error")
	}
	// Out-of-range index is an error, not a panic.
	if err := cfg.ValidateProvider(99); err == nil {
		t.Error("ValidateProvider(99) = nil, want out-of-range error")
	}
}

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

func TestRenameModelReferences(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Models:              []string{"old", "old", "keep"},
				ImageModel:          "old",
				ImageModelFallbacks: []string{"old", "x"},
			},
			List: []AgentConfig{
				{ID: "a", Models: []string{"keep", "old"}, SummarizationModels: []string{"old", "y"}},
			},
		},
		Summarization: SummarizationConfig{Models: []string{"z", "old"}},
	}

	cfg.RenameModelReferences("old", "new")

	if cfg.Agents.Defaults.Models[0] != "new" || cfg.Agents.Defaults.Models[1] != "new" {
		t.Errorf("defaults model not repointed: %v", cfg.Agents.Defaults.Models)
	}
	if cfg.Agents.Defaults.Models[2] != "keep" {
		t.Error("unrelated model entry should be untouched")
	}
	if cfg.Agents.Defaults.ImageModel != "new" || cfg.Agents.Defaults.ImageModelFallbacks[0] != "new" {
		t.Errorf("image model not repointed: %v / %v", cfg.Agents.Defaults.ImageModel, cfg.Agents.Defaults.ImageModelFallbacks)
	}
	if cfg.Summarization.Models[1] != "new" {
		t.Errorf("summarization chain not repointed: %v", cfg.Summarization.Models)
	}
	if cfg.Agents.List[0].Models[1] != "new" || cfg.Agents.List[0].SummarizationModels[0] != "new" {
		t.Errorf("per-agent refs not repointed: %v / %v", cfg.Agents.List[0].Models, cfg.Agents.List[0].SummarizationModels)
	}

	// No-op cases.
	before := cfg.Agents.Defaults.Models[0]
	cfg.RenameModelReferences("", "x")
	cfg.RenameModelReferences("new", "new")
	if cfg.Agents.Defaults.Models[0] != before {
		t.Error("no-op rename mutated config")
	}
}
