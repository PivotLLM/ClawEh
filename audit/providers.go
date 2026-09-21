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
	cli := Table{Caption: "CLI providers", Columns: []string{"Model", "Launch command", "Working directory", "Env names"}}
	var modelTables []Table
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if config.IsCLIProtocol(p.Protocol) {
			for _, m := range modelsFor(cfg, p.Name) {
				cmd, wd, envNames := cliLaunch(p, m)
				alias := m.ModelName + " (" + p.Name + ")"
				if !m.Enabled {
					alias += " (disabled)"
				}
				if wd == "." {
					wd = ". (the ClawEh process working directory)"
				}
				cli.Rows = append(cli.Rows, row(alias, cmd, wd, joinOr(envNames, none)))
			}
			continue
		}
		api.Rows = append(api.Rows, row(p.Name, p.Protocol, p.BaseURL, "key: "+setOrNot(p.APIKey), orValue(redactURL(p.Proxy), "")))
		mt := Table{Caption: "Models via " + p.Name, Columns: []string{"Alias", "Model id", "Settings"}}
		for _, m := range modelsFor(cfg, p.Name) {
			mt.Rows = append(mt.Rows, row(m.ModelName, m.Model, modelSettings(m)))
		}
		if len(mt.Rows) == 0 {
			mt.Rows = append(mt.Rows, row("(no models configured)", "", ""))
		}
		modelTables = append(modelTables, mt)
	}
	if len(api.Rows) == 0 {
		api.Rows = append(api.Rows, row(none, "", "", "", ""))
	}
	if len(cli.Rows) == 0 {
		cli.Rows = append(cli.Rows, row(none, "", "", ""))
	}

	d := cfg.Agents.Defaults
	roles := pairs("Model roles",
		row("Default model", orValue(d.DefaultModelName(), none)),
		row("Fallback models", joinOr(tail(d.Models), none)),
		row("Summarization and memory model chain",
			joinOr(cfg.Summarization.Models, "(none: each agent summarizes with its own model)")+
				"; the agent's own model is always the last resort"),
		row("Image model", orValue(d.ImageModel, none)+fallbacks(d.ImageModelFallbacks)),
		row("Vision model (describes images for text-only models)", orValue(d.VisionModel, none)+fallbacks(d.VisionModelFallbacks)),
	)

	return Section{
		Title:  "Providers and models",
		Notes:  []string{"API keys are reported as set or not set only. A CLI provider is a separate program on this host that ClawEh runs per request."},
		Tables: append([]Table{api}, modelTables...),
		Subsections: []Section{
			{
				Title: "CLI providers",
				Notes: []string{"A CLI provider runs as its own program with its own configuration on this host; " +
					"ClawEh's file sandbox applies to ClawEh's tools, not to what the CLI does on its own behalf."},
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

func fallbacks(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return " (fallbacks: " + strings.Join(ss, ", ") + ")"
}
