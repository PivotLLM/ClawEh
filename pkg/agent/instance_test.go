package agent

import (
	"os"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestNewAgentInstance_UsesDefaultsTemperatureAndMaxTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             &config.AgentModelConfig{Primary: "test-model"},
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 1.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.MaxTokens != 1234 {
		t.Fatalf("MaxTokens = %d, want %d", agent.MaxTokens, 1234)
	}
	if agent.Temperature != 1.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 1.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             &config.AgentModelConfig{Primary: "test-model"},
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 0.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenUnset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             &config.AgentModelConfig{Primary: "test-model"},
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.2 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.2)
	}
}

func TestNewAgentInstance_ResolveCandidatesFromModelListAlias(t *testing.T) {
	tests := []struct {
		name         string
		aliasName    string
		modelName    string
		provider     config.Provider
		wantProvider string
		wantModel    string
	}{
		{
			name:      "alias routed through openrouter provider",
			aliasName: "step-3.5-flash",
			modelName: "stepfun/step-3.5-flash:free",
			provider: config.Provider{
				Name: "openrouter", Protocol: "openai",
				BaseURL: "https://openrouter.ai/api/v1", APIKey: "k",
			},
			wantProvider: "openrouter",
			wantModel:    "stepfun/step-3.5-flash:free",
		},
		{
			name:      "alias routed through openai-compatible provider",
			aliasName: "glm-5",
			modelName: "glm-5",
			provider: config.Provider{
				Name: "openai", Protocol: "openai",
				BaseURL: "https://api.z.ai/api/coding/paas/v4", APIKey: "k",
			},
			wantProvider: "openai",
			wantModel:    "glm-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace: tmpDir,
						Model:     &config.AgentModelConfig{Primary: tt.aliasName},
					},
				},
				Providers: []config.Provider{tt.provider},
				ModelList: []config.ModelConfig{
					{
						ModelName: tt.aliasName,
						Model:     tt.modelName,
						Provider:  tt.provider.Name,
						Enabled:   true,
					},
				},
			}

			provider := &mockProvider{}
			agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

			if len(agent.Candidates) != 1 {
				t.Fatalf("len(Candidates) = %d, want 1", len(agent.Candidates))
			}
			if agent.Candidates[0].Provider != tt.wantProvider {
				t.Fatalf("candidate provider = %q, want %q", agent.Candidates[0].Provider, tt.wantProvider)
			}
			if agent.Candidates[0].Model != tt.wantModel {
				t.Fatalf("candidate model = %q, want %q", agent.Candidates[0].Model, tt.wantModel)
			}
		})
	}
}
