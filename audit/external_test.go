// ClawEh
// License: MIT

package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectExternal_MCPServers(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectExternal(t.Context(), cfg, env)
	tb := findTable(t, s, "MCP servers (tools.mcp.servers)")

	_, fusion := findRow(t, tb, "fusion")
	if fusion[2] != "http" || fusion[3] != "https://fusion.example.com/mcp" {
		t.Errorf("fusion row = %v", fusion)
	}
	if fusion[4] != "headers: Authorization" {
		t.Errorf("fusion secrets column = %q", fusion[4])
	}
	if fusion[5] != "alice" {
		t.Errorf("fusion agents = %q", fusion[5])
	}

	_, local := findRow(t, tb, "local")
	if local[2] != "stdio" || local[3] != "npx -y some-mcp-server" {
		t.Errorf("local row = %v", local)
	}
	if local[4] != "env: API_TOKEN; env_file: /etc/mcp.env" {
		t.Errorf("local secrets column = %q", local[4])
	}
	if local[5] != "(no agent)" {
		t.Errorf("local agents = %q", local[5])
	}
	txt := tableText(tb)
	if strings.Contains(txt, secretHeader) || strings.Contains(txt, secretMCPEnv) {
		t.Error("MCP header or env value leaked")
	}
}

func TestCollectExternal_SkillsAndRegistries(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectExternal(t.Context(), cfg, env)
	skillsTable := s.Tables[1]
	if skillsTable.Rows[0][0] != "(none installed)" {
		t.Errorf("fresh install skills = %v", skillsTable.Rows)
	}

	dir := filepath.Join(cfg.SkillsPath(), "weather")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: weather\ndescription: Reads the forecast\n---\n# Weather\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsTable = collectExternal(t.Context(), cfg, env).Tables[1]
	_, w := findRow(t, skillsTable, "weather")
	if w[1] != "global" {
		t.Errorf("skill source = %q", w[1])
	}

	reg := findTable(t, s, "Skill registries")
	_, gh := findRow(t, reg, "GitHub")
	if gh[1] != "token set, proxy (none)" {
		t.Errorf("github row = %q", gh[1])
	}
	_, hub := findRow(t, reg, "ClawHub")
	contains(t, hub[1], "auth token set", "clawhub token")

	web := findTable(t, s, "Web tools")
	_, brave := findRow(t, web, "Brave")
	if brave[1] != "on, key set (1)" {
		t.Errorf("brave row = %q", brave[1])
	}
	_, ddg := findRow(t, web, "DuckDuckGo")
	if ddg[1] != "off (no key)" {
		t.Errorf("ddg row = %q", ddg[1])
	}

	voice := findTable(t, s, "Voice")
	_, active := findRow(t, voice, "Active transcription chain")
	if active[1] != "groq" {
		t.Errorf("active transcription = %q", active[1])
	}
	for _, sec := range allSecrets {
		if strings.Contains(RenderText(&Report{Sections: []Section{s}}), sec) {
			t.Errorf("secret %q leaked into external services", sec)
		}
	}
}
