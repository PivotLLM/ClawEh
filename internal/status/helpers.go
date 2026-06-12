package status

import (
	"fmt"
	"os"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/auth"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func statusCmd() {
	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	configPath := internal.GetConfigPath()

	fmt.Println("Status")
	fmt.Printf("Version: %s\n", config.FormatVersion())
	build, _ := config.FormatBuildInfo()
	if build != "" {
		fmt.Printf("Build: %s\n", build)
	}
	fmt.Println()

	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("Config:", configPath, "✓")
	} else {
		fmt.Println("Config:", configPath, "✗")
	}

	workspace := cfg.WorkspacePath()
	if _, err := os.Stat(workspace); err == nil {
		fmt.Println("Workspace:", workspace, "✓")
	} else {
		fmt.Println("Workspace:", workspace, "✗")
	}

	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Model: %s\n", cfg.Agents.Defaults.DefaultModelName())

		// Report each configured provider and whether it carries credentials.
		fmt.Printf("\nProviders (%d):\n", len(cfg.Providers))
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			credentialed := p.APIKey != "" || p.AuthMethod != "" || p.BaseURL != ""
			mark := "not set"
			if credentialed {
				mark = "✓"
			}
			detail := p.Protocol
			if p.BaseURL != "" {
				detail = fmt.Sprintf("%s · %s", p.Protocol, p.BaseURL)
			}
			fmt.Printf("  %-16s %s (%s)\n", p.Name+":", mark, detail)
		}

		store, _ := auth.LoadStore()
		if store != nil && len(store.Credentials) > 0 {
			fmt.Println("\nOAuth/Token Auth:")
			for provider, cred := range store.Credentials {
				status := "authenticated"
				if cred.IsExpired() {
					status = "expired"
				} else if cred.NeedsRefresh() {
					status = "needs refresh"
				}
				fmt.Printf("  %s (%s): %s\n", provider, cred.AuthMethod, status)
			}
		}
	}
}
