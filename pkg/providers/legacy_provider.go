// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// CreateProvider creates a provider based on the configuration.
// It uses the models configuration (new format) to create providers.
// The old providers config is automatically converted to models during config loading.
// Returns the provider, the model ID to use, and any error.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	model := cfg.Agents.Defaults.DefaultModelName()

	// Must have models at this point
	if len(cfg.Models) == 0 {
		return nil, "", fmt.Errorf("no models configured. Please add entries to models in your config")
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
	// Only inject workspace when it is explicitly configured (non-empty in Defaults).
	// CLI providers fall back to "." when workspace is unset.
	if modelCfg.Workspace == "" && cfg.Agents.Defaults.Workspace != "" {
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
