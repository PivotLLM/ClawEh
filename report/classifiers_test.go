// ClawEh
// License: MIT

package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
)

// writeClassifier writes a definition file into the agent's classifiers
// folder, as the classifier tools or a hand edit would.
func writeClassifier(t *testing.T, cfg *config.Config, agentID, id string, def map[string]any) {
	t.Helper()
	dir := filepath.Join(resolveWorkspace(cfg, cfg.AgentByID(agentID)), classifierfs.BaseDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+classifierfs.DefinitionExt), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// classifierDef is a valid definition owned by owner.
func classifierDef(id, owner string) map[string]any {
	return map[string]any{
		"id": id, "owner": owner, "enabled": true, "dry_run": true,
		"trigger": map[string]any{"every_seconds": 60},
		"source":  map[string]any{"tool": "mail_list", "items": "messages", "id": "id"},
		"classify": map[string]any{
			"instructions": "Decide how important this email is.",
			"categories":   []any{map[string]any{"name": "urgent"}, map[string]any{"name": "low"}},
		},
	}
}

// The Settings rows: the switch and the model, marked when the switch is on
// and the model is missing or not one of the agent's models.
func TestCollectAgents_ClassifierSettings(t *testing.T) {
	cfg, env := fixtureConfig(t)
	settings := func(id string) Table {
		return findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), id), "Settings")
	}
	check := func(id, wantSwitch, wantModel string, wantMarked bool) {
		t.Helper()
		tb := settings(id)
		if _, r := findRow(t, tb, "Allow classifiers"); r[1] != wantSwitch {
			t.Errorf("%s Allow classifiers = %q, want %q", id, r[1], wantSwitch)
		}
		i, r := findRow(t, tb, "Classifier model")
		if r[1] != wantModel || isHighlighted(tb, i) != wantMarked {
			t.Errorf("%s Classifier model = %q marked=%v, want %q marked=%v", id, r[1], isHighlighted(tb, i), wantModel, wantMarked)
		}
	}

	check("alice", "off", none, false)

	bob := &cfg.Agents.List[1]
	bob.Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "GPT"}
	check("bob", "on", "GPT", false)

	bob.Classifiers.Model = "Codex Fast"
	check("bob", "on", "Codex Fast (not one of the agent's models)", true)

	bob.Classifiers.Model = ""
	check("bob", "on", "(none set)", true)

	// With the switch off the model is not used, so it is not marked.
	bob.Classifiers = &config.AgentClassifiersConfig{Model: "Codex Fast"}
	check("bob", "off", "Codex Fast", false)
}

// The Classifiers table lists every definition file, marking an invalid one
// and one owned by another agent; it is left out when the switch is off and
// the folder is empty.
func TestCollectAgents_ClassifiersTable(t *testing.T) {
	cfg, env := fixtureConfig(t)
	hasTable := func(id string) bool {
		for _, tb := range agentSub(t, collectAgents(t.Context(), cfg, env), id).Tables {
			if tb.Caption == "Classifiers" {
				return true
			}
		}
		return false
	}
	if hasTable("bob") {
		t.Fatal("Classifiers table shown with the switch off and no files")
	}

	bob := &cfg.Agents.List[1]
	bob.Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "GPT"}
	tb := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	if len(tb.Rows) != 1 || tb.Rows[0][0] != none {
		t.Errorf("empty folder rows = %v", tb.Rows)
	}

	writeClassifier(t, cfg, "bob", "inbox", classifierDef("inbox", "bob"))
	writeClassifier(t, cfg, "bob", "broken", map[string]any{"id": "broken", "owner": "bob", "surprise": 1})
	writeClassifier(t, cfg, "bob", "alices", classifierDef("alices", "alice"))

	tb = findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	rows := map[string][]string{}
	marked := map[string]bool{}
	for i, r := range tb.Rows {
		rows[r[0]] = r
		marked[r[0]] = isHighlighted(tb, i)
	}
	if r := rows["inbox"]; r == nil || r[1] != "yes" || r[2] != "yes" || r[3] != "GPT" || r[4] != "never" || r[6] != "" || marked["inbox"] {
		t.Errorf("inbox row = %v marked=%v", r, marked["inbox"])
	}
	if r := rows["broken"]; r == nil || r[1] != "not run" || !strings.HasPrefix(r[6], "Invalid: ") || !marked["broken"] {
		t.Errorf("broken row = %v marked=%v", r, marked["broken"])
	}
	if r := rows["alices"]; r == nil || r[1] != "not run" || r[6] != "Owned by alice." || !marked["alices"] {
		t.Errorf("alices row = %v marked=%v", r, marked["alices"])
	}

	// Switched off, the files stay and the enabled one says why it does not run.
	bob.Classifiers.Enabled = false
	tb = findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	for _, r := range tb.Rows {
		if r[0] == "inbox" && r[1] != "yes (Allow classifiers off)" {
			t.Errorf("inbox with the switch off = %v", r)
		}
	}
}

// Assessment rows: a missing or unusable classifier model and an invalid
// definition, for agents with the switch on only.
func TestAssessment_Classifiers(t *testing.T) {
	cfg, env := fixtureConfig(t)
	bob := &cfg.Agents.List[1]
	bob.Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "Codex Fast"}
	writeClassifier(t, cfg, "bob", "broken", map[string]any{"id": "broken", "owner": "bob", "surprise": 1})
	writeClassifier(t, cfg, "bob", "inbox", classifierDef("inbox", "bob"))

	s := collectAssessment(t.Context(), cfg, env)
	if r := assessmentRow(t, s, "Classifier model (Bob)"); r[0] != actionMark || r[2] != "Codex Fast is not one of Bob's models." {
		t.Errorf("model row = %v", r)
	}
	if r := assessmentRow(t, s, "Classifier (Bob: broken)"); r[0] != actionMark || !strings.HasPrefix(r[2], "Invalid, not run: ") {
		t.Errorf("invalid row = %v", r)
	}
	for _, r := range s.Tables[0].Rows {
		if r[1] == "Classifier (Bob: inbox)" {
			t.Errorf("valid definition listed: %v", r)
		}
	}

	// A definition above the install's maximums is invalid, as a run finds it.
	cfg.Classifiers.Limits.MaxPromptChars = 10
	contains(t, assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Classifier (Bob: inbox)")[2],
		"this install allows 10", "row above the maximums")

	bob.Classifiers.Model = ""
	if r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Classifier model (Bob)"); r[2] != "None set." {
		t.Errorf("missing model row = %v", r)
	}

	bob.Classifiers.Enabled = false
	for _, r := range collectAssessment(t.Context(), cfg, env).Tables[0].Rows {
		if strings.HasPrefix(r[1], "Classifier") {
			t.Errorf("switch off, row listed: %v", r)
		}
	}
}

func TestCollectData_Classifiers(t *testing.T) {
	cfg, env := fixtureConfig(t)
	_, r := findRow(t, findTable(t, collectData(t.Context(), cfg, env), "Storage"), "Classifiers")
	if r[1] != "<workspace>/classifiers" || r[2] != "definitions and run state per agent; processed items remembered 30 days" {
		t.Errorf("classifiers row = %v", r)
	}
}
