// ClawEh
// License: MIT

package report

import (
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/voice"
)

// Item names of the task-model assessment rows; the WebUI links them to the
// Task models page (assessmentLinks).
const (
	itemSTT    = "Speech to text"
	itemVision = "Vision"
)

// taskModelsLink is the WebUI page where the task model lists are kept.
const taskModelsLink = "/task-models"

// taskModelsTable is one row per list, in page order: its models in
// fallback order and those whose provider cannot serve the use (known from
// the protocol), highlighted.
func taskModelsTable(cfg *config.Config) Table {
	t := Table{Caption: "Task models", Columns: []string{"List", "Models", "Provider cannot serve"}}
	for _, use := range config.TaskUses {
		bad := cfg.TaskModelsUnsupported(use)
		if len(bad) > 0 {
			t.Highlight = append(t.Highlight, len(t.Rows))
		}
		t.Rows = append(t.Rows, row(use.Label(), joinOr(cfg.TaskModels.ModelsFor(use), none), joinOr(bad, "")))
	}
	return t
}

// taskModelAssessment is the assessment rows for the built-in uses whose
// list is empty: incoming voice messages are not transcribed and images are
// not described for text-only models until one is. A state, not a finding,
// so no action mark.
//
// When the list has models but none can be used the row is marked and names
// each with why: transcription by the transcriber's own rules
// (voice.TranscriptionStatus), vision by the configuration
// (Config.TaskModelProblem).
func taskModelAssessment(cfg *config.Config) []assessRow {
	var rows []assessRow
	if len(cfg.TaskModels.ModelsFor(config.TaskUseSTT)) == 0 {
		rows = append(rows, assessRow{false, itemSTT, "No model in " + config.TaskUseSTT.Label() + "."})
	} else if st := voice.TranscriptionStatus(cfg); len(st.Usable) == 0 {
		rows = append(rows, assessRow{true, itemSTT, noUsableModel(st.Unusable)})
	}
	vision := cfg.TaskModels.ModelsFor(config.TaskUseVision)
	if len(vision) == 0 {
		rows = append(rows, assessRow{false, itemVision, "No model in " + config.TaskUseVision.Label() + "."})
	} else {
		var unusable []voice.UnusableModel
		for _, name := range vision {
			if p := cfg.TaskModelProblem(name, config.TaskUseVision); p != "" {
				unusable = append(unusable, voice.UnusableModel{Model: name, Reason: p})
			}
		}
		if len(unusable) == len(vision) {
			rows = append(rows, assessRow{true, itemVision, noUsableModel(unusable)})
		}
	}
	return rows
}

// noUsableModel is the status of a use whose listed models are all
// unusable, naming each with why.
func noUsableModel(models []voice.UnusableModel) string {
	parts := make([]string, len(models))
	for i, m := range models {
		parts[i] = m.Model + " (" + m.Reason + ")"
	}
	return "No usable model: " + strings.Join(parts, ", ") + "."
}

// classifyNote is the classifier model status when the Classification list
// is empty, naming where to fix it.
const classifyNote = "No model in Classification; add one on the Task models page."

// taskModelGrant is the kinds of task model the agent may use through
// agent_spawn, by their labels, or "off"; a grant is marked ignored when
// the agent lacks agent_spawn, which is how it uses them.
func taskModelGrant(a *config.AgentConfig) string {
	var kinds []string
	for _, use := range config.TaskSpawnUses {
		if a.TaskModelAllowed(use) {
			kinds = append(kinds, use.Label())
		}
	}
	if len(kinds) == 0 {
		return "off"
	}
	out := strings.Join(kinds, ", ")
	if !a.IsToolAllowed(spawnTool) {
		out += " (ignored: no agent_spawn)"
	}
	return out
}

// spawnTool is the tool an agent calls task models with.
const spawnTool = "agent_spawn"
