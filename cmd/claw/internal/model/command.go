package model

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

// LocalModel is a special model name that indicates that the model is local and with or without api_key.
const LocalModel = "local-model"

func NewModelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model [model_name]",
		Short: "Show or change the default model",
		Long: fmt.Sprintf(`Show or change the default model configuration.

If no argument is provided, shows the current default model.
If a model name is provided, sets it as the default model.

Examples:
  %s model                    # Show current default model
  %s model gpt-5.2           # Set gpt-5.2 as default
  %s model claude-sonnet-4.6 # Set claude-sonnet-4.6 as default
  %s model local-model       # Set local VLLM server as default

Note: 'local-model' is a special value for using a local VLLM server
(running at localhost:8000 by default) which does not require an API key.`,
			internal.BinaryName, internal.BinaryName, internal.BinaryName, internal.BinaryName),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()

			// Load current config
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if len(args) == 0 {
				// Show current default model
				showCurrentModel(cfg)
				return nil
			}

			// Set new default model
			modelName := args[0]
			return setDefaultModel(configPath, cfg, modelName)
		},
	}

	return cmd
}

func showCurrentModel(cfg *config.Config) {
	defaultModel := cfg.Agents.Defaults.DefaultModelName()

	if defaultModel == "" {
		fmt.Println("No default model is currently set.")
		fmt.Println("\nAvailable models in your config:")
		listAvailableModels(cfg)
	} else {
		fmt.Printf("Current default model: %s\n", defaultModel)
		fmt.Println("\nAvailable models in your config:")
		listAvailableModels(cfg)
	}
}

func listAvailableModels(cfg *config.Config) {
	if len(cfg.ModelList) == 0 {
		fmt.Println("  No models configured in model_list")
		return
	}

	defaultModel := cfg.Agents.Defaults.DefaultModelName()

	for _, model := range cfg.ModelList {
		if model.APIKey == "" || !model.Enabled {
			continue
		}
		marker := "  "
		if model.ModelName == defaultModel {
			marker = "> "
		}
		fmt.Printf("%s- %s (%s)\n", marker, model.ModelName, model.Model)
	}
}

func setDefaultModel(configPath string, cfg *config.Config, modelName string) error {
	// Validate that the model exists in model_list and is enabled
	modelFound := false
	for _, model := range cfg.ModelList {
		if model.APIKey != "" && model.ModelName == modelName {
			modelFound = true
			if !model.Enabled {
				return fmt.Errorf("model '%s' is disabled; enable it before setting as default", modelName)
			}
			break
		}
	}

	if !modelFound && modelName != LocalModel {
		return fmt.Errorf("cannot found model '%s' in config", modelName)
	}

	// Update the default model
	oldModel := cfg.Agents.Defaults.DefaultModelName()

	cfg.Agents.Defaults.SetDefaultModel(modelName)

	// Save config back to file
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Default model changed from '%s' to '%s'\n",
		formatModelName(oldModel), modelName)
	fmt.Println("\nThe new default model will be used for all agent interactions.")

	return nil
}

func formatModelName(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}
