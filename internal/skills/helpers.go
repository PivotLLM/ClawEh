package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/skills"
	"github.com/PivotLLM/ClawEh/utils"
)

const skillsSearchMaxResults = 20

func skillsListCmd(loader *skills.SkillsLoader) {
	allSkills := loader.ListSkills()

	if len(allSkills) == 0 {
		fmt.Println("No skills installed.")
		return
	}

	fmt.Println("\nInstalled Skills:")
	fmt.Println("------------------")
	for _, skill := range allSkills {
		fmt.Printf("  ✓ %s (%s)\n", skill.Name, skill.Source)
		if skill.Description != "" {
			fmt.Printf("    %s\n", skill.Description)
		}
	}
}

func skillsInstallCmd(installer *skills.SkillInstaller, repo string) error {
	fmt.Printf("Installing skill from %s...\n", repo)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := installer.InstallFromGitHub(ctx, repo); err != nil {
		return fmt.Errorf("failed to install skill: %w", err)
	}

	fmt.Printf("\u2713 Skill '%s' installed successfully!\n", filepath.Base(repo))

	return nil
}

// skillsInstallFromRegistry installs a skill from a named registry (e.g. clawhub).
func skillsInstallFromRegistry(cfg *config.Config, registryName, slug string) error {
	err := utils.ValidateSkillIdentifier(registryName)
	if err != nil {
		return fmt.Errorf("✗  invalid registry name: %w", err)
	}

	err = utils.ValidateSkillIdentifier(slug)
	if err != nil {
		return fmt.Errorf("✗  invalid slug: %w", err)
	}

	fmt.Printf("Installing skill '%s' from %s registry...\n", slug, registryName)

	registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
		MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
		ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
	})

	registry := registryMgr.GetRegistry(registryName)
	if registry == nil {
		//nolint:staticcheck // ST1005: user-facing CLI message, formatted for the terminal
		return fmt.Errorf("✗  registry '%s' not found or not enabled. check your config.json.", registryName)
	}

	targetDir := filepath.Join(cfg.SkillsPath(), slug)

	if _, err = os.Stat(targetDir); err == nil {
		return fmt.Errorf("\u2717 skill '%s' already installed at %s", slug, targetDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err = os.MkdirAll(cfg.SkillsPath(), 0o700); err != nil {
		return fmt.Errorf("\u2717 failed to create skills directory: %w", err)
	}

	result, err := registry.DownloadAndInstall(ctx, slug, "", targetDir)
	if err != nil {
		rmErr := os.RemoveAll(targetDir)
		if rmErr != nil {
			fmt.Printf("\u2717 Failed to remove partial install: %v\n", rmErr)
		}
		return fmt.Errorf("✗ failed to install skill: %w", err)
	}

	if result.IsMalwareBlocked {
		rmErr := os.RemoveAll(targetDir)
		if rmErr != nil {
			fmt.Printf("\u2717 Failed to remove partial install: %v\n", rmErr)
		}

		//nolint:staticcheck // ST1005: user-facing CLI message, formatted for the terminal
		return fmt.Errorf("\u2717 Skill '%s' is flagged as malicious and cannot be installed.\n", slug)
	}

	if result.IsSuspicious {
		fmt.Printf("\u26a0\ufe0f  Warning: skill '%s' is flagged as suspicious.\n", slug)
	}

	fmt.Printf("\u2713 Skill '%s' v%s installed successfully!\n", slug, result.Version)
	if result.Summary != "" {
		fmt.Printf("  %s\n", result.Summary)
	}

	return nil
}

func skillsRemoveCmd(installer *skills.SkillInstaller, skillName string) {
	fmt.Printf("Removing skill '%s'...\n", skillName)

	if err := installer.Uninstall(skillName); err != nil {
		fmt.Printf("✗ Failed to remove skill: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Skill '%s' removed successfully!\n", skillName)
}

func skillsSearchCmd(query string) {
	fmt.Println("Searching for available skills...")

	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Printf("✗ Failed to load config: %v\n", err)
		return
	}

	registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
		MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
		ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := registryMgr.SearchAll(ctx, query, skillsSearchMaxResults)
	if err != nil {
		fmt.Printf("✗ Failed to fetch skills list: %v\n", err)
		return
	}

	if len(results) == 0 {
		fmt.Println("No skills available.")
		return
	}

	fmt.Printf("\nAvailable Skills (%d):\n", len(results))
	fmt.Println("--------------------")
	for _, result := range results {
		fmt.Printf("  📦 %s\n", result.DisplayName)
		fmt.Printf("     %s\n", result.Summary)
		fmt.Printf("     Slug: %s\n", result.Slug)
		fmt.Printf("     Registry: %s\n", result.RegistryName)
		if result.Version != "" {
			fmt.Printf("     Version: %s\n", result.Version)
		}
		fmt.Println()
	}
}

func skillsShowCmd(loader *skills.SkillsLoader, skillName string) {
	content, ok := loader.LoadSkill(skillName)
	if !ok {
		fmt.Printf("✗ Skill '%s' not found\n", skillName)
		return
	}

	fmt.Printf("\n📦 Skill: %s\n", skillName)
	fmt.Println("----------------------")
	fmt.Println(content)
}
