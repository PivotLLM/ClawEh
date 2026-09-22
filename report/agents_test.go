// ClawEh
// License: MIT

package report

import (
	"path/filepath"
	"strings"
	"testing"
)

func agentSub(t *testing.T, s Section, id string) Section {
	t.Helper()
	for _, sub := range s.Subsections {
		if strings.HasPrefix(sub.Title, id) {
			return sub
		}
	}
	t.Fatalf("agent subsection %q not found", id)
	return Section{}
}

func TestCollectAgents_FolderAccessResolved(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAgents(t.Context(), cfg, env)
	alice := agentSub(t, s, "alice")
	fa := findTable(t, alice, "Folder access")

	realWS := filepath.Join(env.DataDir, "real-workspace")
	link := filepath.Join(env.DataDir, "link-workspace")
	// The write area is files/ under the symlinked workspace: shown resolved
	// with the configured form in brackets.
	_, files := findRow(t, fa, filepath.Join(realWS, "files")+" ["+filepath.Join(link, "files")+"]")
	if files[1] != "[read/write]" {
		t.Errorf("files access = %q", files[1])
	}
	// Read-only workspace areas.
	_, tasks := findRow(t, fa, filepath.Join(link, "tasks"))
	if tasks[1] != "[read]" || !strings.Contains(tasks[2], "does not exist") {
		t.Errorf("tasks row = %v", tasks)
	}
	// Allow-list patterns are shown verbatim.
	_, rp := findRow(t, fa, "^/srv/shared/")
	if rp[1] != "[read]" || !strings.Contains(rp[2], "pattern") {
		t.Errorf("read pattern row = %v", rp)
	}
	_, wp := findRow(t, fa, "^/srv/out/")
	if wp[1] != "[write]" {
		t.Errorf("write pattern row = %v", wp)
	}
	_, skills := findRow(t, fa, cfg.SkillsPath())
	if skills[1] != "[read]" {
		t.Errorf("skills row = %v", skills)
	}
	_, mem := findRow(t, fa, filepath.Join(link, "cogmem"))
	if mem[1] != "[write]" {
		t.Errorf("memory row = %v", mem)
	}
	if len(fa.Highlight) != 0 {
		t.Errorf("confined agent must have no highlighted row, got %v", fa.Highlight)
	}

	bob := agentSub(t, s, "bob")
	ofa := findTable(t, bob, "Folder access")
	_, docs := findRow(t, ofa, filepath.Join(env.DataDir, "docs"))
	if docs[1] != "[read]" || !strings.Contains(docs[2], "mount docs, notify") {
		t.Errorf("mount row = %v", docs)
	}
	opsWS := filepath.Join(cfg.BaseDir(), "bob")
	_, maestro := findRow(t, ofa, filepath.Join(opsWS, "maestro"))
	if maestro[1] != "[write]" || !strings.Contains(maestro[2], "Maestro data (auto mount)") {
		t.Errorf("maestro row = %v", maestro)
	}
}

func TestCollectAgents_UnrestrictedIsHighlighted(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = false
	fa := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "alice"), "Folder access")
	if len(fa.Rows) == 0 || fa.Rows[0][0] != "anything user eric can access" {
		t.Fatalf("first row = %v", fa.Rows)
	}
	if fa.Rows[0][1] != "[read/write]" || !strings.Contains(fa.Rows[0][2], "restrict_to_workspace is off") {
		t.Errorf("unrestricted row = %v", fa.Rows[0])
	}
	if !isHighlighted(fa, 0) {
		t.Error("unrestricted row must be highlighted")
	}
	if strings.Contains(tableText(fa), "^/srv/") {
		t.Error("patterns are moot when unrestricted and must not be listed")
	}
}

func TestCollectAgents_ReadOutsideWorkspace(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Agents.Defaults.AllowReadOutsideWorkspace = true
	fa := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "alice"), "Folder access")
	if fa.Rows[0][0] != "anything user eric can read" || fa.Rows[0][1] != "[read]" {
		t.Errorf("first row = %v", fa.Rows[0])
	}
	if !isHighlighted(fa, 0) {
		t.Error("read-anywhere row must be highlighted")
	}
}

func TestCollectAgents_ToolsAndSensitivity(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAgents(t.Context(), cfg, env)

	// alice has no tools key: the install defaults ("*") apply, and every
	// sensitive tool is admitted by the pattern.
	alice := toolMarkers(findTable(t, agentSub(t, s, "alice"), "Internal tools"))
	if alice["*"] != "no" {
		t.Errorf("* marker = %q", alice["*"])
	}
	if alice["shell_exec"] != "yes (by pattern)" {
		t.Errorf("shell_exec marker = %q", alice["shell_exec"])
	}

	// bob lists two tools explicitly: one row holding both pairs.
	bobTable := findTable(t, agentSub(t, s, "bob"), "Internal tools")
	bob := toolMarkers(bobTable)
	if bob["file_read"] != "no" || bob["shell_exec"] != "yes" {
		t.Errorf("bob markers = %v", bob)
	}
	if len(bobTable.Rows) != 1 {
		t.Errorf("bob tools rows = %v", bobTable.Rows)
	}

	// An empty tools list means no tools.
	empty := []string{}
	cfg.Agents.List[1].Tools = empty
	bobTable = findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Internal tools")
	if len(bobTable.Rows) != 1 || !strings.HasPrefix(bobTable.Rows[0][0], "(none") {
		t.Errorf("empty tools rows = %v", bobTable.Rows)
	}
}

func TestCollectAgents_MCPAccess(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAgents(t.Context(), cfg, env)
	mcp := findTable(t, agentSub(t, s, "alice"), "MCP access")
	_, fusion := findRow(t, mcp, "fusion")
	if fusion[1] != "fusion (all tools)" {
		t.Errorf("fusion reach = %q", fusion[1])
	}
	i, nothing := findRow(t, mcp, "nothing")
	if nothing[1] != "matches no configured server" || !isHighlighted(mcp, i) {
		t.Errorf("unmatched entry row = %v highlighted=%v", nothing, isHighlighted(mcp, i))
	}

	cfg.Agents.List[0].MCPTools = []string{"fusion_gcwx"}
	mcp = findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "alice"), "MCP access")
	_, sub := findRow(t, mcp, "fusion_gcwx")
	if sub[1] != "fusion (tools starting with gcwx)" {
		t.Errorf("prefixed reach = %q", sub[1])
	}

	bob := findTable(t, agentSub(t, s, "bob"), "MCP access")
	if bob.Rows[0][0] != none {
		t.Errorf("bob mcp rows = %v", bob.Rows)
	}
}

func TestCollectAgents_Settings(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAgents(t.Context(), cfg, env)
	st := findTable(t, agentSub(t, s, "bob"), "Settings")
	_, ws := findRow(t, st, "Workspace")
	if ws[1] != filepath.Join(cfg.BaseDir(), "bob") {
		t.Errorf("bob workspace = %q", ws[1])
	}
	_, models := findRow(t, st, "Models")
	if models[1] != "GPT\nClaude CLI\n(from defaults)" {
		t.Errorf("bob models = %q", models[1])
	}
	_, suites := findRow(t, st, "Suites")
	if suites[1] != "Maestro on, Fusion off" {
		t.Errorf("suites = %q", suites[1])
	}
	_, cron := findRow(t, st, "Global cron")
	if cron[1] != "on" {
		t.Errorf("global cron = %q", cron[1])
	}
	_, def := findRow(t, st, "Routing default")
	if def[1] != "no" {
		t.Errorf("bob routing default = %q", def[1])
	}
	_, def = findRow(t, findTable(t, agentSub(t, s, "alice"), "Settings"), "Routing default")
	if def[1] != "yes" {
		t.Errorf("alice routing default = %q", def[1])
	}
}

// toolMarkers flattens the two-tools-per-row Internal tools table into
// tool -> sensitive marker.
func toolMarkers(tb Table) map[string]string {
	out := map[string]string{}
	for _, r := range tb.Rows {
		for c := 0; c+1 < len(r); c += 2 {
			if r[c] != "" {
				out[r[c]] = r[c+1]
			}
		}
	}
	return out
}
