// ClawEh
// License: MIT

package report

import (
	"context"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
)

// maxClassifierErrorChars caps a last-run error in the Classifiers table.
const maxClassifierErrorChars = 200

// classifierCeilings is the install's classifier maximums (classifiers.limits
// and classifiers.processed_days, defaults for unset fields), the ones a run
// checks a definition against.
func classifierCeilings(cfg *config.Config) classifier.Ceilings {
	c := cfg.Classifiers.Effective()
	return classifier.Ceilings{
		MaxRules:         c.Limits.MaxRules,
		MaxPromptChars:   c.Limits.MaxPromptChars,
		MaxItemChars:     c.Limits.MaxItemChars,
		MaxItemsPerRun:   c.Limits.MaxItemsPerRun,
		MaxParallel:      c.Limits.MaxParallel,
		MaxActivityHours: c.Limits.MaxActivityHours,
		MinEverySeconds:  c.Limits.MinEverySeconds,
		ProcessedDays:    c.ProcessedDays,
	}
}

// classifiersSwitch is the "Allow classifiers" value: on or off, marked when
// the agent represents a person (its classifiers are set aside).
func classifiersSwitch(a *config.AgentConfig, human bool) string {
	if human && a.ClassifiersEnabled() {
		return "on (ignored: the agent represents a person)"
	}
	return onOff(a.ClassifiersEnabled())
}

// classifierModelState is the "Classifier model" value and whether it is
// marked: with the switch on, a missing model or one that is not one of the
// agent's models (its classifiers fail every run until it is fixed).
func classifierModelState(cfg *config.Config, a *config.AgentConfig) (string, bool) {
	model := a.ClassifierModel()
	if !a.ClassifiersEnabled() {
		return orValue(model, none), false
	}
	switch {
	case model == "":
		return "(none set)", true
	case !cfg.ClassifierModelUsable(a, model):
		return model + " (not one of the agent's models)", true
	}
	return model, false
}

// classifiersDir is the agent's classifiers folder.
func classifiersDir(cfg *config.Config, a *config.AgentConfig) string {
	return filepath.Join(resolveWorkspace(cfg, a), classifierfs.BaseDirName)
}

// classifiersTable is one row per definition file in the agent's
// classifiers folder, or ok=false when the switch is off and the folder has
// none. Invalid files, files owned by another agent and runs that ended in
// an error are highlighted.
func classifiersTable(ctx context.Context, cfg *config.Config, a *config.AgentConfig) (Table, bool) {
	t := Table{Caption: "Classifiers", Columns: []string{"Classifier", "Enabled", "Dry run", "Model", "Last run", "Last run counts", "Last error"}}
	sums, err := classifier.Inspect(ctx, classifiersDir(cfg, a), classifierCeilings(cfg))
	if err != nil {
		t.Highlight = append(t.Highlight, 0)
		t.Rows = append(t.Rows, row(none, "", "", "", "", "", "unreadable: "+err.Error()))
		return t, true
	}
	if len(sums) == 0 && !a.ClassifiersEnabled() {
		return t, false
	}
	if len(sums) == 0 {
		t.Rows = append(t.Rows, row(none, "", "", "", "", "", ""))
		return t, true
	}
	model := orValue(a.ClassifierModel(), none)
	for _, s := range sums {
		enabled := yesNo(s.Enabled)
		lastErr := ""
		marked := false
		switch {
		case s.Owner != "" && !config.SameAgentID(s.Owner, a.ID):
			enabled, lastErr, marked = "not run", "Owned by "+s.Owner+".", true
		case s.Invalid != "":
			enabled, lastErr, marked = "not run", "Invalid: "+s.Invalid, true
		case s.Enabled && !a.ClassifiersEnabled():
			enabled = "yes (Allow classifiers off)"
		}
		last, counts := "never", ""
		if r := s.LastRun; r != nil {
			at := r.EndedAt
			if at.IsZero() {
				at = r.StartedAt
			}
			last = at.Format(timeFormat) + ", " + r.Status
			if r.DryRun {
				last += " (dry run)"
			}
			counts = "new " + itoa(r.NewItems) + ", acted " + itoa(r.Acted) +
				", unclassified " + itoa(r.Unclassified) + ", failed " + itoa(r.Failed)
			if lastErr == "" {
				lastErr = truncateText(r.Error, maxClassifierErrorChars)
			}
			if r.Status == classifier.RunError {
				marked = true
			}
		}
		if marked {
			t.Highlight = append(t.Highlight, len(t.Rows))
		}
		t.Rows = append(t.Rows, row(s.ID, enabled, yesNo(s.DryRun), model, last, counts, lastErr))
	}
	return t, true
}

// truncateText cuts s to at most n runes, marking the cut.
func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// classifierAssessment is the assessment rows for classifiers: per enabled
// agent with the switch on (a person's agent excluded), a missing or
// unusable classifier model and every definition file that is not run
// because it is invalid.
func classifierAssessment(ctx context.Context, cfg *config.Config) []assessRow {
	var rows []assessRow
	for _, a := range enabledAgents(cfg) {
		if !a.ClassifiersEnabled() || cfg.IsHumanAgent(a.ID) {
			continue
		}
		name := a.DisplayName()
		switch model := a.ClassifierModel(); {
		case model == "":
			rows = append(rows, assessRow{true, "Classifier model (" + name + ")", "None set."})
		case !cfg.ClassifierModelUsable(a, model):
			rows = append(rows, assessRow{true, "Classifier model (" + name + ")", model + " is not one of " + name + "'s models."})
		}
		sums, err := classifier.Inspect(ctx, classifiersDir(cfg, a), classifierCeilings(cfg))
		if err != nil {
			continue
		}
		for _, s := range sums {
			if s.Invalid != "" {
				rows = append(rows, assessRow{true, "Classifier (" + name + ": " + s.ID + ")", "Invalid, not run: " + s.Invalid})
			}
		}
	}
	return rows
}
