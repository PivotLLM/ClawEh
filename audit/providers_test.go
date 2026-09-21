// ClawEh
// License: MIT

package audit

import (
	"strings"
	"testing"
)

func TestCollectProviders_APIKeyNeverShown(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectProviders(cfg, env)
	api := findTable(t, s, "API providers")
	_, r := findRow(t, api, "OpenAI")
	if r[3] != "key: set" {
		t.Errorf("key column = %q, want %q", r[3], "key: set")
	}
	if strings.Contains(tableText(api), secretAPIKey) {
		t.Error("API key value leaked")
	}
	cfg.Providers[0].APIKey = ""
	api = findTable(t, collectProviders(cfg, env), "API providers")
	_, r = findRow(t, api, "OpenAI")
	if r[3] != "key: not set" {
		t.Errorf("key column = %q, want %q", r[3], "key: not set")
	}
}

func TestCollectProviders_DisabledModelMarked(t *testing.T) {
	cfg, env := fixtureConfig(t)
	models := findTable(t, collectProviders(cfg, env), "Models via OpenAI")
	_, old := findRow(t, models, "GPT Old")
	contains(t, old[2], "disabled", "disabled model settings")
	contains(t, old[2], "no tools", "no_tools flag")
	_, gpt := findRow(t, models, "GPT")
	if strings.Contains(gpt[2], "disabled") {
		t.Errorf("enabled model marked disabled: %q", gpt[2])
	}
	contains(t, gpt[2], "thinking high", "thinking level")
	contains(t, gpt[2], "context 200000", "context window")
}

func TestCollectProviders_CLILaunchLine(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectProviders(cfg, env)
	var cli Table
	for _, sub := range s.Subsections {
		if sub.Title == "CLI providers" {
			cli = sub.Tables[0]
			contains(t, sub.Notes[0], "ClawEh's file sandbox applies to ClawEh's tools", "CLI note")
		}
	}
	_, claude := findRow(t, cli, "Claude CLI (Claude CLI)")
	// Base args, then required + extra args (deduplicated), no --model for the
	// sentinel id, then the stdin marker.
	want := "claude -p --output-format json --dangerously-skip-permissions --no-chrome --verbose -"
	if claude[1] != want {
		t.Errorf("claude launch = %q\nwant %q", claude[1], want)
	}
	contains(t, claude[2], ". (the ClawEh process working directory)", "claude workdir")
	contains(t, claude[3], "CLAUDE_CODE_DISABLE_AUTO_MEMORY", "env names")
	contains(t, claude[3], "MY_SECRET_ENV", "env names")
	if strings.Contains(tableText(cli), secretEnvValue) {
		t.Error("env value leaked into the CLI table")
	}

	_, codex := findRow(t, cli, "Codex Fast (Codex CLI)")
	want = "/opt/codex/bin/codex exec --json --color never --dangerously-bypass-approvals-and-sandbox " +
		"--skip-git-repo-check -m o4-mini -C /tmp/codex-ws -"
	if codex[1] != want {
		t.Errorf("codex launch = %q\nwant %q", codex[1], want)
	}
	if codex[2] != "/tmp/codex-ws" {
		t.Errorf("codex workdir = %q", codex[2])
	}
}

func TestCollectProviders_ModelRoles(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.Defaults.ImageModel = "GPT"
	cfg.Summarization.Models = []string{"GPT Old"}
	s := collectProviders(cfg, env)
	var roles Table
	for _, sub := range s.Subsections {
		if sub.Title == "Model roles" {
			roles = sub.Tables[0]
		}
	}
	_, def := findRow(t, roles, "Default model")
	if def[1] != "GPT" {
		t.Errorf("default model = %q", def[1])
	}
	_, fb := findRow(t, roles, "Fallback models")
	if fb[1] != "Claude CLI" {
		t.Errorf("fallbacks = %q", fb[1])
	}
	_, sum := findRow(t, roles, "Summarization")
	contains(t, sum[1], "GPT Old", "summarization chain")
	_, img := findRow(t, roles, "Image model")
	if img[1] != "GPT" {
		t.Errorf("image model = %q", img[1])
	}
}
