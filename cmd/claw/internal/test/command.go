package test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

const testPrompt = `Reply with only the single word: OK`

const defaultTestTimeout = 30 * time.Second

type result struct {
	modelName string
	ok        bool
	response  string
	err       string
	elapsed   time.Duration
}

func NewTestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run diagnostic tests",
	}

	cmd.AddCommand(newModelsCommand())
	cmd.AddCommand(newModelCommand())

	return cmd
}

func newModelsCommand() *cobra.Command {
	var timeout int

	cmd := &cobra.Command{
		Use:   "models",
		Short: "Test connectivity to all enabled models",
		Long: `Sends a simple prompt to every enabled model in model_list and reports which are reachable.

Each model is asked to reply with "OK".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig(internal.GetConfigPath())
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if len(cfg.ModelList) == 0 {
				fmt.Println("No models configured in model_list.")
				return nil
			}

			deadline := time.Duration(timeout) * time.Second

			if len(args) == 1 {
				return testOne(cfg, args[0], deadline)
			}

			return testAll(cfg, deadline)
		},
	}

	cmd.Flags().IntVarP(&timeout, "timeout", "t", int(defaultTestTimeout.Seconds()),
		"Timeout in seconds per model")

	return cmd
}

func newModelCommand() *cobra.Command {
	var timeout int

	cmd := &cobra.Command{
		Use:   "model <model_name>",
		Short: "Test connectivity to a specific model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig(internal.GetConfigPath())
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			return testOne(cfg, args[0], time.Duration(timeout)*time.Second)
		},
	}

	cmd.Flags().IntVarP(&timeout, "timeout", "t", int(defaultTestTimeout.Seconds()),
		"Timeout in seconds per model")

	return cmd
}

func testOne(cfg *config.Config, name string, deadline time.Duration) error {
	for _, mc := range cfg.ModelList {
		if mc.ModelName == name {
			if !mc.Enabled {
				return fmt.Errorf("model %q is disabled", name)
			}
			printResults([]result{testModel(mc, deadline)})
			return nil
		}
	}
	return fmt.Errorf("model %q not found in model_list", name)
}

func testAll(cfg *config.Config, deadline time.Duration) error {
	var enabled []config.ModelConfig
	for _, mc := range cfg.ModelList {
		if mc.Enabled {
			enabled = append(enabled, mc)
		}
	}
	disabledCount := len(cfg.ModelList) - len(enabled)

	if len(enabled) == 0 {
		fmt.Println("No enabled models in model_list.")
		return nil
	}

	if disabledCount > 0 {
		fmt.Printf("Testing %d model(s) (%d disabled, skipped) (timeout: %ds)...\n\n",
			len(enabled), disabledCount, int(deadline.Seconds()))
	} else {
		fmt.Printf("Testing %d model(s) (timeout: %ds)...\n\n",
			len(enabled), int(deadline.Seconds()))
	}

	results := make([]result, len(enabled))
	var wg sync.WaitGroup
	for i, mc := range enabled {
		wg.Add(1)
		go func(idx int, mc config.ModelConfig) {
			defer wg.Done()
			results[idx] = testModel(mc, deadline)
		}(i, mc)
	}
	wg.Wait()

	printResults(results)
	return nil
}

func testModel(mc config.ModelConfig, timeout time.Duration) result {
	r := result{modelName: mc.ModelName}

	provider, modelID, err := providers.CreateProviderFromConfig(&mc)
	if err != nil {
		r.err = fmt.Sprintf("provider init: %v", err)
		return r
	}

	if sp, ok := provider.(providers.StatefulProvider); ok {
		defer sp.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	messages := []providers.Message{
		{Role: "user", Content: testPrompt},
	}

	start := time.Now()
	resp, err := provider.Chat(ctx, messages, nil, modelID, nil)
	r.elapsed = time.Since(start)

	if err != nil {
		r.err = err.Error()
		return r
	}

	r.response = strings.TrimSpace(resp.Content)
	r.ok = true
	return r
}

func printResults(results []result) {
	passCount := 0
	for _, r := range results {
		if r.ok {
			passCount++
		}
	}

	fmt.Printf("%-30s  %-8s  %-8s  %s\n", "MODEL", "STATUS", "TIME", "RESPONSE/ERROR")
	fmt.Printf("%-30s  %-8s  %-8s  %s\n",
		strings.Repeat("-", 30), strings.Repeat("-", 8),
		strings.Repeat("-", 8), strings.Repeat("-", 40))

	for _, r := range results {
		if r.ok {
			fmt.Printf("%-30s  %-8s  %-8s  %s\n",
				r.modelName, "OK", formatDuration(r.elapsed), r.response)
		} else {
			fmt.Printf("%-30s  %-8s  %-8s  %s\n",
				r.modelName, "FAIL", formatDuration(r.elapsed), r.err)
		}
	}

	fmt.Printf("\n%d/%d models reachable\n", passCount, len(results))
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
