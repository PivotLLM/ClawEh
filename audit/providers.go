// ClawEh
// License: MIT

package audit

import (
	"context"
	"slices"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// cliModelSentinels are the model ids each CLI provider treats as "let the CLI
// pick its own model" and passes no model flag for (spawnllm's providers).
var cliModelSentinels = map[string][]string{
	"claude-cli":      {"claude-cli", "claude-code"},
	"codex-cli":       {"codex-cli"},
	"antigravity-cli": {"antigravity-cli", "agy", "antigravity"},
	"cursor-cli":      {"cursor-cli", "cursor-agent", "cursor"},
}

// cliLaunch is the command line a CLI model runs as, built the way
// providers/factory_provider.go and spawnllm build it: the provider's command
// (or the protocol's binary on PATH), the protocol's base arguments, the
// required arguments plus the model's extra_args, the model flag unless the id
// is the CLI's sentinel, the working directory for codex, then the trailing
// stdin marker.
func cliLaunch(prov *config.Provider, m *config.ModelConfig) (command, workdir string, envNames []string) {
	agent := config.CLIAgentByProtocol(prov.Protocol)
	workdir = m.Workspace
	if workdir == "" {
		workdir = "."
	}
	if agent == nil {
		return orValue(prov.Command, prov.Protocol) + " " + strings.Join(m.ExtraArgs, " "), workdir, sortedKeys(m.Env)
	}
	args := append([]string{}, agent.BaseArgs...)
	args = append(args, config.CLIArgs(prov.Protocol, m.ExtraArgs)...)
	if m.Model != "" && !slices.Contains(cliModelSentinels[agent.Protocol], m.Model) {
		flag := "--model"
		if agent.Protocol == "codex-cli" {
			flag = "-m"
		}
		args = append(args, flag, m.Model)
	}
	if agent.Protocol == "codex-cli" {
		args = append(args, "-C", workdir)
	}
	args = append(args, agent.TrailingArgs...)
	parts := append([]string{orValue(prov.Command, agent.Binary)}, args...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t\"'") {
			parts[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " "), workdir, sortedKeys(config.CLIEnv(prov.Protocol, m.Env))
}

// modelSettings lists the flags set on a model that change what it may do.
func modelSettings(m *config.ModelConfig) string {
	var s []string
	if !m.Enabled {
		s = append(s, "disabled")
	}
	if m.NoTools {
		s = append(s, "no tools")
	}
	if m.ThinkingLevel != "" {
		s = append(s, "thinking "+m.ThinkingLevel)
	}
	if m.Vision != "" && m.Vision != config.VisionOff {
		s = append(s, "vision "+m.Vision)
	}
	if m.RequestTimeout > 0 {
		s = append(s, "timeout "+itoa(m.RequestTimeout)+"s")
	}
	if m.ContextWindow > 0 {
		s = append(s, "context "+itoa(m.ContextWindow))
	}
	if m.RPM > 0 {
		s = append(s, "rpm "+itoa(m.RPM))
	}
	if m.ReasoningEffort != "" {
		s = append(s, "reasoning "+m.ReasoningEffort)
	}
	return strings.Join(s, ", ")
}

func modelsFor(cfg *config.Config, provider string) []*config.ModelConfig {
	var out []*config.ModelConfig
	for i := range cfg.Models {
		if cfg.Models[i].Provider == provider {
			out = append(out, &cfg.Models[i])
		}
	}
	return out
}

func collectProviders(_ context.Context, cfg *config.Config, _ Environment) Section {
	api := Table{Caption: "API providers", Columns: []string{"Provider", "Protocol", "Base URL", "API key", "Proxy"}}
	models := Table{Caption: "Enabled models", Columns: []string{"Alias", "Provider", "Model id", "Settings"}}
	cli := Table{Caption: "CLI providers", Columns: []string{"Model", "Launch command", "Working directory", "Env names"}}
	var idle []string
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		var enabled []*config.ModelConfig
		for _, m := range modelsFor(cfg, p.Name) {
			if m.Enabled {
				enabled = append(enabled, m)
			}
		}
		if len(enabled) == 0 {
			idle = append(idle, p.Name)
			continue
		}
		for _, m := range enabled {
			models.Rows = append(models.Rows, row(m.ModelName, p.Name, m.Model, modelSettings(m)))
		}
		if config.IsCLIProtocol(p.Protocol) {
			for _, m := range enabled {
				cmd, wd, envNames := cliLaunch(p, m)
				if wd == "." {
					wd = "process working directory"
				}
				cli.Rows = append(cli.Rows, row(m.ModelName+" ("+p.Name+")", cmd, wd, joinOr(envNames, none)))
			}
			continue
		}
		api.Rows = append(api.Rows, row(p.Name, p.Protocol, p.BaseURL, yesNo(p.APIKey != ""), orValue(redactURL(p.Proxy), "")))
	}
	if len(api.Rows) == 0 {
		api.Rows = append(api.Rows, row(none, "", "", "", ""))
	}
	if len(models.Rows) == 0 {
		models.Rows = append(models.Rows, row(none, "", "", ""))
	}
	if len(cli.Rows) == 0 {
		cli.Rows = append(cli.Rows, row(none, "", "", ""))
	}
	notes := []string{"Only providers with at least one enabled model are listed; a provider whose models are all disabled sends nothing."}
	if len(idle) > 0 {
		notes = append(notes, "Configured but unused (no enabled model): "+strings.Join(idle, ", ")+".")
	}

	d := cfg.Agents.Defaults
	chain := joinLines(cfg.Summarization.Models, "(none: each agent summarizes with its own model)") +
		"\nthe agent's own model is always the last resort"
	roles := Table{Columns: []string{"Purpose", "Model"}, Rows: [][]string{
		row("Default model", orValue(d.DefaultModelName(), none)),
		row("Fallback models", joinLines(tail(d.Models), none)),
		row("Summarization and memory model chain", chain),
		row("Image model", joinLines(append([]string{orValue(d.ImageModel, none)}, d.ImageModelFallbacks...), none)),
		row("Vision model (describes images for text-only models)",
			joinLines(append([]string{orValue(d.VisionModel, none)}, d.VisionModelFallbacks...), none)),
	}}

	return Section{
		Title:  "Providers and models",
		Notes:  notes,
		Tables: []Table{api, models},
		Subsections: []Section{
			{
				Title: "CLI providers",
				Notes: []string{"A CLI provider runs as its own program with its own configuration on this host; " +
					"ClawEh's file sandbox applies to ClawEh's tools, not to what the CLI does on its own behalf. " +
					"\"process working directory\" is where the ClawEh service was started, not the agent's workspace."},
				Tables: []Table{cli},
			},
			{Title: "Model roles", Tables: []Table{roles}},
		},
	}
}

func tail(ss []string) []string {
	if len(ss) <= 1 {
		return nil
	}
	return ss[1:]
}
