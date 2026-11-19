// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RuleUsage is how often each rule was cited over a classifier's activity
// window (activity_hours), counting each item once, by its latest answer
// (a rerun, a reused answer or a live run after a dry run replaces the
// earlier one): Decisions are the items given a category in the window;
// Cited counts, per label of the rules loaded now, the items whose answer
// cited it; NeverCited are the labels loaded now that no item's answer
// cited. Each decision is one of: WithRules, the items whose answer cited
// at least one rule loaded for its request; None, those whose answer was
// "Rules: none"; NoCitation, those whose answer cited nothing usable (no
// Rules line, or only unknown labels), so WithRules + None + NoCitation =
// Decisions. Unknown counts the items whose answer cited a label matching
// no rule of its request, whatever else it cited; OnlyUnknown those of them
// that cited nothing else (they are part of NoCitation), so Unknown -
// OnlyUnknown items cited a rule as well. UnknownLabels counts, per unknown
// label as the model wrote it, the items that cited it (the
// UnknownLabelsShown most cited; decisions stored before the labels were
// kept have none). NoLongerPresent counts, per cited label not
// among the rules now (a file removed or renamed), the items whose answer
// cited it.
type RuleUsage struct {
	Hours           int            `json:"hours"`
	Decisions       int            `json:"decisions"`
	Cited           map[string]int `json:"cited,omitempty"`
	NeverCited      []string       `json:"never_cited,omitempty"`
	WithRules       int            `json:"with_rules"`
	None            int            `json:"none"`
	NoCitation      int            `json:"no_citation"`
	Unknown         int            `json:"unknown"`
	OnlyUnknown     int            `json:"only_unknown"`
	UnknownLabels   map[string]int `json:"unknown_labels,omitempty"`
	NoLongerPresent map[string]int `json:"no_longer_present,omitempty"`
}

// currentLabels are the labels of def's rules as its Markdown is now (a
// missing file or folder has none); false when the Markdown cannot be read.
func (s *Service) currentLabels(scope Scope, def *Definition) ([]string, bool) {
	in, err := s.readInstructions(scope, def, true)
	if err != nil {
		return nil, false
	}
	return promptLabels(def, in), true
}

// ruleUsage is def's RuleUsage from the activity log of st at now.
func (s *Service) ruleUsage(ctx context.Context, scope Scope, def *Definition, st *stateDB, now time.Time) (*RuleUsage, error) {
	labels, ok := s.currentLabels(scope, def)
	if !ok {
		return nil, nil // the run's error says why; no usage against rules not known
	}
	hours := def.activityHours()
	rows, err := st.citations(ctx, NormalizeID(def.ID), now.Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return nil, err
	}
	u := &RuleUsage{Hours: hours}
	cited := map[string]int{}
	unknown := map[string]int{}
	u.Decisions = len(rows)
	for _, r := range rows {
		if r.unknown > 0 {
			u.Unknown++
			if len(r.rules) == 0 {
				u.OnlyUnknown++
			}
			for _, l := range r.labels {
				unknown[l]++
			}
		}
		switch {
		case len(r.rules) == 0:
			u.NoCitation++
			continue
		case slices.Equal(r.rules, []string{RulesNone}):
			u.None++
			continue
		}
		u.WithRules++
		for _, l := range r.rules {
			cited[l]++
		}
	}
	for _, l := range labels {
		if n := cited[l]; n > 0 {
			if u.Cited == nil {
				u.Cited = map[string]int{}
			}
			u.Cited[l] = n
			delete(cited, l)
		} else {
			u.NeverCited = append(u.NeverCited, l)
		}
	}
	if len(cited) > 0 {
		u.NoLongerPresent = cited
	}
	u.UnknownLabels = mostCited(unknown, UnknownLabelsShown)
	return u, nil
}

// UnknownLabelsShown is how many unknown labels RuleUsage lists.
const UnknownLabelsShown = 20

// mostCited keeps the n labels of counts cited by the most items (ties in
// the labels' order); nil when there are none.
func mostCited(counts map[string]int, n int) map[string]int {
	if len(counts) == 0 {
		return nil
	}
	labels := sortedKeys(counts)
	slices.SortStableFunc(labels, func(a, b string) int { return counts[b] - counts[a] })
	out := make(map[string]int, min(n, len(labels)))
	for _, l := range labels[:min(n, len(labels))] {
		out[l] = counts[l]
	}
	return out
}

// resolveRuleFilter is ActivityQuery.Rule as the label stored in the log:
// RulesNone, or the label it names among the rules loaded now and those
// the log cites (a file since removed is still found). An unknown label is
// refused.
func (s *Service) resolveRuleFilter(ctx context.Context, scope Scope, def *Definition, st *stateDB, cls, rule string) (string, error) {
	token := citationTrim(rule)
	if strings.EqualFold(token, RulesNone) {
		return RulesNone, nil
	}
	var labels []string
	if def != nil {
		labels, _ = s.currentLabels(scope, def)
	}
	logged, err := st.citedLabels(ctx, cls)
	if err != nil {
		return "", err
	}
	for _, l := range logged {
		if l != RulesNone && !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	if l, ok := resolveLabel(labels, token); ok {
		return l, nil
	}
	return "", unknownRule("rule", rule)
}

// unknownRule is the refusal of a label (of argument arg) that names no
// rule of the classifier.
func unknownRule(arg, label string) error {
	return &ValidationError{Issues: []string{fmt.Sprintf("%s %q is not one of this classifier's rules; use a rule's file name, rule N or instructions.", arg, label)}}
}

// resolveExclusions resolves classifier_test's exclude_rules against the
// labels of def's rules loaded now (in): the labels to leave out, or the
// refusal of the first unknown one.
func resolveExclusions(def *Definition, in instructions, exclude []string) (map[string]bool, error) {
	labels := promptLabels(def, in)
	out := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		l, ok := resolveLabel(labels, citationTrim(e))
		if !ok {
			return nil, unknownRule("exclude_rules entry", e)
		}
		out[l] = true
	}
	return out, nil
}
