// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGetModelConfig_Found(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{
			{ModelName: "test-model", Model: "openai/gpt-4o", APIKey: "key1", Enabled: true},
			{ModelName: "other-model", Model: "anthropic/claude", APIKey: "key2", Enabled: true},
		},
	}

	result, err := cfg.GetModelConfig("test-model")
	if err != nil {
		t.Fatalf("GetModelConfig() error = %v", err)
	}
	if result.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want %q", result.Model, "openai/gpt-4o")
	}
}

func TestGetModelConfig_NotFound(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{
			{ModelName: "test-model", Model: "openai/gpt-4o", APIKey: "key1"},
		},
	}

	_, err := cfg.GetModelConfig("nonexistent")
	if err == nil {
		t.Fatal("GetModelConfig() expected error for nonexistent model")
	}
}

func TestGetModelConfig_EmptyList(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{},
	}

	_, err := cfg.GetModelConfig("any-model")
	if err == nil {
		t.Fatal("GetModelConfig() expected error for empty model list")
	}
}

func TestGetModelConfig_RoundRobin(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{
			{ModelName: "lb-model", Model: "openai/gpt-4o-1", APIKey: "key1", Enabled: true},
			{ModelName: "lb-model", Model: "openai/gpt-4o-2", APIKey: "key2", Enabled: true},
			{ModelName: "lb-model", Model: "openai/gpt-4o-3", APIKey: "key3", Enabled: true},
		},
	}

	// Test round-robin distribution
	results := make(map[string]int)
	for range 30 {
		result, err := cfg.GetModelConfig("lb-model")
		if err != nil {
			t.Fatalf("GetModelConfig() error = %v", err)
		}
		results[result.Model]++
	}

	// Each model should appear roughly 10 times (30 calls / 3 models)
	for model, count := range results {
		if count < 5 || count > 15 {
			t.Errorf("Model %s appeared %d times, expected ~10", model, count)
		}
	}
}

func TestGetModelConfig_Concurrent(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelConfig{
			{ModelName: "concurrent-model", Model: "openai/gpt-4o-1", APIKey: "key1", Enabled: true},
			{ModelName: "concurrent-model", Model: "openai/gpt-4o-2", APIKey: "key2", Enabled: true},
		},
	}

	const goroutines = 100
	const iterations = 10

	var wg sync.WaitGroup
	errors := make(chan error, goroutines*iterations)

	for range goroutines {
		wg.Go(func() {
			for range iterations {
				_, err := cfg.GetModelConfig("concurrent-model")
				if err != nil {
					errors <- err
				}
			}
		})
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent GetModelConfig() error: %v", err)
	}
}

func TestAgentDefaults_DefaultModelName(t *testing.T) {
	tests := []struct {
		name     string
		defaults AgentDefaults
		wantName string
	}{
		{
			name:     "nil model returns empty",
			defaults: AgentDefaults{},
			wantName: "",
		},
		{
			name:     "model with primary set",
			defaults: AgentDefaults{Model: &AgentModelConfig{Primary: "gpt4"}},
			wantName: "gpt4",
		},
		{
			name:     "model with primary and fallbacks",
			defaults: AgentDefaults{Model: &AgentModelConfig{Primary: "gpt4", Fallbacks: []string{"claude"}}},
			wantName: "gpt4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.defaults.DefaultModelName(); got != tt.wantName {
				t.Errorf("DefaultModelName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestAgentDefaults_SetDefaultModel(t *testing.T) {
	t.Run("set model on nil model field", func(t *testing.T) {
		var defaults AgentDefaults
		defaults.SetDefaultModel("gpt4")
		if defaults.Model == nil {
			t.Fatal("Model should not be nil after SetDefaultModel")
		}
		if defaults.Model.Primary != "gpt4" {
			t.Errorf("Primary = %q, want %q", defaults.Model.Primary, "gpt4")
		}
	})

	t.Run("set model preserves existing fallbacks", func(t *testing.T) {
		defaults := AgentDefaults{
			Model: &AgentModelConfig{
				Primary:   "old-model",
				Fallbacks: []string{"fallback1"},
			},
		}
		defaults.SetDefaultModel("new-model")
		if defaults.Model.Primary != "new-model" {
			t.Errorf("Primary = %q, want %q", defaults.Model.Primary, "new-model")
		}
		if len(defaults.Model.Fallbacks) != 1 || defaults.Model.Fallbacks[0] != "fallback1" {
			t.Errorf("Fallbacks = %v, want [fallback1]", defaults.Model.Fallbacks)
		}
	})
}

func TestFullConfig_JSON_ModelConfig(t *testing.T) {
	// Test complete config with model as AgentModelConfig
	jsonStr := `{
		"agents": {
			"defaults": {
				"workspace": "~/.claw/workspace",
				"model": {"primary": "gpt4", "fallbacks": ["claude"]},
				"max_tokens": 4096
			}
		},
		"model_list": [
			{
				"model_name": "gpt4",
				"model": "openai/gpt-4o",
				"api_key": "test-key",
				"enabled": true
			}
		]
	}`

	cfg := &Config{}
	if err := json.Unmarshal([]byte(jsonStr), cfg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	// Check that DefaultModelName returns correct value
	if got := cfg.Agents.Defaults.DefaultModelName(); got != "gpt4" {
		t.Errorf("DefaultModelName() = %q, want %q", got, "gpt4")
	}

	// Check that GetModelConfig works
	modelCfg, err := cfg.GetModelConfig("gpt4")
	if err != nil {
		t.Fatalf("GetModelConfig error: %v", err)
	}
	if modelCfg.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want %q", modelCfg.Model, "openai/gpt-4o")
	}
}

func TestModelConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  ModelConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: ModelConfig{
				ModelName: "test",
				Model:     "openai/gpt-4o",
			},
			wantErr: false,
		},
		{
			name: "missing model_name",
			config: ModelConfig{
				Model: "openai/gpt-4o",
			},
			wantErr: true,
		},
		{
			name: "missing model",
			config: ModelConfig{
				ModelName: "test",
			},
			wantErr: true,
		},
		{
			name:    "empty config",
			config:  ModelConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_ValidateModelList(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string // partial error message to check
	}{
		{
			name: "valid list",
			config: &Config{
				ModelList: []ModelConfig{
					{ModelName: "test1", Model: "openai/gpt-4o"},
					{ModelName: "test2", Model: "anthropic/claude"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid entry",
			config: &Config{
				ModelList: []ModelConfig{
					{ModelName: "test1", Model: "openai/gpt-4o"},
					{ModelName: "", Model: "anthropic/claude"}, // missing model_name
				},
			},
			wantErr: true,
			errMsg:  "model_name is required",
		},
		{
			name: "empty list",
			config: &Config{
				ModelList: []ModelConfig{},
			},
			wantErr: false,
		},
		{
			// Load balancing: multiple entries with same model_name are allowed
			name: "duplicate model_name for load balancing",
			config: &Config{
				ModelList: []ModelConfig{
					{ModelName: "gpt-4", Model: "openai/gpt-4o", APIKey: "key1"},
					{ModelName: "gpt-4", Model: "openai/gpt-4-turbo", APIKey: "key2"},
				},
			},
			wantErr: false, // Changed: duplicates are allowed for load balancing
		},
		{
			// Load balancing: non-adjacent entries with same model_name are also allowed
			name: "duplicate model_name non-adjacent for load balancing",
			config: &Config{
				ModelList: []ModelConfig{
					{ModelName: "model-a", Model: "openai/gpt-4o"},
					{ModelName: "model-b", Model: "anthropic/claude"},
					{ModelName: "model-a", Model: "openai/gpt-4-turbo"},
				},
			},
			wantErr: false, // Changed: duplicates are allowed for load balancing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateModelList()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateModelList() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateModelList() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestModelConfig_ReasoningEffort_Validate(t *testing.T) {
	for _, level := range []string{"", "low", "medium", "high"} {
		cfg := ModelConfig{ModelName: "m", Model: "openai/gpt", ReasoningEffort: level}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%q) returned error: %v", level, err)
		}
	}

	cfg := ModelConfig{ModelName: "grok-3", Model: "openai/grok-3", ReasoningEffort: "extreme"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for invalid reasoning_effort")
	}
	if !strings.Contains(err.Error(), "grok-3") {
		t.Errorf("error should name the model: %v", err)
	}
	if !strings.Contains(err.Error(), "extreme") {
		t.Errorf("error should name the bad value: %v", err)
	}
}

func TestModelConfig_ExtraBody_CollisionRejected(t *testing.T) {
	cases := []string{
		"model", "messages", "stream", "tools", "tool_choice",
		"parallel_tool_calls", "reasoning_effort", "temperature",
		"max_tokens", "max_completion_tokens", "top_p", "n",
	}
	for _, key := range cases {
		cfg := ModelConfig{
			ModelName: "the-model",
			Model:     "openai/whatever",
			ExtraBody: map[string]any{key: 1},
		}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("extra_body key %q: expected validation error", key)
			continue
		}
		if !strings.Contains(err.Error(), "the-model") {
			t.Errorf("key %q: error should name the model: %v", key, err)
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("key %q: error should name the offending key: %v", key, err)
		}
	}
}

func TestModelConfig_ExtraBody_AllowedKeysPass(t *testing.T) {
	cfg := ModelConfig{
		ModelName: "m",
		Model:     "openai/gpt",
		ExtraBody: map[string]any{
			"custom_xai_thinking": map[string]any{"budget": 1024},
			"safety_settings":     []any{"strict"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestModelConfig_ReasoningEffort_ExtraBody_JSONRoundTrip(t *testing.T) {
	original := ModelConfig{
		ModelName:       "grok",
		Model:           "openai/grok-3",
		APIKey:          "k",
		ReasoningEffort: "high",
		ExtraBody: map[string]any{
			"search_parameters": map[string]any{"mode": "auto"},
		},
		Enabled: true,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"reasoning_effort":"high"`) {
		t.Errorf("missing reasoning_effort in JSON: %s", data)
	}
	if !strings.Contains(string(data), `"extra_body":`) {
		t.Errorf("missing extra_body in JSON: %s", data)
	}
	var got ModelConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", got.ReasoningEffort)
	}
	if got.ExtraBody["search_parameters"] == nil {
		t.Errorf("extra_body lost in round-trip: %+v", got.ExtraBody)
	}
}

func TestModelConfig_ReasoningEffort_ExtraBody_YAMLRoundTrip(t *testing.T) {
	original := ModelConfig{
		ModelName:       "grok",
		Model:           "openai/grok-3",
		APIKey:          "k",
		ReasoningEffort: "medium",
		ExtraBody: map[string]any{
			"search_parameters": map[string]any{"mode": "auto"},
		},
		Enabled: true,
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(data), "reasoning_effort: medium") {
		t.Errorf("missing reasoning_effort in YAML:\n%s", data)
	}
	if !strings.Contains(string(data), "extra_body:") {
		t.Errorf("missing extra_body in YAML:\n%s", data)
	}
	var got ModelConfig
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.ReasoningEffort != "medium" {
		t.Errorf("ReasoningEffort = %q, want medium", got.ReasoningEffort)
	}
	if got.ExtraBody["search_parameters"] == nil {
		t.Errorf("extra_body lost in YAML round-trip: %+v", got.ExtraBody)
	}
}

func TestModelConfig_RequestTimeoutParsing(t *testing.T) {
	jsonData := `{
		"model_name": "slow-local",
		"model": "openai/local-model",
		"api_base": "http://localhost:11434/v1",
		"request_timeout": 300
	}`

	var cfg ModelConfig
	if err := json.Unmarshal([]byte(jsonData), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.RequestTimeout != 300 {
		t.Fatalf("RequestTimeout = %d, want 300", cfg.RequestTimeout)
	}
}

func TestModelConfig_RequestTimeoutDefaultZeroValue(t *testing.T) {
	jsonData := `{
		"model_name": "default-timeout",
		"model": "openai/gpt-4o",
		"api_key": "test-key"
	}`

	var cfg ModelConfig
	if err := json.Unmarshal([]byte(jsonData), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.RequestTimeout != 0 {
		t.Fatalf("RequestTimeout = %d, want 0", cfg.RequestTimeout)
	}
}
