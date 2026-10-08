// ClawEh
// License: MIT

package report

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// TestAssessment_BypassCLIRestrictions: one unmarked awareness row per CLI
// provider with bypass_restrictions on, none for the others.
func TestAssessment_BypassCLIRestrictions(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Allow CLI to bypass restrictions (Claude CLI)")
	if r[0] != "" {
		t.Errorf("mark = %q, want blank: awareness, not an action", r[0])
	}
	contains(t, r[2], "Allow CLI to bypass restrictions is on for Claude CLI", "row status")
	contains(t, r[2], "outside ClawEh's workspace and shell controls", "row status")
	// A CLI with bypass off gets its own awareness row: its tool calls depend
	// on the CLI's permission settings.
	off := assessmentRow(t, s, "CLI bypass not allowed (Codex CLI)")
	if off[0] != "" {
		t.Errorf("mark = %q, want blank: awareness, not an action", off[0])
	}
	contains(t, off[2], "depends on the CLI's own permission settings", "row status")

	cfg.Providers[1].BypassRestrictions = false
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if row[1] == "Allow CLI to bypass restrictions (Claude CLI)" {
			t.Errorf("on-row present with every bypass off: %v", row)
		}
	}
	assessmentRow(t, s, "CLI bypass not allowed (Claude CLI)")
}

// TestAssessment_FusionNoService: an agent with Fusion on and nothing in
// mcp_tools has no Fusion tools; that is marked for action. Listing a service
// clears it, as does Fusion off.
func TestAssessment_FusionNoService(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.List[1].Fusion = true // bob: no mcp_tools
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Fusion on with no service (bob)")
	if r[0] == "" {
		t.Error("expected an action mark")
	}
	contains(t, r[2], "no Fusion tools", "row status")
	for _, row := range s.Tables[0].Rows {
		if strings.HasPrefix(row[1], "Fusion on with no service (alice)") {
			t.Errorf("alice lists services (%v) and Fusion is off; row = %v", cfg.Agents.List[0].MCPTools, row)
		}
	}

	cfg.Agents.List[1].MCPTools = []string{"wxca"}
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if strings.HasPrefix(row[1], "Fusion on with no service") {
			t.Errorf("row present with a service listed: %v", row)
		}
	}
}

// assessmentRow finds the row for an item; the first column is the action mark.
func assessmentRow(t *testing.T, s Section, item string) []string {
	t.Helper()
	for _, r := range s.Tables[0].Rows {
		if r[1] == item {
			return r
		}
	}
	t.Fatalf("assessment row %q not found", item)
	return nil
}

// TestAssessment_ListenerRows: one row per listener, in order, in plain
// words. The fixture is the default install: HTTP on localhost, HTTPS on the
// network with a self-signed certificate, the device listener and the LINE
// webhook on the network. Only the unencrypted device listener earns a mark.
func TestAssessment_ListenerRows(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	want := [][]string{
		{"", "WebUI/API HTTP", "Enabled for localhost."},
		{"", "WebUI/API HTTPS", "Enabled for network access, self-signed certificate; allowed networks: 192.168.1.0/24."},
		{"*", "Device HTTP", "Enabled for network access (unencrypted); allowed from any address."},
		{"", "Device HTTPS", "Disabled."},
		{"", "MCP host (local tools)", "Enabled for localhost."},
		{"", "LINE webhook", "Enabled for network access; every request must carry LINE's signature."},
	}
	rows := s.Tables[0].Rows
	if len(rows) < len(want) {
		t.Fatalf("only %d rows", len(rows))
	}
	for i, w := range want {
		if strings.Join(rows[i], " | ") != strings.Join(w, " | ") {
			t.Errorf("row %d = %q\nwant   %q", i, rows[i], w)
		}
	}
	// No listener row names the process "gateway", mentions a proxy, the
	// tls command or a wildcard address: those are for the Network section.
	for _, r := range rows[:len(want)] {
		for _, banned := range []string{"gateway ", "proxy", "claw tls", "0.0.0.0", "18443"} {
			if strings.Contains(r[2], banned) {
				t.Errorf("row %q mentions %q: %q", r[1], banned, r[2])
			}
		}
	}

	// Plain HTTP on the network is marked, HTTPS still on is not.
	cfg.Gateway.Host = "0.0.0.0"
	s = collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "WebUI/API HTTP")
	if r[0] != "*" || r[2] != "Enabled for network access (unencrypted); allowed networks: 192.168.1.0/24." {
		t.Errorf("network HTTP row = %q", r)
	}
	if r = assessmentRow(t, s, "WebUI/API HTTPS"); r[0] != "" {
		t.Errorf("HTTPS row marked while on: %q", r)
	}

	// HTTPS off while HTTP is on the network: both marked.
	cfg.Gateway.TLS.Mode = config.TLSModeOff
	s = collectAssessment(t.Context(), cfg, env)
	if r = assessmentRow(t, s, "WebUI/API HTTPS"); r[0] != "*" || !strings.HasPrefix(r[2], "Disabled") {
		t.Errorf("HTTPS off with network HTTP = %q, want marked Disabled", r)
	}

	// HTTPS off, HTTP on localhost: nothing reachable, nothing marked.
	cfg.Gateway.Host = "127.0.0.1"
	s = collectAssessment(t.Context(), cfg, env)
	if r = assessmentRow(t, s, "WebUI/API HTTPS"); r[0] != "" || r[2] != "Disabled." {
		t.Errorf("HTTPS off, HTTP local = %q", r)
	}

	// HTTPS on localhost only; any-address allowlists; device on loopback;
	// MCP host and LINE off.
	cfg.Gateway.TLS.Mode = config.TLSModeLocalhost
	cfg.Gateway.AllowedCIDRs = []string{"*"}
	cfg.Channels.Device.Host = "127.0.0.1"
	cfg.MCPHost.Enabled, cfg.MCPHost.AutoEnable = false, false
	cfg.Channels.LINE.Enabled = false
	s = collectAssessment(t.Context(), cfg, env)
	for item, status := range map[string]string{
		"WebUI/API HTTPS":        "Enabled for localhost, self-signed certificate.",
		"Device HTTP":            "Enabled for localhost.",
		"MCP host (local tools)": "Disabled.",
	} {
		if r = assessmentRow(t, s, item); r[0] != "" || r[2] != status {
			t.Errorf("%s = %q, want %q", item, r, status)
		}
	}
	for _, row := range s.Tables[0].Rows {
		if row[1] == "LINE webhook" {
			t.Errorf("LINE row with the channel off: %q", row)
		}
	}
	cfg.Gateway.TLS.Mode = config.TLSModeAll
	s = collectAssessment(t.Context(), cfg, env)
	if r = assessmentRow(t, s, "WebUI/API HTTPS"); !strings.HasSuffix(r[2], "; allowed from any address.") {
		t.Errorf("any-address HTTPS = %q", r)
	}
	cfg.Channels.Device.Enabled = false
	s = collectAssessment(t.Context(), cfg, env)
	if r = assessmentRow(t, s, "Device HTTP"); r[2] != "Disabled." {
		t.Errorf("device off = %q", r)
	}
}

// TestAssessment_Marks: each remaining row marks exactly the condition it
// describes.
func TestAssessment_Marks(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Logging.DumpRefusals = false
	s := collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "File confinement"); r[0] != "" || !strings.Contains(r[2], "confined") {
		t.Errorf("confined file row = %v", r)
	}
	if r := assessmentRow(t, s, "Shell access"); r[0] != "" {
		t.Errorf("shell row with deny patterns on should not be marked: %v", r)
	}
	if r := assessmentRow(t, s, "Channels accepting any sender"); r[0] != "*" || !strings.Contains(r[2], "telegram-bob") {
		t.Errorf("any-sender row = %v", r)
	}
	if r := assessmentRow(t, s, "Message content in logs"); r[0] != "" {
		t.Errorf("logging row = %v", r)
	}
	if r := assessmentRow(t, s, "MCP servers running local programs"); r[0] != "" || !strings.Contains(r[2], "local (npx)") {
		t.Errorf("mcp stdio row = %v", r)
	}

	cfg.Agents.Defaults.RestrictToWorkspace = false
	cfg.Tools.Exec.EnableDenyPatterns = false
	cfg.Logging.LogMessageContent = true
	s = collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "File confinement"); r[0] != "*" || !strings.Contains(r[2], "anything user admin can access") {
		t.Errorf("unconfined file row = %v", r)
	}
	if r := assessmentRow(t, s, "Shell access"); r[0] != "*" || !strings.Contains(r[2], "deny patterns off") {
		t.Errorf("shell row = %v", r)
	}

	if r := assessmentRow(t, s, "Message content in logs"); r[0] != "*" || !strings.Contains(r[2], "log_message_content") {
		t.Errorf("logging row = %v", r)
	}
}

// TestAssessment_DataDirPermissions: owner-only CLAW_HOME is unmarked; a
// secret-bearing file others can read marks the row, counts, and names the
// first offender with its mode.
func TestAssessment_DataDirPermissions(t *testing.T) {
	cfg, env := fixtureConfig(t)
	// t.TempDir and the fixture's folders follow the umask; the gateway would
	// have made every directory under CLAW_HOME, and CLAW_HOME itself, 0700.
	if err := filepath.WalkDir(env.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chmod(p, 0o700)
	}); err != nil {
		t.Fatal(err)
	}
	s := collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "Data directory permissions"); r[0] != "" || r[2] != "CLAW_HOME and its secrets are owner-only." {
		t.Errorf("tight permissions row = %v", r)
	}

	loosePath := filepath.Join(env.DataDir, "internal", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(loosePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loosePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Data directory permissions")
	if r[0] != "*" {
		t.Errorf("loose permissions mark = %q, want *", r[0])
	}
	contains(t, r[2], "1 file under CLAW_HOME is readable by other users (first: "+loosePath+" 0644)", "loose status")
	contains(t, r[2], "ClawEh tightens them at start", "loose fix")

	// A loose data directory itself counts too and comes first.
	if err := os.Chmod(env.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "Data directory permissions")
	contains(t, r[2], "2 files under CLAW_HOME are readable by other users (first: "+env.DataDir+" 0755)", "loose dir status")
}

// TestPermsStatus_TruncatedAndError: a truncated walk keeps its findings and
// says it stopped; any other error is reported as such.
func TestPermsStatus_TruncatedAndError(t *testing.T) {
	got := permsStatus(nil, perms.ErrTruncated)
	contains(t, got, "CLAW_HOME and its secrets are owner-only.", "truncated, no findings")
	contains(t, got, "The check was truncated at the entry cap", "truncated note")

	got = permsStatus([]perms.Finding{{Path: "/x/tokens.json", Mode: 0o644, Want: 0o600}}, perms.ErrTruncated)
	contains(t, got, "1 file under CLAW_HOME is readable by other users (first: /x/tokens.json 0644)", "truncated with findings")
	contains(t, got, "truncated at the entry cap", "truncated note with findings")

	got = permsStatus(nil, errors.New("boom"))
	if got != "Permission check failed: boom." {
		t.Errorf("error status = %q", got)
	}
}

// TestAssessment_DeviceAutoApprove: present only while the device channel is
// enabled; marked only when pairing skips approval.
func TestAssessment_DeviceAutoApprove(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "Device auto-approve"); r[0] != "" || r[2] != "Devices need approval." {
		t.Errorf("approval-required row = %v", r)
	}

	cfg.Channels.Device.AutoApprove = true
	s = collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Device auto-approve")
	if r[0] != "*" {
		t.Errorf("auto-approve mark = %q, want *", r[0])
	}
	if r[2] != "New devices are paired without approval; any client with the shared token joins." {
		t.Errorf("auto-approve status = %q", r[2])
	}

	cfg.Channels.Device.Enabled = false
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if row[1] == "Device auto-approve" {
			t.Errorf("row present with the device channel disabled: %v", row)
		}
	}
}

// TestAssessment_FileConfinementVsShell: one unmarked row per confined agent
// that can call shell_exec; none for an agent that denies it, none when
// restrict_to_workspace is off (the File confinement row covers that).
func TestAssessment_FileConfinementVsShell(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.List[0].Tools = []string{"*", "shell_exec"}
	s := collectAssessment(t.Context(), cfg, env)
	for _, id := range []string{"alice", "bob"} {
		r := assessmentRow(t, s, "File confinement vs shell ("+id+")")
		if r[0] != "" {
			t.Errorf("%s mark = %q, want blank: awareness, not an action", id, r[0])
		}
		if r[2] != "File tools are confined to the workspace for "+id+", but shell_exec is available and is not confined." {
			t.Errorf("%s status = %q", id, r[2])
		}
	}

	cfg.Agents.List[1].DenyTools = []string{"shell_exec"}
	s = collectAssessment(t.Context(), cfg, env)
	assessmentRow(t, s, "File confinement vs shell (alice)")
	for _, row := range s.Tables[0].Rows {
		if row[1] == "File confinement vs shell (bob)" {
			t.Errorf("row present for an agent whose deny_tools removes shell_exec: %v", row)
		}
	}

	cfg.Agents.Defaults.RestrictToWorkspace = false
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if strings.HasPrefix(row[1], "File confinement vs shell") {
			t.Errorf("row present with restrict_to_workspace off: %v", row)
		}
	}
}

// TestAssessment_Certificate: the HTTPS row names the certificate — a
// self-signed one never earns a mark or a warning; a user-supplied one is
// dated, and earns a marked expiry row only within 14 days of expiry;
// nothing when HTTPS is off.
func TestAssessment_Certificate(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "WebUI/API HTTPS")
	if r[0] != "" || !strings.Contains(r[2], ", self-signed certificate;") {
		t.Errorf("self-signed HTTPS row = %q", r)
	}
	for _, row := range s.Tables[0].Rows {
		if row[1] == "TLS certificate expiry" {
			t.Errorf("expiry row for a self-signed certificate: %v", row)
		}
	}

	// User-supplied, far from expiry: dated in the HTTPS row, no expiry row.
	userCert := filepath.Join(env.DataDir, "user.crt")
	writeTestCert(t, userCert, env.Now.Add(100*24*time.Hour))
	cfg.Gateway.TLS = config.TLSConfig{CertFile: userCert, KeyFile: filepath.Join(env.DataDir, "user.key")}
	s = collectAssessment(t.Context(), cfg, env)
	if r = assessmentRow(t, s, "WebUI/API HTTPS"); r[2] != "Enabled for network access, user-provided certificate (expires 2026-12-30); allowed networks: 192.168.1.0/24." {
		t.Errorf("user certificate HTTPS row = %q", r[2])
	}
	for _, row := range s.Tables[0].Rows {
		if row[1] == "TLS certificate expiry" {
			t.Errorf("expiry row for a valid user certificate: %v", row)
		}
	}

	// Within 14 days: marked, dated, counted.
	writeTestCert(t, userCert, env.Now.Add(10*24*time.Hour))
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "TLS certificate expiry")
	if r[0] != "*" {
		t.Errorf("expiry mark = %q, want *", r[0])
	}
	if r[2] != "TLS certificate expires on 2026-10-01 (10 days)." {
		t.Errorf("expiry status = %q", r[2])
	}

	// Already expired: still marked, says so, and the HTTPS row too.
	writeTestCert(t, userCert, env.Now.Add(-2*24*time.Hour))
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "TLS certificate expiry")
	if r[0] != "*" || r[2] != "TLS certificate expired on 2026-09-19 (2 days ago)." {
		t.Errorf("expired row = %v", r)
	}
	contains(t, assessmentRow(t, s, "WebUI/API HTTPS")[2], "user-provided certificate (expired 2026-09-19)", "expired HTTPS row")

	// HTTPS off: no certificate rows.
	cfg.Gateway.TLS.Mode = config.TLSModeOff
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if row[1] == "TLS certificate expiry" {
			t.Errorf("certificate row with HTTPS off: %v", row)
		}
	}
}

// TestAssessment_AuditLog: missing on a running gateway is marked; present is
// named with its retention.
func TestAssessment_AuditLog(t *testing.T) {
	cfg, env := fixtureConfig(t)
	auditPath := filepath.Join(env.DataDir, "internal", "audit.db")
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Audit log")
	if r[0] != "*" {
		t.Errorf("missing audit mark = %q, want *", r[0])
	}
	if r[2] != "Audit log not initialised: "+auditPath+" is missing; ClawEh creates it at start." {
		t.Errorf("missing audit status = %q", r[2])
	}

	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "Audit log")
	if r[0] != "" || r[2] != "Audit log at "+auditPath+" (90-day retention)." {
		t.Errorf("present audit row = %v", r)
	}
}

// TestAssessment_ShellDelegation: the Shell access row names the agents whose
// subagents.allow_agents reaches an agent with shell_exec, and points to the
// CLI bypass setting, which gives a CLI model its own shell, only when a CLI
// provider has it on.
func TestAssessment_ShellDelegation(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.List[0].Tools = []string{"file_read"} // Alice has no shell_exec
	r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if !strings.Contains(r[2], "Allow shell commands: bob;") || strings.Contains(r[2], "allow_agents") {
		t.Errorf("no delegation: shell row = %v", r)
	}
	if !strings.Contains(r[2], "Allow CLI to bypass restrictions") {
		t.Errorf("bypass on: shell row does not name the CLI bypass setting: %v", r)
	}

	// Ids match as the runtime matches them (config.NormalizeAgentID).
	for _, allow := range [][]string{{"bob"}, {"*"}, {" Bob "}} {
		cfg.Agents.List[0].Subagents = &config.SubagentsConfig{AllowAgents: allow}
		r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
		if !strings.Contains(r[2], "also via allow_agents: alice;") {
			t.Errorf("allow_agents %v: shell row = %v", allow, r)
		}
	}

	for i := range cfg.Providers {
		cfg.Providers[i].BypassRestrictions = false
	}
	r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if strings.Contains(r[2], "bypass") {
		t.Errorf("bypass off: shell row mentions the CLI bypass setting: %v", r)
	}
}

// TestAssessment_ShellExplicitOnly: the Shell access row lists only the agents
// whose tools name shell_exec ("Allow shell commands"); a "*" list does not
// count, and deny_tools removes an agent.
func TestAssessment_ShellExplicitOnly(t *testing.T) {
	cfg, env := fixtureConfig(t)
	// alice has no tools key (the "*" default), bob names shell_exec.
	r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if !strings.Contains(r[2], "Allow shell commands: bob;") {
		t.Errorf("default: shell row = %v", r)
	}

	cfg.Agents.List[0].Tools = []string{"*"}
	r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if !strings.Contains(r[2], "Allow shell commands: bob;") {
		t.Errorf(`alice with "*": shell row = %v`, r)
	}

	cfg.Agents.List[0].Tools = []string{"*", "shell_exec"}
	r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if !strings.Contains(r[2], "Allow shell commands: alice, bob;") {
		t.Errorf("alice names shell_exec: shell row = %v", r)
	}

	cfg.Agents.List[0].DenyTools = []string{"shell_exec"}
	cfg.Agents.List[1].DenyTools = []string{"shell_exec"}
	r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access")
	if !strings.HasPrefix(r[2], "No enabled agent is allowed shell commands.") {
		t.Errorf("both denied: shell row = %v", r)
	}
}

// TestAssessment_ShellOverrideHasNoEffect: a tools.tool_overrides.shell_exec
// entry, whatever its value, is shown as having no effect; without one there
// is no such row.
func TestAssessment_ShellOverrideHasNoEffect(t *testing.T) {
	cfg, env := fixtureConfig(t)
	for _, row := range collectAssessment(t.Context(), cfg, env).Tables[0].Rows {
		if row[1] == "Install-wide shell setting" {
			t.Errorf("row present without the override: %v", row)
		}
	}
	for _, v := range []bool{true, false} {
		cfg.Tools.Overrides = map[string]bool{"shell_exec": v}
		r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Install-wide shell setting")
		if r[0] != "" || r[2] != "tools.tool_overrides.shell_exec has no effect; shell commands are allowed per agent." {
			t.Errorf("override %v: row = %v", v, r)
		}
		// The override changes nobody's access.
		if s := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Shell access"); !strings.Contains(s[2], "Allow shell commands: bob;") {
			t.Errorf("override %v: shell row = %v", v, s)
		}
	}
}

// TestAssessment_IgnoredMount: a mount named after a workspace folder gets an
// action row naming the agent and the mount; a normal mount gets none.
func TestAssessment_IgnoredMount(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.List[0].Mounts = []config.MountConfig{{Name: "notes", Path: t.TempDir()}}
	for _, row := range collectAssessment(t.Context(), cfg, env).Tables[0].Rows {
		if strings.HasPrefix(row[1], "Mount (") {
			t.Errorf("row present for a normal mount: %v", row)
		}
	}
	cfg.Agents.List[0].Mounts = append(cfg.Agents.List[0].Mounts, config.MountConfig{Name: "Files", Path: t.TempDir()})
	id := cfg.Agents.List[0].ID
	r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Mount ("+id+")")
	if r[0] == "" || r[2] != `Ignored: "Files" is a reserved name.` {
		t.Errorf("row = %v", r)
	}
}

// TestAssessment_PersonChatNotSetUp: a human agent whose chat is on a channel
// the configuration does not start is marked for action; a chat on a running
// channel, or on a secmsg daemon that discovers its accounts, is not listed.
func TestAssessment_PersonChatNotSetUp(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{Name: "People", Protocol: config.HumanProtocol})
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "Bob person", Model: "bob", Provider: "People", Enabled: true})
	cfg.Agents.List[1].Models = []string{"Bob person"}
	const item = "Person's chat (bob)"
	hasRow := func() bool {
		for _, row := range collectAssessment(t.Context(), cfg, env).Tables[0].Rows {
			if row[1] == item {
				return true
			}
		}
		return false
	}

	if hasRow() {
		t.Fatal("a chat on a running channel must not be listed")
	}

	// The manager looks channels up by exact name: a binding that differs
	// only in case names no channel.
	cfg.Bindings[0].Match.Channel = "Telegram-Bob"
	r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), item)
	contains(t, r[2], "Channel Telegram-Bob is not set up", "row status")
	cfg.Bindings[0].Match.Channel = "telegram-bob"

	cfg.Channels.Telegram[0].Enabled = false
	r = assessmentRow(t, collectAssessment(t.Context(), cfg, env), item)
	if r[0] == "" {
		t.Error("expected an action mark")
	}
	contains(t, r[2], "Channel telegram-bob is not set up", "row status")

	cfg.Channels.SecMsg = []config.SecMsgConfig{{Enabled: true, Address: "127.0.0.1:1"}}
	cfg.Bindings[0].Match.Channel = "secmsg-bob"
	if hasRow() {
		t.Fatal("a chat on a secmsg daemon that discovers its accounts must not be listed")
	}
	cfg.Bindings[0].Match.Channel = "SecMsg-bob"
	if !hasRow() {
		t.Fatal("a secmsg name that differs in case names no channel")
	}

	cfg.Agents.List[1].Models = nil
	cfg.Bindings[0].Match.Channel = "nowhere"
	if hasRow() {
		t.Fatal("an agent that is not a person must not be listed")
	}
}
