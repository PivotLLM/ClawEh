// ClawEh
// License: MIT

package audit

import (
	"context"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/skills"
	"github.com/PivotLLM/ClawEh/voice"
)

func mcpServerRows(cfg *config.Config) [][]string {
	var rows [][]string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		s := cfg.Tools.MCP.Servers[name]
		transport := orValue(s.Type, "stdio")
		target := s.URL
		if s.URL == "" {
			target = strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
		}
		var secrets []string
		if len(s.Env) > 0 {
			secrets = append(secrets, "env: "+strings.Join(sortedKeys(s.Env), ", "))
		}
		if s.EnvFile != "" {
			secrets = append(secrets, "env_file: "+s.EnvFile)
		}
		if len(s.Headers) > 0 {
			secrets = append(secrets, "headers: "+strings.Join(sortedKeys(s.Headers), ", "))
		}
		rows = append(rows, row(name, yesNo(s.Enabled), transport, target,
			orValue(strings.Join(secrets, "; "), none), joinOr(agentsReachingServer(cfg, name), "(no agent)")))
	}
	if len(rows) == 0 {
		rows = append(rows, row(none, "", "", "", "", ""))
	}
	return rows
}

func skillRows(cfg *config.Config) [][]string {
	loader := skills.NewSkillsLoader(cfg.WorkspacePath(), cfg.SkillsPath(), "")
	var rows [][]string
	for _, s := range loader.ListSkills() {
		rows = append(rows, row(s.Name, s.Source, s.Description))
	}
	if len(rows) == 0 {
		rows = append(rows, row("(none installed)", "", ""))
	}
	return rows
}

func keyCount(key string, keys []string) string {
	n := len(config.MergeAPIKeys(key, keys))
	if n == 0 {
		return "key not set"
	}
	return "key set (" + itoa(n) + ")"
}

func webToolRows(cfg *config.Config) [][]string {
	w := cfg.Tools.Web
	tavily := onOff(w.Tavily.Enabled) + ", " + keyCount(w.Tavily.APIKey, w.Tavily.APIKeys)
	if w.Tavily.BaseURL != "" {
		tavily += ", " + w.Tavily.BaseURL
	}
	return [][]string{
		row("Web tools (fetch and search)", onOff(w.Enabled)+", fetch limit "+itoa(int(w.FetchLimitBytes))+" bytes"),
		row("Brave search", onOff(w.Brave.Enabled)+", "+keyCount(w.Brave.APIKey, w.Brave.APIKeys)),
		row("Tavily search", tavily),
		row("Perplexity search", onOff(w.Perplexity.Enabled)+", "+keyCount(w.Perplexity.APIKey, w.Perplexity.APIKeys)),
		row("GLM search", onOff(w.GLMSearch.Enabled)+", "+keyCount(w.GLMSearch.APIKey, nil)+", "+w.GLMSearch.BaseURL),
		row("DuckDuckGo search", onOff(w.DuckDuckGo.Enabled)+" (no key)"),
		row("SearXNG search", onOff(w.SearXNG.Enabled)+", "+orValue(w.SearXNG.BaseURL, "no base URL")),
	}
}

func registryRows(cfg *config.Config) [][]string {
	sk := cfg.Tools.Skills
	return [][]string{
		row("Local skills tool", onOff(sk.Local.Enabled)),
		row("Skill registry tool", onOff(sk.Registry.Enabled)),
		row("ClawHub registry", onOff(sk.Registries.ClawHub.Enabled)+", "+orValue(sk.Registries.ClawHub.BaseURL, "no base URL")+
			", auth token "+setOrNot(sk.Registries.ClawHub.AuthToken)),
		row("GitHub (skill install)", "token "+setOrNot(sk.Github.Token)+", proxy "+orValue(redactURL(sk.Github.Proxy), none)),
	}
}

func voiceRows(cfg *config.Config) [][]string {
	active := "none: no enabled STT backend has a key"
	if t := voice.DetectTranscriber(cfg); t != nil {
		active = t.Name()
	}
	rows := make([][]string, 0, 2+len(cfg.Voice.STT))
	rows = append(rows,
		row("Active transcription chain", active),
		row("Echo transcription to the sender", onOff(cfg.Voice.EchoTranscription)),
	)
	for _, s := range cfg.Voice.STT {
		key := setOrNot(s.APIKey)
		if key == "not set" {
			key = "not set (may reuse a provider key for the same host)"
		}
		rows = append(rows, row("STT backend "+s.Provider, onOff(s.Enabled)+", key "+key+", "+orValue(s.BaseURL, "preset URL")+", model "+orValue(s.Model, "preset")))
	}
	return rows
}

func collectExternal(_ context.Context, cfg *config.Config, _ Environment) Section {
	return Section{
		Title: "External services",
		Notes: []string{
			"Programs and network services ClawEh starts or calls on the agents' behalf. Env and header values are " +
				"never shown, only their names. Upgrades: `claw upgrade` downloads releases from api.github.com when " +
				"run by the operator; it has no configuration and no schedule.",
		},
		Tables: []Table{
			{
				Caption: "MCP servers (tools.mcp.servers)",
				Columns: []string{"Server", "Enabled", "Transport", "Command or URL", "Env / headers", "Agents"},
				Rows:    mcpServerRows(cfg),
			},
			{
				Caption: "Skills installed (" + cfg.SkillsPath() + " and the default workspace)",
				Columns: []string{"Skill", "Source", "Description"},
				Rows:    skillRows(cfg),
			},
			pairs("Skill registries", registryRows(cfg)...),
			pairs("Web tools", webToolRows(cfg)...),
			pairs("Voice", voiceRows(cfg)...),
		},
	}
}
