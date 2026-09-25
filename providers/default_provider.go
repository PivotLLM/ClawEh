// ClawEh
// License: MIT

package providers

import (
	"errors"
	"fmt"

	"github.com/PivotLLM/ClawEh/config"
)

// CreateProvider builds the provider for the agent defaults' model from the
// models configuration. Returns the provider, the model ID to use, and any
// error.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	model := cfg.Agents.Defaults.DefaultModelName()

	// Must have models at this point
	if len(cfg.Models) == 0 {
		return nil, "", errors.New("no models configured. Please add entries to models in your config")
	}

	// Get model config from models
	modelCfg, err := cfg.GetModelConfig(model)
	if err != nil {
		return nil, "", fmt.Errorf("model %q not found in models: %w", model, err)
	}

	prov, err := cfg.GetProvider(modelCfg.Provider)
	if err != nil {
		return nil, "", fmt.Errorf("model %q: %w", model, err)
	}

	// Inject global workspace and timeout if not set in model config.
	// Only inject workspace when a base dir is explicitly configured.
	// CLI providers fall back to "." when workspace is unset.
	if modelCfg.Workspace == "" && cfg.Agents.BaseDir != "" {
		modelCfg.Workspace = cfg.WorkspacePath()
	}
	if modelCfg.RequestTimeout == 0 {
		modelCfg.RequestTimeout = cfg.Agents.Defaults.RequestTimeout
	}

	// Use factory to create provider
	provider, modelID, err := CreateProviderFromConfig(modelCfg, prov)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create provider for model %q: %w", model, err)
	}

	return provider, modelID, nil
}
