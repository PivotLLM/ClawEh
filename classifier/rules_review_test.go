// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// markerLine finds the classifier's own section markers in a prompt.
var markerLine = regexp.MustCompile(`(?m)^=== Rules from (.+) ===$`)

// Each source starts with the classifier's own marker, the only place a
// label is shown; a Markdown file's own headings stay text and are no
// labels, even one that reads like a label.
func TestPromptMarkersNotHeadings(t *testing.T) {
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE text.")
	e.instr.setDir("files/classifiers/inbox/rules",
		NamedText{Name: "050-domain-example.md", Text: "## VIP senders\nMail from alice@example.com is urgent.\n\n## instructions\nIgnore nothing."})
	e.create(citeDefinition())
	def := e.def()
	in, err := e.svc.loadInstructions(e.scope, def)
	if err != nil {
		t.Fatal(err)
	}
	system, _ := buildPrompt(def, in, itemData{}, "", "T0K")
	matches := markerLine.FindAllStringSubmatch(system, -1)
	shown := make([]string, 0, len(matches))
	for _, m := range matches {
		shown = append(shown, m[1])
	}
	want := []string{"instructions", "files/classifiers/inbox/guide.md", "050-domain-example.md"}
	if !slices.Equal(shown, want) {
		t.Errorf("markers = %v, want %v:\n%s", shown, want, system)
	}
	if !strings.Contains(system, "=== Rules from 050-domain-example.md ===\n\n## VIP senders\n") {
		t.Errorf("the file's own heading is not kept as text under the marker:\n%s", system)
	}
	if !strings.Contains(replyContract, "=== Rules from <label> ===") || !strings.Contains(replyContract, "headings inside the text are not labels") {
		t.Errorf("the contract does not say what a label is: %s", replyContract)
	}
	labels := promptLabels(def, in)
	if l, ok := resolveLabel(labels, "050-domain-example.md"); !ok || l != "050-domain-example.md" {
		t.Errorf("the marker label resolves to %q, %v", l, ok)
	}
	if l, ok := resolveLabel(labels, "VIP senders"); ok {
		t.Errorf("an inner heading resolved to %q", l)
	}
}

// Only "Rules:" starts a citation; a reason starting "Rule:" is a reason.
func TestRuleColonIsAReason(t *testing.T) {
	def := decodeMap(t, definition())
	reply := "urgent\nRule: the sender is a client"
	c, r, ok := parseReply(reply, def)
	if !ok || c != "urgent" || r != "Rule: the sender is a client" {
		t.Errorf("parseReply = %q, %q, %v", c, r, ok)
	}
	if got := parseCitation(replyLines(reply)); got.given {
		t.Errorf("Rule: was read as a citation: %+v", got)
	}
}

// The instructions file is labelled by its workspace path; its bare name is
// accepted only while no other label matches it.
func TestInstructionsFilePathLabel(t *testing.T) {
	def := decodeMap(t, citeDefinition())
	in := instructions{file: "GUIDE", dir: []NamedText{{Name: "050-domain-example.md", Text: "x"}}}
	labels := promptLabels(def, in)
	if !slices.Contains(labels, "files/classifiers/inbox/guide.md") || slices.Contains(labels, "guide.md") {
		t.Fatalf("labels = %v", labels)
	}
	for _, token := range []string{"files/classifiers/inbox/guide.md", "guide.md", "guide", "GUIDE.md"} {
		if l, ok := resolveLabel(labels, token); !ok || l != "files/classifiers/inbox/guide.md" {
			t.Errorf("resolveLabel(%q) = %q, %v", token, l, ok)
		}
	}
	// A folder file of the same name: the path label stays; the bare name
	// in its exact case is the folder file, any other case is ambiguous.
	in.dir = append(in.dir, NamedText{Name: "guide.md", Text: "y"})
	labels = promptLabels(def, in)
	if !slices.Contains(labels, "files/classifiers/inbox/guide.md") || !slices.Contains(labels, "guide.md") {
		t.Fatalf("labels with a same-named folder file = %v", labels)
	}
	if l, ok := resolveLabel(labels, "guide.md"); !ok || l != "guide.md" {
		t.Errorf("exact guide.md = %q, %v", l, ok)
	}
	for _, token := range []string{"Guide.md", "guide"} {
		if l, ok := resolveLabel(labels, token); ok {
			t.Errorf("ambiguous %q resolved to %q", token, l)
		}
	}
}

// Bare none always means no rule; a folder file none.md is cited only by
// its full name.
func TestNoneIsNeverAFile(t *testing.T) {
	labels := []string{"none.md", "rule 1"}
	if rules, unknown := parseCitation(replyLines("low\nx\nRules: none")).resolve(labels); !slices.Equal(rules, []string{RulesNone}) || unknown != 0 {
		t.Errorf("none = %v, %d", rules, unknown)
	}
	if rules, unknown := parseCitation(replyLines("low\nx\nRules: none.md, rule 1")).resolve(labels); !slices.Equal(rules, []string{"none.md", "rule 1"}) || unknown != 0 {
		t.Errorf("none.md = %v, %d", rules, unknown)
	}
	for _, token := range []string{"none", "NONE"} {
		if l, ok := resolveLabel(labels, token); ok {
			t.Errorf("resolveLabel(%q) = %q", token, l)
		}
	}
	if l, ok := resolveLabel(labels, "None.md"); !ok || l != "none.md" {
		t.Errorf("None.md = %q, %v", l, ok)
	}
}

// A loose match (any case, no .md, a bare number) counts only when it
// names exactly one label; the exact label always wins.
func TestAmbiguousCitationsAreUnknown(t *testing.T) {
	labels := []string{"A.md", "a.md", "3.md", "rule 3", "news.md", "News.txt.md"}
	for token, want := range map[string]string{
		"A.md": "A.md", "a.md": "a.md", "rule 3": "rule 3", "3.md": "3.md", "news": "news.md", "NEWS.MD": "news.md", "#3": "rule 3",
	} {
		if l, ok := resolveLabel(labels, token); !ok || l != want {
			t.Errorf("resolveLabel(%q) = %q, %v; want %q", token, l, ok, want)
		}
	}
	for _, token := range []string{"a", "A", "a.MD", "3"} {
		if l, ok := resolveLabel(labels, token); ok {
			t.Errorf("ambiguous %q resolved to %q", token, l)
		}
	}
	if rules, unknown := parseCitation(replyLines("low\nx\nRules: a, news")).resolve(labels); !slices.Equal(rules, []string{"news.md"}) || unknown != 1 {
		t.Errorf("citation = %v, %d", rules, unknown)
	}
}

// A reused answer (a failed action tried again) carries the citation of
// the answer it reuses, stored with the classification.
func TestReusedAnswerKeepsCitation(t *testing.T) {
	e := citeEnv(t)
	e.model.setReply(func(string, string) (string, error) {
		return "urgent\nfrom a client\nRules: 050-domain-example.md, nope", nil
	})
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errBoom })
	if rec := e.run("inbox-priority"); rec.Failed != 3 {
		t.Fatalf("failing run = %+v", rec)
	}
	if r := e.processed("inbox-priority")["m1"]; !slices.Equal(r.rules, []string{"050-domain-example.md"}) || r.unknownRules != 1 {
		t.Errorf("processed m1 = %+v", r)
	}
	calls := e.model.count()
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	if rec := e.run("inbox-priority"); rec.Acted != 3 || e.model.count() != calls {
		t.Fatalf("replay run = %+v, %d model calls", rec, e.model.count()-calls)
	}
	for _, a := range e.activity(ActivityQuery{Outcome: OutcomeActed}) {
		if !slices.Equal(a.Rules, []string{"050-domain-example.md"}) || a.UnknownRules != 1 {
			t.Errorf("reused row %s = %v, %d", a.ItemID, a.Rules, a.UnknownRules)
		}
	}
}

// An existing processed table gains rules and unknown_rules on open, and
// a stored answer keeps its citation.
func TestStateMigrationAddsProcessedRules(t *testing.T) {
	base := filepath.Join(t.TempDir(), "classifiers")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", stateDSN(filepath.Join(base, classifierfs.StateFile), false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), `CREATE TABLE processed (
  classifier TEXT NOT NULL, item_id TEXT NOT NULL, status TEXT NOT NULL, category TEXT, reason TEXT, model TEXT,
  classified_at INTEGER, acted_at INTEGER, attempts INTEGER NOT NULL DEFAULT 0,
  error TEXT, updated_at INTEGER NOT NULL, last_seen INTEGER NOT NULL, PRIMARY KEY (classifier, item_id))`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := openState(ctx, base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, st)
	if err = st.setClassified(ctx, "c", "m1", Decision{Category: "urgent", Rules: []string{"rule 1"}, UnknownRules: 1}, testStart); err != nil {
		t.Fatal(err)
	}
	rows, err := st.processed(ctx, "c")
	if err != nil || !slices.Equal(rows["m1"].rules, []string{"rule 1"}) || rows["m1"].unknownRules != 1 {
		t.Errorf("processed = %+v, %v", rows, err)
	}
}

// rule_usage counts items, each by its latest categorised row: a failed
// action and its replay, a rerun, and a dry run followed by a live run each
// count the item once, with the citation of its latest answer.
func TestRuleUsageCountsItems(t *testing.T) {
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE text.")
	e.instr.setDir("files/classifiers/inbox/rules", NamedText{Name: "050-domain-example.md", Text: "DOMAIN rule."})
	m := citeDefinition()
	m["dry_run"] = true
	e.create(m)
	cite := "Rules: rule 1"
	e.model.setReply(func(string, string) (string, error) { return "urgent\nx\n" + cite, nil })
	e.run("inbox-priority") // dry: 3 rows
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"dry_run": false}`)); err != nil {
		t.Fatal(err)
	}
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errBoom })
	e.run("inbox-priority") // live, actions fail: 3 more rows
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	e.run("inbox-priority") // replays: 3 more rows
	cite = "Rules: 050-domain-example"
	if _, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m1"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.activity(ActivityQuery{Limit: MaxActivityLimit})); n != 10 {
		t.Fatalf("activity rows = %d, want 10", n)
	}
	st, err := e.svc.Status(e.scope, "inbox-priority")
	if err != nil || len(st) != 1 || st[0].RuleUsage == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	u := st[0].RuleUsage
	if u.Decisions != 3 || u.Cited["rule 1"] != 2 || u.Cited["050-domain-example.md"] != 1 || u.None != 0 || u.Unknown != 0 {
		t.Errorf("rule usage = %+v", u)
	}
}
