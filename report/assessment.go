// ClawEh
// License: MIT

package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
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

// Item names of the per-listener assessment rows, in table order.
const (
	itemWebHTTP     = "WebUI/API HTTP"
	itemWebHTTPS    = "WebUI/API HTTPS"
	itemDeviceHTTP  = "Device HTTP"
	itemDeviceHTTPS = "Device HTTPS"
	itemMCPHost     = "MCP host (local tools)"
	itemLINE        = "LINE webhook"
)

// Status phrases of the per-listener rows, written for a reader who does not
// know what a bind address is.
const (
	enabledLocal   = "Enabled for localhost"
	enabledNetwork = "Enabled for network access"
	disabledStatus = "Disabled."
)

// assessRow is one assessment table row before rendering.
type assessRow struct {
	action       bool
	item, status string
}

// listenerRows is one row per listener, in a fixed order: the WebUI/API over
// HTTP and HTTPS, the device listener over HTTP and HTTPS, the MCP host, and
// the LINE webhook when that channel is on. Only unencrypted network access
// is marked: WebUI/API or Device HTTP open to the network, and
// WebUI/API HTTPS off while its HTTP is. A self-signed certificate is stated,
// never marked: on a LAN there is usually no alternative. Raw bind addresses
// are left to the Network section.
func listenerRows(cfg *config.Config, now time.Time) []assessRow {
	gw := cfg.Gateway
	webAllow := webAllowPhrase(gw.EffectiveAllowedCIDRs())
	var rows []assessRow

	if gw.HTTPOnNetwork() {
		rows = append(rows, assessRow{
			true, itemWebHTTP,
			enabledNetwork + " (unencrypted)" + webAllow + ".",
		})
	} else {
		rows = append(rows, assessRow{false, itemWebHTTP, enabledLocal + "."})
	}

	switch gw.TLS.EffectiveMode() {
	case config.TLSModeAll:
		rows = append(rows, assessRow{false, itemWebHTTPS, enabledNetwork + certificatePhrase(cfg, now) + webAllow + "."})
	case config.TLSModeLocalhost:
		rows = append(rows, assessRow{false, itemWebHTTPS, enabledLocal + certificatePhrase(cfg, now) + "."})
	default:
		if gw.HTTPOnNetwork() {
			rows = append(rows, assessRow{true, itemWebHTTPS, "Disabled, while HTTP is enabled for network access."})
		} else {
			rows = append(rows, assessRow{false, itemWebHTTPS, disabledStatus})
		}
	}

	// The device listener speaks either plain WebSocket or, with
	// channels.device.tls, TLS on the same port; the other row is Disabled.
	dev := cfg.Channels.Device
	devLocal := isLoopback(orValue(dev.Host, "127.0.0.1"))
	devAllow := deviceAllowPhrase(dev.AllowedCIDRs)
	switch {
	case !dev.Enabled || dev.TLS:
		rows = append(rows, assessRow{false, itemDeviceHTTP, disabledStatus})
	case devLocal:
		rows = append(rows, assessRow{false, itemDeviceHTTP, enabledLocal + "."})
	default:
		rows = append(rows, assessRow{true, itemDeviceHTTP, enabledNetwork + " (unencrypted)" + devAllow + "."})
	}
	switch {
	case !dev.Enabled || !dev.TLS:
		rows = append(rows, assessRow{false, itemDeviceHTTPS, disabledStatus})
	case devLocal:
		rows = append(rows, assessRow{false, itemDeviceHTTPS, enabledLocal + certificatePhrase(cfg, now) + "."})
	default:
		rows = append(rows, assessRow{false, itemDeviceHTTPS, enabledNetwork + certificatePhrase(cfg, now) + devAllow + "."})
	}

	if cfg.MCPHostEffectivelyEnabled() {
		// config.ValidateMCPHostListen refuses anything but loopback.
		rows = append(rows, assessRow{false, itemMCPHost, enabledLocal + "."})
	} else {
		rows = append(rows, assessRow{false, itemMCPHost, disabledStatus})
	}

	if line := cfg.Channels.LINE; line.Enabled {
		if isLoopback(line.WebhookHost) {
			rows = append(rows, assessRow{false, itemLINE, enabledLocal + "."})
		} else {
			rows = append(rows, assessRow{false, itemLINE, enabledNetwork + "; every request must carry LINE's signature."})
		}
	}
	return rows
}

// webAllowPhrase is the WebUI/API client allowlist as a trailing clause.
// Empty means no network is allowed, only this machine.
func webAllowPhrase(cidrs []string) string {
	switch {
	case len(cidrs) == 0:
		return "; no networks allowed yet (this machine only)"
	case slices.Contains(cidrs, config.AllowAnyAddress):
		return "; allowed from any address"
	default:
		return "; allowed networks: " + strings.Join(cidrs, ", ")
	}
}

// deviceAllowPhrase is the device listener allowlist as a trailing clause, in
// the shape of webAllowPhrase; empty means any address.
func deviceAllowPhrase(cidrs []string) string {
	if len(cidrs) == 0 || slices.Contains(cidrs, config.AllowAnyAddress) {
		return "; allowed from any address"
	}
	return "; allowed networks: " + strings.Join(cidrs, ", ")
}

// certificatePhrase names the HTTPS certificate as a clause: self-signed, or
// user-provided with its expiry date.
func certificatePhrase(cfg *config.Config, now time.Time) string {
	opts := tlscert.OptionsFromConfig(cfg)
	if opts.Source() == tlscert.SourceSelfSigned {
		return ", self-signed certificate"
	}
	info, err := tlscert.InspectFile(opts)
	if err != nil {
		return ", user-provided certificate (unreadable)"
	}
	if now.IsZero() {
		now = time.Now()
	}
	date := info.NotAfter.Format("2006-01-02")
	if now.After(info.NotAfter) {
		return ", user-provided certificate (expired " + date + ")"
	}
	return ", user-provided certificate (expires " + date + ")"
}

func mark(action bool) string {
	if action {
		return actionMark
	}
	return ""
}

// collectAssessment is the table a reviewer reads first: the items that most
// often need attention, each with the fact and, where action is recommended,
// a mark. It opens with one row per listener (see listenerRows).
func collectAssessment(_ context.Context, cfg *config.Config, env Environment) Section {
	t := Table{Columns: []string{"Action", "Item", "Status"}}
	add := func(action bool, item, status string) {
		t.Rows = append(t.Rows, row(mark(action), item, status))
	}

	for _, r := range listenerRows(cfg, env.Now) {
		add(r.action, r.item, r.status)
	}
	if cfg.Gateway.HTTPSEnabled() {
		if action, item, status := certificateExpiryRow(cfg, env.Now); item != "" {
			add(action, item, status)
		}
	}
	dd := dataDir(cfg, env)

	findings, permErr := perms.Check(dd, env.ConfigPath)
	add(len(findings) > 0, "Data directory permissions", permsStatus(findings, permErr))

	wu := cfg.Channels.WebUI
	add(wu.Enabled && wu.Token == "", "WebUI chat token",
		ifStr(!wu.Enabled, "WebUI channel disabled.",
			"Token "+setOrNot(wu.Token)+" (non-browser clients only; the browser uses its login session)."))

	if dev := cfg.Channels.Device; dev.Enabled {
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

	// The WebUI's one sender is the logged-in operator and every device is
	// authenticated and paired, so allow_from says nothing about exposure
	// there; only channels where anyone can message the bot count.
	var open []string
	for _, c := range enabledChannels(cfg) {
		if c.AnyOpen && c.Name != "webui" && c.Name != "device" {
			open = append(open, c.Name)
		}
	}
	add(len(open) > 0, "Channels accepting any sender",
		ifStr(len(open) == 0, "None: every messaging channel restricts senders.", strings.Join(open, ", ")+" (allow_from contains *)."))

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
	auditPath := audit.Path(dd)
	_, auditErr := os.Stat(auditPath)
	add(auditErr != nil, "Audit log",
		ifStr(auditErr == nil, "Audit log at "+auditPath+" ("+itoa(audit.RetentionDays)+"-day retention).",
			"Audit log not initialised: "+auditPath+" is missing; ClawEh creates it at start."))

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
			"ClawEh tightens them at start — run it, or chmod 700/600.", len(findings), noun, f.Path, f.Mode)
	}
	if errors.Is(err, perms.ErrTruncated) {
		s += " The check was truncated at the entry cap, so files beyond it were not examined."
	}
	return s
}

// certificateExpiryRow is the marked row for a user-supplied certificate
// within 14 days of expiry, or past it. Item is "" otherwise (a self-signed
// certificate renews itself).
func certificateExpiryRow(cfg *config.Config, now time.Time) (action bool, item, status string) {
	opts := tlscert.OptionsFromConfig(cfg)
	if opts.Source() == tlscert.SourceSelfSigned {
		return false, "", ""
	}
	info, err := tlscert.InspectFile(opts)
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
