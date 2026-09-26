// ClawEh
// License: MIT

package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

// certExpiryWarn is how close to expiry a user-supplied certificate earns an
// action mark; the same horizon as the first tlscert expiry alert.
const certExpiryWarn = 14 * 24 * time.Hour

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
// this host. The gateway row lists two listeners ("127.0.0.1:18790 (HTTP),
// 0.0.0.0:18443 (HTTPS)"); it is loopback only if every one of them is.
func loopbackAddr(addr string) bool {
	if parts := strings.Split(addr, ", "); len(parts) > 1 {
		for _, p := range parts {
			if !loopbackAddr(p) {
				return false
			}
		}
		return true
	}
	host := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(addr, " (HTTP)"), " (HTTPS)"))
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
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
// a mark. The gateway is HTTPS whenever it is bound off-box, so the HTTPS row
// is marked only when a plain-HTTP listener (the device gateway, the LINE
// webhook) is reachable from other hosts; operator authentication is marked
// whenever no usable admin account exists, since without one the WebUI is
// locked.
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

	gw := cfg.Gateway
	var plain []string // exposed listeners that speak plain HTTP
	for _, e := range exposed {
		if !gw.HTTPSEnabled() || !strings.HasPrefix(e, "Gateway ") {
			plain = append(plain, e)
		}
	}
	httpsStatus := "The gateway is loopback only: plain HTTP on this host, no HTTPS listener (set gateway.host to a LAN address or 0.0.0.0 for HTTPS on gateway.tls_port)."
	if gw.HTTPSEnabled() {
		httpsStatus = "HTTPS on " + bindAddr(gw.Host, gw.EffectiveTLSPort()) + " with a " + string(tlscert.OptionsFromConfig(cfg).Source()) +
			" certificate (`claw tls` shows it); plain HTTP is served on loopback only."
	}
	add(len(plain) > 0, "Transport encryption (HTTPS)",
		httpsStatus+" "+reach+
			ifStr(len(plain) > 0, " Plain-HTTP listeners reachable from other hosts: "+strings.Join(plain, "; ")+"; put them behind a TLS reverse proxy.", ""))
	if gw.HTTPSEnabled() {
		if action, item, status := certificateRow(cfg, env.Now); item != "" {
			add(action, item, status)
		}
	}
	dd := dataDir(cfg, env)
	credPath := admin.Path(dd)
	_, credErr := admin.Load(credPath)
	add(credErr != nil, "Operator authentication (WebUI and API)", adminAccountStatus(credErr, credPath)+" "+reach)

	findings, permErr := perms.Check(dd, env.ConfigPath)
	add(len(findings) > 0, "Data directory permissions", permsStatus(findings, permErr))

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
	add(wu.Enabled && wu.Token == "", "WebUI chat token",
		ifStr(!wu.Enabled, "WebUI channel disabled.",
			"Token "+setOrNot(wu.Token)+" (non-browser clients only; the browser uses its login session)."))

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
		add(dev.AutoApprove, "Device auto-approve",
			ifStr(dev.AutoApprove, "New devices are paired without approval; any client with the shared token joins.", "Devices need approval."))
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

	// Awareness only: the file tools honour restrict_to_workspace, the shell
	// does not, so a confined agent with shell_exec is confined in name only.
	if d.RestrictToWorkspace {
		for _, id := range shell {
			add(false, "File confinement vs shell ("+id+")",
				"File tools are confined to the workspace for "+id+", but shell_exec is available and is not confined.")
		}
	}

	// Awareness only, not an action mark: the operator turned it on, and the
	// row says what that means. One per CLI provider so each is named.
	for i := range cfg.Providers {
		if p := &cfg.Providers[i]; p.BypassRestrictions && config.IsCLIProtocol(p.Protocol) {
			add(false, "Bypass CLI restrictions ("+p.Name+")",
				"Bypass CLI restrictions is on for "+p.Name+": it can run commands and modify files anywhere the service user can, "+
					"outside ClawEh's workspace and shell controls.")
		}
	}

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

	// The audit store is opened when the gateway boots, and the report is
	// served by a running gateway, so a missing file means it is not recording.
	auditPath := filepath.Join(dd, audit.FileName)
	_, auditErr := os.Stat(auditPath)
	add(auditErr != nil, "Audit log",
		ifStr(auditErr == nil, "Audit log at "+auditPath+" ("+itoa(audit.RetentionDays)+"-day retention).",
			"Audit log not initialised: "+auditPath+" is missing; the gateway creates it at start."))

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

// adminAccountStatus describes the credentials file the gateway authenticates
// against, from the error admin.Load returned (nil when it is usable).
func adminAccountStatus(err error, path string) string {
	var perr *admin.PermissionError
	switch {
	case err == nil:
		return "Admin account configured (" + path + "); a login is required for the WebUI and API."
	case errors.Is(err, admin.ErrNotConfigured):
		return "No admin account: the WebUI and API refuse every request until `claw admin` is run on the server."
	case errors.As(err, &perr):
		return "Credentials file ignored because other accounts can read it; run: " + perr.Fix() + "."
	default:
		return "Credentials file unusable: " + err.Error() + "."
	}
}

// permsStatus describes what perms.Check found under CLAW_HOME. A truncated
// walk still reports the findings it collected, and says it stopped early.
func permsStatus(findings []perms.Finding, err error) string {
	if err != nil && !errors.Is(err, perms.ErrTruncated) {
		return "Permission check failed: " + err.Error() + "."
	}
	var s string
	if len(findings) == 0 {
		s = "CLAW_HOME and its secrets are owner-only."
	} else {
		f := findings[0]
		noun := "files under CLAW_HOME are"
		if len(findings) == 1 {
			noun = "file under CLAW_HOME is"
		}
		s = fmt.Sprintf("%d %s readable by other users (first: %s %04o); "+
			"the gateway tightens them at start — run it, or chmod 700/600.", len(findings), noun, f.Path, f.Mode)
	}
	if errors.Is(err, perms.ErrTruncated) {
		s += " The check was truncated at the entry cap, so files beyond it were not examined."
	}
	return s
}

// certificateRow is the assessment row for the HTTPS certificate: an
// awareness row for a self-signed one (browsers warn until its fingerprint is
// verified), an action row for a user-supplied one about to expire. Item is
// "" when there is nothing to say.
func certificateRow(cfg *config.Config, now time.Time) (action bool, item, status string) {
	opts := tlscert.OptionsFromConfig(cfg)
	info, err := tlscert.InspectFile(opts)
	if opts.Source() == tlscert.SourceSelfSigned {
		id := "SHA-256 " + info.Fingerprint
		if err != nil {
			id = "not yet generated: " + err.Error()
		}
		return false, "Self-signed certificate",
			"HTTPS uses a self-signed certificate (" + id + "); browsers warn once — verify the fingerprint with `claw tls`, or install your own with gateway.tls."
	}
	if err != nil {
		return false, "", ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	left := info.NotAfter.Sub(now)
	if left > certExpiryWarn {
		return false, "", ""
	}
	date := info.NotAfter.Format("2006-01-02")
	daysLeft := int(left.Hours() / 24)
	if left < 0 {
		return true, "TLS certificate expiry", fmt.Sprintf("TLS certificate expired on %s (%d days ago).", date, -daysLeft)
	}
	return true, "TLS certificate expiry", fmt.Sprintf("TLS certificate expires on %s (%d days).", date, daysLeft)
}

func ifStr(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
