// ClawEh
// License: MIT

package report

import (
	"strings"
	"testing"
)

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

// TestAssessment_TransportAndAuth: HTTPS and operator authentication are not
// implemented; they are marked only while a listener is reachable from
// other hosts.
func TestAssessment_TransportAndAuth(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	https := assessmentRow(t, s, "Transport encryption (HTTPS)")
	if https[0] != "*" {
		t.Errorf("HTTPS mark = %q, want * (gateway bound to all interfaces)", https[0])
	}
	contains(t, https[2], "Not implemented yet", "https status")
	contains(t, https[2], "Reachable from other hosts: Gateway (WebUI and HTTP API) on 0.0.0.0:18790", "https exposure")
	auth := assessmentRow(t, s, "Operator authentication (WebUI and API)")
	if auth[0] != "*" {
		t.Errorf("auth mark = %q, want *", auth[0])
	}
	contains(t, auth[2], "Not implemented yet", "auth status")

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
	if auth := assessmentRow(t, s, "Operator authentication (WebUI and API)"); auth[0] != "" {
		t.Errorf("auth mark = %q, want blank", auth[0])
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
