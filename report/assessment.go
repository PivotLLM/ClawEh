// ClawEh
// License: MIT

package report

import (
	"context"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// actionMark is the first column of the assessment table: "*" where action
// is recommended, blank otherwise, so a reader scans a column rather than
// words.
const actionMark = "*"

// exposedListeners names the enabled listeners bound to something other than
// a loopback address: traffic to them can leave or enter the host.
func exposedListeners(cfg *config.Config) []string {
	var out []string
	for _, l := range listeners(cfg) {
		if l.Enabled && !loopbackAddr(l.Addr) {
			out = append(out, l.Name+" on "+l.Addr)
		}
	}
	return out
}

// loopbackAddr reports whether a bind address as bindAddr renders it stays on
// this host.
func loopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func mark(action bool) string {
	if action {
		return actionMark
	}
	return ""
}

// collectAssessment is the table a reviewer reads first: the items that most
// often need attention, each with the fact and, where action is recommended,
// a mark. HTTPS and operator authentication are not implemented yet; they are
// marked only when a listener is reachable from other hosts.
func collectAssessment(_ context.Context, cfg *config.Config, env Environment) Section {
	exposed := exposedListeners(cfg)
	reach := "All listeners are loopback only, so traffic stays on this host."
	if len(exposed) > 0 {
		reach = "Reachable from other hosts: " + strings.Join(exposed, "; ") + "."
	}
	t := Table{Columns: []string{"Action", "Item", "Status"}}
	add := func(action bool, item, status string) {
		t.Rows = append(t.Rows, row(mark(action), item, status))
	}

	add(len(exposed) > 0, "Transport encryption (HTTPS)",
		"Not implemented yet: ClawEh does not terminate TLS. "+reach+
			ifStr(len(exposed) > 0, " Put a TLS reverse proxy in front until HTTPS is supported.", ""))
	add(len(exposed) > 0, "Operator authentication (WebUI and API)",
		"Not implemented yet: access is limited by gateway.allowed_cidrs only. "+reach)

	gw := cfg.Gateway
	gwExposed := !loopbackAddr(bindAddr(gw.Host, gw.Port))
	anyAddr := false
	for _, c := range gw.AllowedCIDRs {
		if strings.TrimSpace(c) == "*" {
			anyAddr = true
		}
	}
	add(gwExposed && anyAddr, "WebUI and API reachability",
		"Gateway on "+bindAddr(gw.Host, gw.Port)+"; client allowlist: "+gatewayAllow(gw.AllowedCIDRs)+".")

	wu := cfg.Channels.WebUI
	add(wu.Enabled && (wu.Token == "" || wu.AllowTokenQuery), "WebUI chat token",
		ifStr(!wu.Enabled, "WebUI channel disabled.",
			"Token "+setOrNot(wu.Token)+"; accepted in the query string: "+yesNo(wu.AllowTokenQuery)+
				ifStr(wu.AllowTokenQuery, " (tokens in URLs end up in logs and history).", ".")))

	dev := cfg.Channels.Device
	if !dev.Enabled {
		add(false, "Device gateway", "Disabled.")
	} else {
		devAddr := bindAddr(dev.Host, dev.Port)
		devAny := len(dev.AllowedCIDRs) == 0
		for _, c := range dev.AllowedCIDRs {
			if strings.TrimSpace(c) == "*" {
				devAny = true
			}
		}
		add(!loopbackAddr(devAddr) && devAny, "Device gateway",
			"On "+devAddr+"; client allowlist: "+ifStr(devAny, "any address (device token required)", strings.Join(dev.AllowedCIDRs, ", "))+
				"; word token "+setOrNot(dev.WordToken)+".")
	}

	d := cfg.Agents.Defaults
	agents := enabledAgents(cfg)
	ids := strings.Join(agentIDs(agents), ", ")
	user := orValue(env.User, unknown)
	switch {
	case !d.RestrictToWorkspace:
		add(true, "File confinement", "restrict_to_workspace is off: "+ids+" can read and write anything user "+user+" can access.")
	case d.AllowReadOutsideWorkspace:
		add(true, "File confinement", "allow_read_outside_workspace is on: "+ids+" can read anything user "+user+" can read; writes stay in the workspace and mounts.")
	default:
		add(false, "File confinement", "Agents are confined to their workspace, mounts and the allow-listed patterns ("+ids+").")
	}

	var shell []string
	for _, a := range agents {
		if agentHasTool(a, "shell_exec") {
			shell = append(shell, a.ID)
		}
	}
	ex := cfg.Tools.Exec
	add(len(shell) > 0 && (!ex.EnableDenyPatterns || ex.AllowRemote), "Shell access",
		ifStr(len(shell) == 0, "No enabled agent has shell_exec.",
			"shell_exec: "+strings.Join(shell, ", ")+"; deny patterns "+onOff(ex.EnableDenyPatterns)+", remote commands "+onOff(ex.AllowRemote)+"."))

	var open []string
	for _, c := range enabledChannels(cfg) {
		if c.AnyOpen {
			open = append(open, c.Name)
		}
	}
	add(len(open) > 0, "Channels accepting any sender",
		ifStr(len(open) == 0, "None: every enabled channel restricts senders.", strings.Join(open, ", ")+" (allow_from contains *)."))

	lg := cfg.Logging
	var flags []string
	if lg.LogMessageContent {
		flags = append(flags, "log_message_content")
	}
	if lg.DumpAll {
		flags = append(flags, "dump_all")
	}
	if lg.DumpRefusals {
		flags = append(flags, "dump_refusals")
	}
	add(len(flags) > 0, "Message content in logs",
		ifStr(len(flags) == 0, "Off: logs carry metadata only.", "On: "+strings.Join(flags, ", ")+" write message content to disk."))

	var stdio []string
	for _, name := range sortedKeys(cfg.Tools.MCP.Servers) {
		if s := cfg.Tools.MCP.Servers[name]; s.Enabled && s.URL == "" && s.Command != "" {
			stdio = append(stdio, name+" ("+s.Command+")")
		}
	}
	add(false, "MCP servers running local programs", joinOr(stdio, "None."))

	add(false, "Sub-agent spawning",
		"Gate "+onOff(cfg.Tools.Subagent.Enabled)+", max depth "+itoa(d.GetMaxSubagentDepth())+".")

	return Section{
		Title:  "Security assessment",
		Notes:  []string{"A " + actionMark + " in the first column means action is recommended. Details for every item are in the sections below."},
		Tables: []Table{t},
	}
}

func ifStr(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
