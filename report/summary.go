// ClawEh
// License: MIT

package report

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

// usedAPIProviders are the API providers at least one enabled model goes
// through; a configured provider with no enabled model sends nothing.
func usedAPIProviders(cfg *config.Config) []*config.Provider {
	var out []*config.Provider
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if config.IsCLIProtocol(p.Protocol) || p.BaseURL == "" {
			continue
		}
		for _, m := range modelsFor(cfg, p.Name) {
			if m.Enabled {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func outboundSummary(cfg *config.Config) string {
	hosts := map[string]bool{}
	for _, p := range usedAPIProviders(cfg) {
		hosts[hostOf(p.BaseURL)] = true
	}
	var mcp []string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		if s := cfg.Tools.MCP.Servers[name]; s.Enabled && s.URL != "" {
			mcp = append(mcp, bullet+name+" ("+hostOf(s.URL)+")")
		}
	}
	if len(mcp) == 0 {
		mcp = []string{bullet + none}
	}
	lines := make([]string, 0, 2+len(mcp))
	lines = append(lines, itoa(len(hosts))+" API provider endpoints (details below)", "MCP servers:")
	return strings.Join(append(lines, mcp...), "\n")
}

func inboundSummary(cfg *config.Config) string {
	var lines []string
	for _, l := range listeners(cfg) {
		if l.Enabled {
			lines = append(lines, bullet+l.Name+" on "+l.Addr)
		}
	}
	return joinLines(lines, none)
}

func messagingSummary(cfg *config.Config) string {
	channels := enabledChannels(cfg)
	names := make([]string, 0, len(channels))
	for _, c := range channels {
		names = append(names, displayChannel(c.Name))
	}
	return joinOr(names, "No messaging channel enabled")
}

// displayChannel renders a channel id for prose: "webui" reads as WebUI, the
// rest with a capital first letter.
func displayChannel(id string) string {
	switch strings.ToLower(id) {
	case "webui":
		return "WebUI"
	case "":
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

func devicesSummary(ctx context.Context, cfg *config.Config, env Environment) string {
	if !cfg.Channels.Device.Enabled {
		return "Device gateway off"
	}
	paired, _ := deviceRows(ctx, dataDir(cfg, env))
	count := "paired devices unknown (store unavailable)"
	switch {
	case len(paired) > 0 && strings.HasPrefix(paired[0][0], "unavailable"):
	case len(paired) == 0 || paired[0][0] == none:
		count = "0 devices paired"
	case len(paired) == 1:
		count = "1 device paired"
	default:
		count = itoa(len(paired)) + " devices paired"
	}
	return "Device gateway on\n" + count
}

func externalExecSummary(cfg *config.Config) string {
	var clis []string
	for _, p := range cfg.Providers {
		if !config.IsCLIProtocol(p.Protocol) {
			continue
		}
		for _, m := range modelsFor(cfg, p.Name) {
			if m.Enabled {
				clis = append(clis, p.Name)
				break
			}
		}
	}
	var stdio []string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		if s := cfg.Tools.MCP.Servers[name]; s.Enabled && s.URL == "" && s.Command != "" {
			stdio = append(stdio, name+" ("+s.Command+")")
		}
	}
	return "CLI providers: " + joinOr(clis, none) + "\nMCP stdio commands: " + joinOr(stdio, none)
}

// skillsSummary names the installed skills; every agent without a skills
// filter can use all of them.
func skillsSummary(cfg *config.Config) string {
	var names []string
	for _, r := range skillRows(cfg) {
		if !strings.HasPrefix(r[0], "(none") {
			names = append(names, r[0])
		}
	}
	return joinOr(names, "(none installed)")
}

func collectSummary(ctx context.Context, cfg *config.Config, env Environment) Section {
	return Section{
		Title: "Summary",
		Notes: []string{
			"ClawEh runs as user " + orValue(env.User, unknown) + ", group " + orValue(env.Group, unknown) +
				" on " + orValue(env.Hostname, unknown) + ".",
		},
		Tables: []Table{{
			Columns: []string{"Area", "Access"},
			Rows: [][]string{
				row("Files", filesSummary(cfg, env)),
				row("Shell", shellSummary(cfg)),
				row("Outbound network", outboundSummary(cfg)),
				row("Inbound network", inboundSummary(cfg)),
				row("Messaging", messagingSummary(cfg)),
				row("Devices", devicesSummary(ctx, cfg, env)),
				row("External execution", externalExecSummary(cfg)),
				row("Skills", skillsSummary(cfg)),
			},
		}},
	}
}
