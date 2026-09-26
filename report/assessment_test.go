// ClawEh
// License: MIT

package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

// TestAssessment_BypassCLIRestrictions: one unmarked awareness row per CLI
// provider with bypass_restrictions on, none for the others.
func TestAssessment_BypassCLIRestrictions(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Bypass CLI restrictions (Claude CLI)")
	if r[0] != "" {
		t.Errorf("mark = %q, want blank: awareness, not an action", r[0])
	}
	contains(t, r[2], "Bypass CLI restrictions is on for Claude CLI", "row status")
	contains(t, r[2], "outside ClawEh's workspace and shell controls", "row status")
	for _, row := range s.Tables[0].Rows {
		if strings.Contains(row[1], "Codex CLI") {
			t.Errorf("row for a CLI with bypass off: %v", row)
		}
	}

	cfg.Providers[1].BypassRestrictions = false
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if strings.HasPrefix(row[1], "Bypass CLI restrictions") {
			t.Errorf("row present with every bypass off: %v", row)
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

// TestAssessment_TransportAndAuth: the gateway is HTTPS off-box, so the HTTPS
// row is marked only while a plain-HTTP listener (device gateway, LINE) is
// reachable from other hosts; operator authentication is marked while no
// admin account exists, whatever the bind.
func TestAssessment_TransportAndAuth(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	https := assessmentRow(t, s, "Transport encryption (HTTPS)")
	if https[0] != "*" {
		t.Errorf("HTTPS mark = %q, want * (device gateway and LINE webhook are plain HTTP off-box)", https[0])
	}
	contains(t, https[2], "HTTPS on 0.0.0.0:18443 with a self-signed certificate", "https status")
	contains(t, https[2], "Reachable from other hosts: Gateway (WebUI and HTTP API) on 127.0.0.1:18790 (HTTP), 0.0.0.0:18443 (HTTPS)", "https exposure")
	contains(t, https[2], "Plain-HTTP listeners reachable from other hosts: Device gateway on 0.0.0.0:18791; LINE webhook on 0.0.0.0:18792/webhook/line", "plain listeners")
	if strings.Contains(https[2], "Gateway (WebUI and HTTP API) on 127.0.0.1:18790 (HTTP), 0.0.0.0:18443 (HTTPS); LINE") {
		t.Error("the HTTPS gateway must not be listed as a plain-HTTP listener")
	}

	// Gateway off-box but every other listener loopback: HTTPS row is not marked.
	cfg.Channels.Device.Host = "127.0.0.1"
	cfg.Channels.LINE.WebhookHost = "127.0.0.1"
	s = collectAssessment(t.Context(), cfg, env)
	if https = assessmentRow(t, s, "Transport encryption (HTTPS)"); https[0] != "" {
		t.Errorf("HTTPS mark = %q, want blank when the only off-box listener is HTTPS", https[0])
	}
	auth := assessmentRow(t, s, "Operator authentication (WebUI and API)")
	if auth[0] != "*" {
		t.Errorf("auth mark = %q, want * (no admin account)", auth[0])
	}
	contains(t, auth[2], "No admin account", "auth status")
	contains(t, auth[2], "claw admin", "auth fix")

	// Loopback everywhere: the facts stay, the marks go.
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Channels.Device.Host = "127.0.0.1"
	cfg.Channels.LINE.WebhookHost = "127.0.0.1"
	s = collectAssessment(t.Context(), cfg, env)
	https = assessmentRow(t, s, "Transport encryption (HTTPS)")
	if https[0] != "" {
		t.Errorf("HTTPS mark = %q, want blank when every listener is loopback", https[0])
	}
	contains(t, https[2], "All listeners are loopback only", "https loopback")
	// Loopback does not excuse a missing account: the WebUI is locked either way.
	if loopAuth := assessmentRow(t, s, "Operator authentication (WebUI and API)"); loopAuth[0] != "*" {
		t.Errorf("auth mark = %q, want * on loopback too", loopAuth[0])
	}

	// An account in the data dir clears the mark and is named by path.
	credPath := admin.Path(env.DataDir)
	if err := admin.Write(credPath, "alice", "a perfectly fine password"); err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	auth = assessmentRow(t, s, "Operator authentication (WebUI and API)")
	if auth[0] != "" {
		t.Errorf("auth mark = %q, want blank with an admin account", auth[0])
	}
	contains(t, auth[2], "Admin account configured ("+credPath+")", "auth configured")

	// A readable-by-others file is as good as none, and the row says how to fix it.
	if err := os.Chmod(credPath, 0o644); err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	auth = assessmentRow(t, s, "Operator authentication (WebUI and API)")
	if auth[0] != "*" {
		t.Errorf("auth mark = %q, want * for loose permissions", auth[0])
	}
	contains(t, auth[2], "chmod 600 "+credPath, "auth permission fix")
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
	if r := assessmentRow(t, s, "File confinement"); r[0] != "*" || !strings.Contains(r[2], "anything user eric can access") {
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
	// t.TempDir follows the umask; the gateway would have made it 0700.
	if err := os.Chmod(env.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "Data directory permissions"); r[0] != "" || r[2] != "CLAW_HOME and its secrets are owner-only." {
		t.Errorf("tight permissions row = %v", r)
	}

	loosePath := filepath.Join(env.DataDir, "state", "tokens.json")
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
	contains(t, r[2], "the gateway tightens them at start", "loose fix")

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

// TestAssessment_Certificate: the self-signed row is awareness (unmarked) and
// carries the fingerprint once the pair exists; a user-supplied certificate
// earns a marked row only within 14 days of expiry; nothing when HTTPS is off.
func TestAssessment_Certificate(t *testing.T) {
	cfg, env := fixtureConfig(t)
	// Fixture: HTTPS on, self-signed, not yet generated.
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Self-signed certificate")
	if r[0] != "" {
		t.Errorf("self-signed mark = %q, want blank", r[0])
	}
	contains(t, r[2], "HTTPS uses a self-signed certificate (not yet generated:", "ungenerated self-signed")
	contains(t, r[2], "verify the fingerprint with `claw tls`, or install your own with gateway.tls", "self-signed advice")

	certPath := filepath.Join(env.DataDir, tlscert.DirName, tlscert.SelfSignedCertFile)
	writeTestCert(t, certPath, env.Now.Add(300*24*time.Hour))
	info, err := tlscert.InspectFile(tlscert.Options{DataDir: env.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "Self-signed certificate")
	if r[0] != "" {
		t.Errorf("self-signed mark = %q, want blank", r[0])
	}
	if r[2] != "HTTPS uses a self-signed certificate (SHA-256 "+info.Fingerprint+"); browsers warn once — verify the fingerprint with `claw tls`, or install your own with gateway.tls." {
		t.Errorf("self-signed status = %q", r[2])
	}
	if !strings.Contains(info.Fingerprint, ":") || len(info.Fingerprint) != 95 {
		t.Errorf("fingerprint %q is not colon-separated SHA-256", info.Fingerprint)
	}

	// User-supplied, far from expiry: no certificate row at all.
	userCert := filepath.Join(env.DataDir, "user.crt")
	writeTestCert(t, userCert, env.Now.Add(100*24*time.Hour))
	cfg.Gateway.TLS = config.TLSConfig{CertFile: userCert, KeyFile: filepath.Join(env.DataDir, "user.key")}
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if row[1] == "Self-signed certificate" || row[1] == "TLS certificate expiry" {
			t.Errorf("certificate row for a valid user certificate: %v", row)
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

	// Already expired: still marked, says so.
	writeTestCert(t, userCert, env.Now.Add(-2*24*time.Hour))
	s = collectAssessment(t.Context(), cfg, env)
	r = assessmentRow(t, s, "TLS certificate expiry")
	if r[0] != "*" || r[2] != "TLS certificate expired on 2026-09-19 (2 days ago)." {
		t.Errorf("expired row = %v", r)
	}

	// Loopback gateway: no HTTPS listener, no certificate rows.
	cfg.Gateway.Host = "127.0.0.1"
	s = collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if row[1] == "Self-signed certificate" || row[1] == "TLS certificate expiry" {
			t.Errorf("certificate row with HTTPS off: %v", row)
		}
	}
}

// TestAssessment_AuditLog: missing on a running gateway is marked; present is
// named with its retention.
func TestAssessment_AuditLog(t *testing.T) {
	cfg, env := fixtureConfig(t)
	auditPath := filepath.Join(env.DataDir, "audit.db")
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Audit log")
	if r[0] != "*" {
		t.Errorf("missing audit mark = %q, want *", r[0])
	}
	if r[2] != "Audit log not initialised: "+auditPath+" is missing; the gateway creates it at start." {
		t.Errorf("missing audit status = %q", r[2])
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
