package tools

import (
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/skills"
)

// TestFormatSearchResults_Cached verifies the (cached) annotation is added.
func TestFormatSearchResults_Cached(t *testing.T) {
	results := []skills.SearchResult{
		{Slug: "github", Score: 0.9, RegistryName: "clawhub"},
	}
	output := formatSearchResults("github", results, true)
	if !strings.Contains(output, "cached") {
		t.Errorf("expected 'cached' in output, got: %s", output)
	}
}

// TestFormatSearchResults_NoVersion verifies result without version.
func TestFormatSearchResults_NoVersion(t *testing.T) {
	results := []skills.SearchResult{
		{Slug: "my-tool", Score: 0.5, RegistryName: "myreg"},
	}
	output := formatSearchResults("tool", results, false)
	if strings.Contains(output, " v") {
		t.Errorf("should not contain version string when version is empty, got: %s", output)
	}
	if !strings.Contains(output, "my-tool") {
		t.Errorf("expected slug 'my-tool' in output, got: %s", output)
	}
}

// TestFormatSearchResults_DisplayNameSameAsSlug verifies that display name matching slug is not duplicated.
func TestFormatSearchResults_DisplayNameSameAsSlug(t *testing.T) {
	results := []skills.SearchResult{
		{Slug: "github", DisplayName: "github", Score: 0.8, RegistryName: "clawhub"},
	}
	output := formatSearchResults("github", results, false)
	// DisplayName == Slug, so the Name line should NOT appear
	if strings.Contains(output, "Name: github") {
		t.Errorf("display name same as slug should not add extra 'Name:' line, got: %s", output)
	}
}

// TestFormatSearchResults_DisplayNameDifferent verifies that distinct display name IS shown.
func TestFormatSearchResults_DisplayNameDifferent(t *testing.T) {
	results := []skills.SearchResult{
		{Slug: "gh", DisplayName: "GitHub Integration", Score: 0.95, RegistryName: "clawhub"},
	}
	output := formatSearchResults("github", results, false)
	if !strings.Contains(output, "GitHub Integration") {
		t.Errorf("expected 'GitHub Integration' in output, got: %s", output)
	}
}

// TestFormatSearchResults_WithSummary verifies summary is shown.
func TestFormatSearchResults_WithSummary(t *testing.T) {
	results := []skills.SearchResult{
		{Slug: "docker", Score: 0.7, RegistryName: "clawhub", Summary: "Docker container management"},
	}
	output := formatSearchResults("docker", results, false)
	if !strings.Contains(output, "Docker container management") {
		t.Errorf("expected summary in output, got: %s", output)
	}
}

// TestInstallSkillTool_ForceReinstall verifies force=true allows reinstall past the "already installed" check.
func TestInstallSkillTool_ForceReinstall(t *testing.T) {
	workspace := t.TempDir()

	// Create a pre-existing skill dir to trigger the "already installed" check
	if err := os.MkdirAll(workspace+"/skills/existing-skill", 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	tool := NewInstallSkillTool(skills.NewRegistryManager(), workspace)
	result := tool.Execute(t.Context(), map[string]any{
		"slug":     "existing-skill",
		"registry": "nonexistent-registry", // no real registry — will fail at registry lookup
		"force":    true,
	})

	// Should fail at registry lookup, NOT at "already installed"
	if !result.IsError {
		t.Fatal("expected error (registry not found)")
	}
	if strings.Contains(result.ForLLM, "already installed") {
		t.Error("force=true should skip the 'already installed' check")
	}
	if !strings.Contains(result.ForLLM, "not found") {
		t.Errorf("expected 'not found' error (registry), got: %s", result.ForLLM)
	}
}

// TestInstallSkillTool_Description verifies tool description is non-empty.
func TestInstallSkillTool_Description(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
	if !strings.Contains(desc, "skill") {
		t.Errorf("Description() = %q, should contain 'skill'", desc)
	}
}
