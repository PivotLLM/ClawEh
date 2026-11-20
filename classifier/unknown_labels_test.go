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
	"testing"
)

// A label the model knows but writes in another shape still names its rule:
// with a path before it, a note in parentheses after it, or the section
// marker around it. Anything that names no rule, or more than one, stays
// unknown and is kept as written.
func TestCitedForms(t *testing.T) {
	labels := []string{"files/classifiers/inbox/guide.md", "050-domain-example.md", "060-newsletters.md", "notes (old).md", "rule 1"}
	const domain, news = "050-domain-example.md", "060-newsletters.md"
	cases := []struct {
		line    string
		rules   []string
		unknown []string
	}{
		{"Rules: 050-domain-example.md", []string{domain}, nil},
		{"Rules: files/classifiers/inbox/rules/050-domain-example.md", []string{domain}, nil},
		{"Rules: rules/050-domain-example", []string{domain}, nil},
		{"Rules: 050-domain-example.md (client domain)", []string{domain}, nil},
		{"Rules: 050-domain-example.md (clients, partners), 060-newsletters.md (news; digests)", []string{domain, news}, nil},
		{"Rules: === Rules from 050-domain-example.md ===", []string{domain}, nil},
		{"Rules: Rules from 050-domain-example.md, rules from 060-newsletters", []string{domain, news}, nil},
		{"Rules: === Rules from rules/050-Domain-Example.md (clients) ===", []string{domain}, nil},
		{"Rules: guide.md (general guidance)", []string{"files/classifiers/inbox/guide.md"}, nil},
		{"Rules: rule 1 (the first one)", []string{"rule 1"}, nil},
		{"Rules: notes (old).md", []string{"notes (old).md"}, nil}, // the exact label wins before any reshaping
		// Still unknown, kept as the model wrote them.
		{"Rules: 050-domain-example.md and 060-newsletters.md", []string{}, []string{"050-domain-example.md and 060-newsletters.md"}},
		// A number that is no numbered rule names the file starting with it.
		{"Rules: 050", []string{domain}, nil},
		{"Rules: rule 50, #60", []string{domain, news}, nil},
		{"Rules: rule 1, 51", []string{"rule 1"}, []string{"51"}},
		{"Rules: instructions", []string{}, []string{"instructions"}},
		{"Rules: domain-example", []string{}, []string{"domain-example"}},
		{"Rules: 050-domain-example.md, Client   domains, Client domains", []string{domain}, []string{"Client domains"}},
		{"Rules: other/070-gone.md (old)", []string{}, []string{"other/070-gone.md (old)"}},
	}
	for _, tc := range cases {
		rules, unknown := parseCitation([]string{"low", "why", tc.line}).resolve(labels)
		if !slices.Equal(rules, tc.rules) || !slices.Equal(unknown, tc.unknown) {
			t.Errorf("%s\n  = %q, unknown %q\n  want %q, unknown %q", tc.line, rules, unknown, tc.rules, tc.unknown)
		}
	}
	// A reshaped form counts only when it names exactly one label.
	twice := []string{"a/guide.md", "guide.md", "Guide.md"}
	if l, ok := resolveLabel(twice, "x/GUIDE (general)"); ok {
		t.Errorf("an ambiguous reshaped citation resolved to %q", l)
	}
	// Bare "none" is never a label, however it is wrapped.
	if l, ok := resolveLabel([]string{"none.md"}, "Rules from none"); ok {
		t.Errorf(`"Rules from none" resolved to %q`, l)
	}
}

// The unknown labels of one reply are kept once each, cut, and bounded.
func TestUnknownLabelsAreBounded(t *testing.T) {
	tokens := make([]string, 0, UnknownLabelsMax+6)
	for i := range UnknownLabelsMax + 5 {
		tokens = append(tokens, fmt.Sprintf("made-up-%d", i))
	}
	tokens = append(tokens, strings.Repeat("x", UnknownLabelChars+50))
	_, unknown := parseCitation([]string{"low", "why", "Rules: " + strings.Join(tokens, ", ")}).resolve([]string{"rule 1"})
	if len(unknown) != UnknownLabelsMax {
		t.Fatalf("%d unknown labels kept, want %d", len(unknown), UnknownLabelsMax)
	}
	_, unknown = parseCitation([]string{"low", "why", "Rules: " + strings.Repeat("y", UnknownLabelChars+50)}).resolve([]string{"rule 1"})
	if len(unknown) != 1 || len([]rune(unknown[0])) != UnknownLabelChars {
		t.Errorf("a long label kept as %d characters", len([]rune(unknown[0])))
	}
}

// The unknown labels reach the activity log (where search finds them),
// classifier_test and the status, which tells an item that cited only
// unknown labels from one that cited a rule as well.
func TestUnknownLabelsAreReported(t *testing.T) {
	e := citeEnv(t)
	e.model.setReply(func(_, user string) (string, error) {
		switch {
		case strings.Contains(user, "About m1"):
			return "urgent\nclient\nRules: 050-domain-example.md, Client domains", nil
		case strings.Contains(user, "About m2"):
			return "low\nnews\nRules: Newsletter policy", nil
		}
		return "low\nnews\nRules: rules/060-news.md (digests), Newsletter policy", nil
	})
	if rec := e.run("inbox-priority"); rec.Acted != 3 {
		t.Fatalf("run = %+v", rec)
	}
	by := map[string]ActivityEntry{}
	for _, a := range e.activity(ActivityQuery{}) {
		by[a.ItemID] = a
	}
	if a := by["m1"]; !slices.Equal(a.Rules, []string{"050-domain-example.md"}) || !slices.Equal(a.UnknownLabels, []string{"Client domains"}) || a.UnknownRules != 1 {
		t.Errorf("m1 = %v, unknown %v (%d)", a.Rules, a.UnknownLabels, a.UnknownRules)
	}
	if a := by["m2"]; len(a.Rules) != 0 || !slices.Equal(a.UnknownLabels, []string{"Newsletter policy"}) {
		t.Errorf("m2 = %v, unknown %v", a.Rules, a.UnknownLabels)
	}
	if a := by["m3"]; !slices.Equal(a.Rules, []string{"060-news.md"}) || !slices.Equal(a.UnknownLabels, []string{"Newsletter policy"}) {
		t.Errorf("m3 = %v, unknown %v", a.Rules, a.UnknownLabels)
	}
	if found := e.activity(ActivityQuery{Search: "newsletter POLICY"}); len(found) != 2 {
		t.Errorf("search for an unknown label found %d entries, want 2", len(found))
	}

	u := e.status().RuleUsage
	if u == nil || u.Unknown != 3 || u.OnlyUnknown != 1 || u.NoCitation != 1 || u.WithRules != 2 {
		t.Fatalf("rule usage = %+v", u)
	}
	if want := map[string]int{"Newsletter policy": 2, "Client domains": 1}; fmt.Sprint(u.UnknownLabels) != fmt.Sprint(want) {
		t.Errorf("unknown labels = %v, want %v", u.UnknownLabels, want)
	}

	res, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "m2")
	if err != nil || !slices.Equal(res.UnknownLabels, []string{"Newsletter policy"}) {
		t.Errorf("Test = unknown %v, %v", res.UnknownLabels, err)
	}
}

// The status lists only the most cited unknown labels.
func TestMostCited(t *testing.T) {
	counts := map[string]int{"a": 1, "b": 3, "c": 2, "d": 3}
	if got := mostCited(counts, 2); fmt.Sprint(got) != fmt.Sprint(map[string]int{"b": 3, "d": 3}) {
		t.Errorf("mostCited = %v", got)
	}
	if got := mostCited(nil, 2); got != nil {
		t.Errorf("mostCited of nothing = %v", got)
	}
}

// A number names a rule file only when no numbered rule has it and exactly
// one file starts with it; the request offers "rule N" only when it has
// numbered rules.
func TestNumberNamesARuleFile(t *testing.T) {
	files := []string{"files/classifiers/inbox/guide.md", "11-jira.md", "020-news.md", "110-other.md"}
	for token, want := range map[string]string{"rule 11": "11-jira.md", "Rule #11": "11-jira.md", "11": "11-jira.md", "rule 20": "020-news.md"} {
		if l, ok := resolveLabel(files, token); !ok || l != want {
			t.Errorf("resolveLabel(%q) = %q, %v; want %q", token, l, ok, want)
		}
	}
	for _, token := range []string{"rule 1", "rule 2", "rule 12"} {
		if l, ok := resolveLabel(files, token); ok {
			t.Errorf("%q resolved to %q", token, l)
		}
	}
	// A numbered rule 11 is "rule 11" itself; two files with the number are ambiguous.
	if l, ok := resolveLabel(append(files, "rule 11"), "rule 11"); !ok || l != "rule 11" {
		t.Errorf("with a numbered rule 11: %q, %v", l, ok)
	}
	if l, ok := resolveLabel(append(files, "011-twice.md"), "rule 11"); ok {
		t.Errorf("two files numbered 11 resolved to %q", l)
	}

	e := newEnv(t)
	def := definition()
	delete(obj(def["classify"]), "rules")
	e.create(def)
	e.run("inbox-priority")
	if system := e.model.systems[0]; strings.Contains(system, `"rule N"`) || !strings.Contains(system, "copy it exactly as written there") {
		t.Errorf("a prompt without numbered rules offers \"rule N\":\n%s", system)
	}
}
