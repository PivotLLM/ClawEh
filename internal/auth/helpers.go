package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/auth"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

const (
	supportedProvidersMsg = "supported providers: openai, anthropic"
	defaultAnthropicModel = "claude-sonnet-4.6"
)

func authLoginCmd(provider string, useDeviceCode bool, useOauth bool) error {
	switch provider {
	case "openai":
		return authLoginOpenAI(useDeviceCode)
	case "anthropic":
		return authLoginAnthropic(useOauth)
	default:
		return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
}

func authLoginOpenAI(useDeviceCode bool) error {
	cfg := auth.OpenAIOAuthConfig()

	var cred *auth.AuthCredential
	var err error

	if useDeviceCode {
		cred, err = auth.LoginDeviceCode(cfg)
	} else {
		cred, err = auth.LoginBrowser(cfg)
	}

	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential("openai", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		alias := setProtocolAuth(appCfg, "openai", "https://api.openai.com/v1", "oauth", "gpt-5.4", "gpt-5.4")
		appCfg.Agents.Defaults.SetDefaultModel(alias)

		if err = config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Println("Login successful!")
	if cred.AccountID != "" {
		fmt.Printf("Account: %s\n", cred.AccountID)
	}
	fmt.Println("Default model set to: gpt-5.4")

	return nil
}

func authLoginAnthropic(useOauth bool) error {
	if useOauth {
		return authLoginAnthropicSetupToken()
	}

	fmt.Println("Anthropic login method:")
	fmt.Println("  1) Setup token (from `claude setup-token`) (Recommended)")
	fmt.Println("  2) API key (from console.anthropic.com)")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Choose [1]: ")
		choice := "1"
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text != "" {
				choice = text
			}
		}

		switch choice {
		case "1":
			return authLoginAnthropicSetupToken()
		case "2":
			return authLoginPasteToken("anthropic")
		default:
			fmt.Printf("Invalid choice: %s. Please enter 1 or 2.\n", choice)
		}
	}
}

func authLoginAnthropicSetupToken() error {
	cred, err := auth.LoginSetupToken(os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential("anthropic", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		alias := setProtocolAuth(appCfg, "anthropic", "https://api.anthropic.com/v1", "oauth", defaultAnthropicModel, defaultAnthropicModel)
		// Only set default model if user has no default configured yet
		if appCfg.Agents.Defaults.DefaultModelName() == "" {
			appCfg.Agents.Defaults.SetDefaultModel(alias)
		}

		if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Println("Setup token saved for Anthropic!")

	return nil
}

func authLoginPasteToken(provider string) error {
	cred, err := auth.LoginPasteToken(provider, os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential(provider, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		switch provider {
		case "anthropic":
			alias := setProtocolAuth(appCfg, "anthropic", "https://api.anthropic.com/v1", "token", defaultAnthropicModel, defaultAnthropicModel)
			appCfg.Agents.Defaults.SetDefaultModel(alias)
		case "openai":
			alias := setProtocolAuth(appCfg, "openai", "https://api.openai.com/v1", "token", "gpt-5.4", "gpt-5.4")
			appCfg.Agents.Defaults.SetDefaultModel(alias)
		}
		if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Printf("Token saved for %s!\n", provider)

	if appCfg != nil {
		fmt.Printf("Default model set to: %s\n", appCfg.Agents.Defaults.DefaultModelName())
	}

	return nil
}

func authLogoutCmd(provider string) error {
	if provider != "" {
		if err := auth.DeleteCredential(provider); err != nil {
			return fmt.Errorf("failed to remove credentials: %w", err)
		}

		appCfg, err := internal.LoadConfig()
		if err == nil {
			clearProtocolAuth(appCfg, provider)
			config.SaveConfig(internal.GetConfigPath(), appCfg)
		}

		fmt.Printf("Logged out from %s\n", provider)

		return nil
	}

	if err := auth.DeleteAllCredentials(); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		// Clear auth method on every provider.
		for i := range appCfg.Providers {
			appCfg.Providers[i].AuthMethod = ""
		}
		config.SaveConfig(internal.GetConfigPath(), appCfg)
	}

	fmt.Println("Logged out from all providers")

	return nil
}

func authStatusCmd() error {
	store, err := auth.LoadStore()
	if err != nil {
		return fmt.Errorf("failed to load auth store: %w", err)
	}

	if len(store.Credentials) == 0 {
		fmt.Println("No authenticated providers.")
		fmt.Println("Run: " + internal.BinaryName + " auth login --provider <name>")
		return nil
	}

	fmt.Println("\nAuthenticated Providers:")
	fmt.Println("------------------------")
	for provider, cred := range store.Credentials {
		status := "active"
		if cred.IsExpired() {
			status = "expired"
		} else if cred.NeedsRefresh() {
			status = "needs refresh"
		}

		fmt.Printf("  %s:\n", provider)
		fmt.Printf("    Method: %s\n", cred.AuthMethod)
		fmt.Printf("    Status: %s\n", status)
		if cred.AccountID != "" {
			fmt.Printf("    Account: %s\n", cred.AccountID)
		}
		if cred.Email != "" {
			fmt.Printf("    Email: %s\n", cred.Email)
		}
		if cred.ProjectID != "" {
			fmt.Printf("    Project: %s\n", cred.ProjectID)
		}
		if !cred.ExpiresAt.IsZero() {
			fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04"))
		}

		if provider == "anthropic" && cred.AuthMethod == "oauth" {
			usage, err := auth.FetchAnthropicUsage(cred.AccessToken)
			if err != nil {
				fmt.Printf("    Usage: unavailable (%v)\n", err)
			} else {
				fmt.Printf("    Usage (5h):  %.1f%%\n", usage.FiveHourUtilization*100)
				fmt.Printf("    Usage (7d):  %.1f%%\n", usage.SevenDayUtilization*100)
			}
		}
	}

	return nil
}

// ensureProtocolProvider returns the first provider speaking the given
// protocol, creating one (named after the protocol, with the given default base
// URL) when none exists. The returned pointer is valid until cfg.Providers is
// next appended to.
func ensureProtocolProvider(cfg *config.Config, protocol, baseURL string) *config.Provider {
	if p := cfg.FindProviderByProtocol(protocol); p != nil {
		return p
	}
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name:     protocol,
		Protocol: protocol,
		BaseURL:  baseURL,
	})
	return &cfg.Providers[len(cfg.Providers)-1]
}

// setProtocolAuth attaches an OAuth/token auth method to the provider for the
// given protocol (creating it if needed), ensures a model_list entry references
// that provider, and returns the model alias to use as default.
func setProtocolAuth(cfg *config.Config, protocol, baseURL, method, modelAlias, modelID string) string {
	prov := ensureProtocolProvider(cfg, protocol, baseURL)
	prov.AuthMethod = method

	for i := range cfg.ModelList {
		if cfg.ModelList[i].Provider == prov.Name {
			return cfg.ModelList[i].ModelName
		}
	}
	cfg.ModelList = append(cfg.ModelList, config.ModelConfig{
		ModelName: modelAlias,
		Model:     modelID,
		Provider:  prov.Name,
		Enabled:   true,
	})
	return modelAlias
}

// clearProtocolAuth clears the auth method on every provider speaking the given
// protocol.
func clearProtocolAuth(cfg *config.Config, protocol string) {
	for i := range cfg.Providers {
		if cfg.Providers[i].Protocol == protocol {
			cfg.Providers[i].AuthMethod = ""
		}
	}
}
