// ClawEh
// License: MIT

package report

import (
	"strings"
	"testing"
)

func TestCollectSummary_Confined(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectSummary(t.Context(), cfg, env)
	if s.Notes[0] != "ClawEh runs as user eric, group staff on testbox." {
		t.Errorf("headline = %q", s.Notes[0])
	}
	tb := s.Tables[0]
	_, files := findRow(t, tb, "Files")
	want := "2 of 2 agents confined to their workspace and mounts (alice, bob); allow-listed host patterns: 1 read, 1 write."
	if files[1] != want {
		t.Errorf("Files = %q\nwant %q", files[1], want)
	}
	_, shell := findRow(t, tb, "Shell")
	if shell[1] != "shell_exec allowed for alice, bob; deny patterns on, remote commands off." {
		t.Errorf("Shell = %q", shell[1])
	}
	_, out := findRow(t, tb, "Outbound network")
	contains(t, out[1], "1 API provider endpoints (details below)", "outbound providers")
	contains(t, out[1], "MCP servers:\n"+bullet+"fusion (fusion.example.com)", "outbound mcp")
	_, in := findRow(t, tb, "Inbound network")
	contains(t, in[1], "Gateway (WebUI and HTTP API) on 127.0.0.1:18790 (HTTP), 0.0.0.0:18443 (HTTPS)", "inbound gateway")
	contains(t, in[1], "MCP host on 127.0.0.1:5911/mcp", "inbound mcp host (auto_enable with a CLI provider)")
	_, msg := findRow(t, tb, "Messaging")
	if msg[1] != "Telegram-bob, Discord, Slack, Matrix, Line, WebUI, Device" {
		t.Errorf("Messaging = %q", msg[1])
	}
	_, dev := findRow(t, tb, "Devices")
	if dev[1] != "Device gateway on\npaired devices unknown (store unavailable)" {
		t.Errorf("Devices = %q", dev[1])
	}
	_, ext := findRow(t, tb, "External execution")
	if ext[1] != "CLI providers: Claude CLI, Codex CLI\nMCP stdio commands: local (npx)" {
		t.Errorf("External execution = %q", ext[1])
	}
	_, sk := findRow(t, tb, "Skills")
	if sk[1] != "(none installed)" {
		t.Errorf("Skills = %q", sk[1])
	}
}

func TestCollectSummary_Unconfined(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = false
	_, files := findRow(t, collectSummary(t.Context(), cfg, env).Tables[0], "Files")
	want := "agent alice can read and write anything user eric can access; " +
		"agent bob can read and write anything user eric can access (restrict_to_workspace is off)."
	if files[1] != want {
		t.Errorf("Files = %q\nwant %q", files[1], want)
	}

	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.Defaults.AllowReadOutsideWorkspace = true
	_, files = findRow(t, collectSummary(t.Context(), cfg, env).Tables[0], "Files")
	if !strings.HasPrefix(files[1], "agent alice can read anything user eric can read; agent bob can read anything user eric can read;") {
		t.Errorf("Files = %q", files[1])
	}
}

func TestCollectSummary_NoShell(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.List[0].Tools = []string{"file_read"}
	cfg.Agents.List[1].Tools = []string{"file_read"}
	_, shell := findRow(t, collectSummary(t.Context(), cfg, env).Tables[0], "Shell")
	if shell[1] != "No enabled agent has shell_exec (deny patterns on, remote commands off)." {
		t.Errorf("Shell = %q", shell[1])
	}
}
