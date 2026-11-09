// ClawEh
// License: MIT

package report

import "github.com/PivotLLM/ClawEh/config"

// Item names of the task-model assessment rows; the WebUI links them to the
// Task models page (assessmentLinks).
const (
	itemSTT    = "Speech to text"
	itemVision = "Vision"
)

// taskModelsLink is the WebUI page where task models are ticked.
const taskModelsLink = "/models/task"

// taskModelsTable is one row per task model: its ticked uses and the uses its
// provider cannot serve (known from the protocol), highlighted.
func taskModelsTable(cfg *config.Config) Table {
	t := Table{Caption: "Task models", Columns: []string{"Model", "Uses", "Provider cannot serve"}}
	for _, m := range cfg.TaskModels.Models {
		uses := labels(m.Uses())
		bad := cfg.TaskModelUnsupported(m)
		if len(bad) > 0 {
			t.Highlight = append(t.Highlight, len(t.Rows))
		}
		t.Rows = append(t.Rows, row(m.Model, joinOr(uses, none), joinOr(labels(bad), "")))
	}
	if len(t.Rows) == 0 {
		t.Rows = append(t.Rows, row(none, "", ""))
	}
	return t
}

// labels are the uses as the page names them.
func labels(uses []config.TaskUse) []string {
	out := make([]string, len(uses))
	for i, u := range uses {
		out[i] = u.Label()
	}
	return out
}

// taskModelAssessment is the assessment rows for the built-in uses with no
// model ticked: incoming voice messages are not transcribed and images are
// not described for text-only models until one is. A state, not a finding,
// so no action mark.
func taskModelAssessment(cfg *config.Config) []assessRow {
	var rows []assessRow
	if len(cfg.TaskModels.ModelsFor(config.TaskUseSTT)) == 0 {
		rows = append(rows, assessRow{false, itemSTT, "No model."})
	}
	if len(cfg.TaskModels.ModelsFor(config.TaskUseVision)) == 0 {
		rows = append(rows, assessRow{false, itemVision, "No model."})
	}
	return rows
}

// classifyNote is the classifier model status when no model is ticked
// Classify at all, naming where to fix it.
const classifyNote = "No Classify model; tick Classify on the Task models page."
