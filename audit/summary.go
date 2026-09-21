// ClawEh
// License: MIT

package audit

import (
	"context"
	"net/url"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// filesSummary states, from the same inputs the Agents section uses, whether
// agents are confined to their workspace and mounts.
func filesSummary(cfg *config.Config, env Environment) string {
	agents := enabledAgents(cfg)
	ids := agentIDs(agents)
	d := cfg.Agents.Defaults
	user := orValue(env.User, unknown)
	n := itoa(len(agents))
	switch {
	case len(agents) == 0:
		return "No enabled agents."
	case !d.RestrictToWorkspace:
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, "agent "+id+" can read and write anything user "+user+" can access")
		}
		return strings.Join(parts, "; ") + " (restrict_to_workspace is off)."
	case d.AllowReadOutsideWorkspace:
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, "agent "+id+" can read anything user "+user+" can read")
		}
		return strings.Join(parts, "; ") + "; writes confined to the workspace write area and mounts " +
			"(allow_read_outside_workspace is on)."
	default:
		return n + " of " + n + " agents confined to their workspace and mounts (" + strings.Join(ids, ", ") +
			"); allow-listed host patterns: " + itoa(len(cfg.Tools.AllowReadPaths)) + " read, " +
			itoa(len(cfg.Tools.AllowWritePaths)) + " write."
	}
}

func shellSummary(cfg *config.Config) string {
	var with []string
	for _, a := range enabledAgents(cfg) {
		if agentHasTool(a, "shell_exec") {
			with = append(with, a.ID)
		}
	}
	ex := cfg.Tools.Exec
	policy := "deny patterns " + onOff(ex.EnableDenyPatterns) + ", remote commands " + onOff(ex.AllowRemote)
	if len(with) == 0 {
		return "No enabled agent has shell_exec (" + policy + ")."
	}
	return "shell_exec allowed for " + strings.Join(with, ", ") + "; " + policy + "."
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func outboundSummary(cfg *config.Config) string {
	var hosts []string
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if config.IsCLIProtocol(p.Protocol) || p.BaseURL == "" {
			continue
		}
		h := hostOf(p.BaseURL)
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	var mcpURLs []string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		if s := cfg.Tools.MCP.Servers[name]; s.Enabled && s.URL != "" {
			mcpURLs = append(mcpURLs, name+" ("+hostOf(s.URL)+")")
		}
	}
	return itoa(len(hosts)) + " API provider endpoints (" + joinOr(hosts, none) + "); web tools " +
		onOff(cfg.Tools.Web.Enabled) + "; MCP servers over HTTP: " + joinOr(mcpURLs, none) + "."
}

func inboundSummary(cfg *config.Config) string {
	var parts []string
	for _, l := range listeners(cfg) {
		if l.Enabled {
			parts = append(parts, l.Name+" on "+l.Addr)
		}
	}
	return strings.Join(parts, "; ") + "."
}

func messagingSummary(cfg *config.Config) string {
	channels := enabledChannels(cfg)
	names := make([]string, 0, len(channels))
	open := 0
	for _, c := range channels {
		names = append(names, c.Name)
		if c.AnyOpen {
			open++
		}
	}
	if len(names) == 0 {
		return "No messaging channel enabled."
	}
	s := "Enabled: " + strings.Join(names, ", ")
	if open > 0 {
		s += "; " + itoa(open) + " accept any sender"
	}
	return s + "."
}

func devicesSummary(ctx context.Context, cfg *config.Config, env Environment) string {
	if !cfg.Channels.Device.Enabled {
		return "Device gateway off; USB monitor " + onOff(cfg.Devices.Enabled) + "."
	}
	paired, _ := deviceRows(ctx, dataDir(cfg, env))
	count := "unknown (store unavailable)"
	if len(paired) > 0 && !strings.HasPrefix(paired[0][0], "unavailable") {
		count = itoa(len(paired))
		if paired[0][0] == none {
			count = "0"
		}
	}
	return "Device gateway on; paired devices: " + count + "; USB monitor " + onOff(cfg.Devices.Enabled) + "."
}

func externalExecSummary(cfg *config.Config) string {
	var clis []string
	for _, p := range cfg.Providers {
		if config.IsCLIProtocol(p.Protocol) && len(modelsFor(cfg, p.Name)) > 0 {
			clis = append(clis, p.Name)
		}
	}
	var stdio []string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		if s := cfg.Tools.MCP.Servers[name]; s.Enabled && s.URL == "" && s.Command != "" {
			stdio = append(stdio, name+" ("+s.Command+")")
		}
	}
	sk := skillRows(cfg)
	skillCount := len(sk)
	if skillCount == 1 && strings.HasPrefix(sk[0][0], "(none") {
		skillCount = 0
	}
	return "CLI providers: " + joinOr(clis, none) + "; MCP stdio commands: " + joinOr(stdio, none) +
		"; skills installed: " + itoa(skillCount) + "."
}

func collectSummary(ctx context.Context, cfg *config.Config, env Environment) Section {
	return Section{
		Title: "Summary",
		Notes: []string{
			"ClawEh runs as user " + orValue(env.User, unknown) + ", group " + orValue(env.Group, unknown) +
				", on " + orValue(env.Hostname, unknown) + ".",
		},
		Tables: []Table{{
			Columns: []string{"Area", "Access"},
			Rows: [][]string{
				row("Files", filesSummary(cfg, env)),
				row("Shell", shellSummary(cfg)),
				row("Outbound network", outboundSummary(cfg)),
				row("Inbound", inboundSummary(cfg)),
				row("Messaging", messagingSummary(cfg)),
				row("Devices", devicesSummary(ctx, cfg, env)),
				row("External execution", externalExecSummary(cfg)),
			},
		}},
	}
}
