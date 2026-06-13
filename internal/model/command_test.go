package model

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

var configPath = ""

func initTest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath = filepath.Join(tmpDir, "config.json")
	_ = os.Setenv("CLAW_HOME", tmpDir)
}

// openaiProvider returns a credentialed openai provider for use in test configs.
func openaiProvider() config.Provider {
	return config.Provider{
		Name:     "openai",
		Protocol: "openai-chat",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "test",
	}
}

// anthropicProvider returns a credentialed anthropic provider for use in test configs.
func anthropicProvider() config.Provider {
	return config.Provider{
		Name:     "anthropic",
		Protocol: "anthropic",
		BaseURL:  "https://api.anthropic.com/v1",
		APIKey:   "test",
	}
}

// captureStdout captures stdout during the execution of fn and returns the captured output
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestNewModelCommand(t *testing.T) {
	cmd := NewModelCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "model [model_name]", cmd.Use)
	assert.Equal(t, "Show or change the default model", cmd.Short)

	assert.Len(t, cmd.Aliases, 0)

	assert.False(t, cmd.HasFlags())

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRunE)
	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)
}

func TestShowCurrentModel_WithDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"gpt-4"},
			},
		},
		Providers: []config.Provider{openaiProvider(), anthropicProvider()},
		Models: []config.ModelConfig{
			{ModelName: "gpt-4", Model: "gpt-4", Provider: "openai", Enabled: true},
			{ModelName: "claude-3", Model: "claude-3", Provider: "anthropic", Enabled: true},
		},
	}

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "Current default model: gpt-4")
	assert.Contains(t, output, "Available models in your config:")
	assert.Contains(t, output, "gpt-4")
	assert.Contains(t, output, "claude-3")
}

func TestShowCurrentModel_NoDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "gpt-4", Model: "gpt-4", Provider: "openai", Enabled: true},
		},
	}

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "No default model is currently set.")
	assert.Contains(t, output, "Available models in your config:")
}

func TestShowCurrentModel_WithModelConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"my-model"},
			},
		},
		Models: []config.ModelConfig{},
	}

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "Current default model: my-model")
}

func TestListAvailableModels_Empty(t *testing.T) {
	cfg := &config.Config{
		Models: []config.ModelConfig{},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.Contains(t, output, "No models configured in models")
}

func TestListAvailableModels_WithModels(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"gpt-4"},
			},
		},
		Providers: []config.Provider{
			openaiProvider(),
			anthropicProvider(),
			// Provider without credentials: models referencing it are skipped.
			{Name: "nokey", Protocol: "openai-chat", BaseURL: "https://api.example.com/v1"},
		},
		Models: []config.ModelConfig{
			{ModelName: "gpt-4", Model: "gpt-4", Provider: "openai", Enabled: true},
			{ModelName: "claude-3", Model: "claude-3", Provider: "anthropic", Enabled: true},
			{ModelName: "no-key-model", Model: "test", Provider: "nokey", Enabled: true},
		},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.NotEmpty(t, output)
	assert.Contains(t, output, "> - gpt-4 (gpt-4 via openai)")
	assert.Contains(t, output, "claude-3 (claude-3 via anthropic)")
	assert.NotContains(t, output, "no-key-model")
}

func TestSetDefaultModel_ValidModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"old-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "new-model", Model: "new-model", Provider: "openai", Enabled: true},
			{ModelName: "old-model", Model: "old-model", Provider: "openai", Enabled: true},
		},
	}

	output := captureStdout(func() {
		err := setDefaultModel(configPath, cfg, "new-model")
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Default model changed from 'old-model' to 'new-model'")

	// Verify config was updated
	updatedCfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "new-model", updatedCfg.Agents.Defaults.DefaultModelName())
}

func TestSetDefaultModel_InvalidModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"existing-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "existing-model", Model: "existing", Provider: "openai", Enabled: true},
		},
	}

	assert.Error(t, setDefaultModel(configPath, cfg, "nonexistent-model"))
}

func TestSetDefaultModel_DisabledModel(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"existing-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "existing-model", Model: "existing", Provider: "openai", Enabled: true},
			{ModelName: "disabled-model", Model: "disabled", Provider: "openai", Enabled: false},
		},
	}

	assert.Error(t, setDefaultModel(configPath, cfg, "disabled-model"))
}

func TestSetDefaultModel_SaveConfigError(t *testing.T) {
	// Use an invalid path to trigger save error
	invalidPath := "/nonexistent/directory/config.json"

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"old-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "new-model", Model: "new-model", Provider: "openai", Enabled: true},
		},
	}

	err := setDefaultModel(invalidPath, cfg, "new-model")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config")
}

func TestFormatModelName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "(none)"},
		{"simple model", "gpt-4", "gpt-4"},
		{"model with version", "claude-sonnet-4.6", "claude-sonnet-4.6"},
		{"model with spaces", "my model", "my model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatModelName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestModelCommandExecution_Show(t *testing.T) {
	initTest(t)

	// Create a test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"test-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "test-model", Model: "test", Provider: "openai", Enabled: true},
		},
	}

	err := config.SaveConfig(configPath, cfg)
	require.NoError(t, err)

	cmd := NewModelCommand()

	output := captureStdout(func() {
		err = cmd.RunE(cmd, []string{})
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Current default model: test-model")
}

func TestModelCommandExecution_Set(t *testing.T) {
	initTest(t)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"old-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "old-model", Model: "old", Provider: "openai", Enabled: true},
			{ModelName: "new-model", Model: "new", Provider: "openai", Enabled: true},
		},
	}

	err := config.SaveConfig(configPath, cfg)
	require.NoError(t, err)

	cmd := NewModelCommand()

	output := captureStdout(func() {
		err = cmd.RunE(cmd, []string{"new-model"})
		assert.NoError(t, err)
	})

	assert.Contains(t, output, "Default model changed from 'old-model' to 'new-model'")
}

func TestModelCommandExecution_TooManyArgs(t *testing.T) {
	cmd := NewModelCommand()

	err := cmd.RunE(cmd, []string{"model1", "model2"})

	assert.Error(t, err)
}

func TestListAvailableModels_MarkerLogic(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Models: []string{"middle-model"},
			},
		},
		Providers: []config.Provider{openaiProvider()},
		Models: []config.ModelConfig{
			{ModelName: "first-model", Model: "first", Provider: "openai", Enabled: true},
			{ModelName: "middle-model", Model: "middle", Provider: "openai", Enabled: true},
			{ModelName: "last-model", Model: "last", Provider: "openai", Enabled: true},
		},
	}

	output := captureStdout(func() {
		listAvailableModels(cfg)
	})

	assert.Contains(t, output, "  - first-model (first via openai)")
	assert.Contains(t, output, "> - middle-model (middle via openai)")
	assert.Contains(t, output, "  - last-model (last via openai)")
}
