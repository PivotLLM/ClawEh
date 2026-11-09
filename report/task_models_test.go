// ClawEh
// License: MIT

package report

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// taskModelsSection is the "Task models" subsection of the providers section.
func taskModelsSection(t *testing.T, cfg *config.Config, env Environment) Section {
	t.Helper()
	for _, sub := range collectProviders(t.Context(), cfg, env).Subsections {
		if sub.Title == "Task models" {
			return sub
		}
	}
	t.Fatal("Task models subsection not found")
	return Section{}
}

// One row per task model with its ticked uses; a use the provider's protocol
// cannot serve is named and the row highlighted.
func TestCollectProviders_TaskModelsTable(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.TaskModels.Models = []config.TaskModel{
		{Model: "GPT", STT: true, Vision: true},
		{Model: "Claude CLI", Text: true, STT: true, Classify: true},
		{Model: "Codex Fast"},
	}
	tb := findTable(t, taskModelsSection(t, cfg, env), "Task models")
	want := [][]string{
		{"GPT", "Transcription (STT), Vision", ""},
		{"Claude CLI", "Text, Transcription (STT), Classify", "Transcription (STT)"},
		{"Codex Fast", none, ""},
	}
	if len(tb.Rows) != len(want) {
		t.Fatalf("rows = %v, want %v", tb.Rows, want)
	}
	for i, w := range want {
		for j := range w {
			if tb.Rows[i][j] != w[j] {
				t.Errorf("row %d = %v, want %v", i, tb.Rows[i], w)
				break
			}
		}
		if isHighlighted(tb, i) != (w[2] != "") {
			t.Errorf("row %d highlighted = %v", i, isHighlighted(tb, i))
		}
	}

	cfg.TaskModels.Models = nil
	tb = findTable(t, taskModelsSection(t, cfg, env), "Task models")
	if len(tb.Rows) != 1 || tb.Rows[0][0] != none {
		t.Errorf("no task models rows = %v", tb.Rows)
	}
}

// "Speech to text" and "Vision" rows appear, unmarked and linked to the Task
// models page, while no model is ticked for them, and go once one is.
func TestAssessment_TaskModelRows(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.TaskModels.Models = nil
	s := collectAssessment(t.Context(), cfg, env)
	for _, item := range []string{itemSTT, itemVision} {
		if r := assessmentRow(t, s, item); r[0] != "" || r[2] != "No model." {
			t.Errorf("%s row = %v", item, r)
		}
	}
	a := Assess(t.Context(), cfg, env)
	links := 0
	for _, r := range a.Assessment {
		if r.Item == itemSTT || r.Item == itemVision {
			if r.Link != "/task-models" {
				t.Errorf("%s link = %q", r.Item, r.Link)
			}
			links++
		}
	}
	if links != 2 {
		t.Errorf("found %d task model rows in Assess, want 2", links)
	}

	cfg.TaskModels.Models = []config.TaskModel{{Model: "GPT", STT: true, Vision: true}}
	for _, r := range collectAssessment(t.Context(), cfg, env).Tables[0].Rows {
		if r[1] == itemSTT || r[1] == itemVision {
			t.Errorf("row listed with a model ticked: %v", r)
		}
	}
}

// The per-agent row lists the kinds the agent may use, or off.
func TestCollectAgents_TaskModelGrant(t *testing.T) {
	cfg, env := fixtureConfig(t)
	settings := func(id string) Table {
		return findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), id), "Settings")
	}
	if _, r := findRow(t, settings("alice"), "Task models"); r[1] != "off" {
		t.Errorf("no grant = %q", r[1])
	}
	cfg.Agents.List[0].TaskModels = &config.AgentTaskModels{Image: true, STT: true}
	if _, r := findRow(t, settings("alice"), "Task models"); r[1] != "Images, Transcription (STT)" {
		t.Errorf("grant = %q", r[1])
	}
}
