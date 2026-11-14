// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// citeDefinition is the inbox classifier with every kind of rule source:
// inline instructions, an instructions file, a rules folder and two inline
// rules.
func citeDefinition() map[string]any {
	m := definition()
	c := obj(m["classify"])
	c["instructions_file"] = "files/classifiers/inbox/guide.md"
	c["instructions_dir"] = "files/classifiers/inbox/rules"
	c["rules"] = []any{"Email from bob@example.com is urgent.", "Newsletters are low."}
	return m
}

// citeEnv is an env with citeDefinition created and its Markdown in place.
func citeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE text.")
	e.instr.setDir("files/classifiers/inbox/rules",
		NamedText{Name: "050-domain-example.md", Text: "DOMAIN rule."},
		NamedText{Name: "060-news.md", Text: "NEWS rule."})
	e.create(citeDefinition())
	return e
}

// Every source of rules is in the prompt under its citable label: inline
// instructions, the instructions file and each folder file under a heading,
// the inline rules numbered; the contract says how to cite them.
func TestPromptLabelsAllKinds(t *testing.T) {
	e := citeEnv(t)
	def := e.def()
	in, err := e.svc.loadInstructions(e.scope, def)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"instructions", "guide.md", "050-domain-example.md", "060-news.md", "rule 1", "rule 2"}
	if got := promptLabels(def, in); !slices.Equal(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	system, _ := buildPrompt(def, in, itemData{}, "", "T0K")
	wantPrefix := "## instructions\n\nDecide how important this email is to the user.\n\n" +
		"## guide.md\n\nGUIDE text.\n\n" +
		"## 050-domain-example.md\n\nDOMAIN rule.\n\n" +
		"## 060-news.md\n\nNEWS rule.\n\n" +
		"Rules:\n1. Email from bob@example.com is urgent.\n2. Newsletters are low.\n\nCategories:\n"
	if !strings.HasPrefix(system, wantPrefix) {
		t.Errorf("system prompt:\n%s\nwant it to start with:\n%s", system, wantPrefix)
	}
	if !strings.Contains(system, replyContract) || !strings.Contains(replyContract, `"Rules: none"`) {
		t.Errorf("the contract does not ask for the Rules line:\n%s", system)
	}

	// The instructions file is labelled by its path when a folder file has
	// its name.
	in.dir = append(in.dir, NamedText{Name: "GUIDE.md", Text: "x"})
	if got := promptLabels(def, in); !slices.Contains(got, "files/classifiers/inbox/guide.md") || !slices.Contains(got, "GUIDE.md") {
		t.Errorf("labels with a name clash = %v", got)
	}
}

// Excluded labels leave their text out of the prompt; the inline rules keep
// their numbers, so labels stay stable.
func TestPromptExclusion(t *testing.T) {
	e := citeEnv(t)
	def := e.def()
	in, err := e.svc.loadInstructions(e.scope, def)
	if err != nil {
		t.Fatal(err)
	}
	in.exclude = map[string]bool{"050-domain-example.md": true, "rule 1": true, "instructions": true}
	system, _ := buildPrompt(def, in, itemData{}, "", "T0K")
	for _, gone := range []string{"DOMAIN rule.", "050-domain-example.md", "bob@example.com is urgent", "Decide how important"} {
		if strings.Contains(system, gone) {
			t.Errorf("excluded text %q is in the prompt:\n%s", gone, system)
		}
	}
	if !strings.Contains(system, "Rules:\n2. Newsletters are low.\n") || !strings.Contains(system, "## 060-news.md") {
		t.Errorf("the rest is not as defined:\n%s", system)
	}
	if got := promptLabels(def, in); !slices.Equal(got, []string{"guide.md", "060-news.md", "rule 2"}) {
		t.Errorf("labels = %v", got)
	}
}

// Old replies still parse; a Rules line in the second or third place is
// read in its variants, never taken as the reason, and never makes a reply
// unreadable.
func TestParseReplyWithRules(t *testing.T) {
	def := decodeMap(t, definition())
	labels := []string{"instructions", "guide.md", "050-domain-example.md", "rule 1", "rule 2"}
	for _, tc := range []struct {
		reply, category, reason string
		rules                   []string
		unknown                 int
	}{
		{"urgent", "urgent", "", nil, 0},
		{"urgent\nfrom a client", "urgent", "from a client", nil, 0},
		{"urgent: from a client", "urgent", "from a client", nil, 0},
		{"urgent\nfrom a client\nRules: rule 1", "urgent", "from a client", []string{"rule 1"}, 0},
		{"urgent\nRules: rule 1", "urgent", "", []string{"rule 1"}, 0},
		{"urgent: from a client\nRules: 050-domain-example.md", "urgent", "from a client", []string{"050-domain-example.md"}, 0},
		{"urgent — from a client\nrules: none", "urgent", "from a client", []string{RulesNone}, 0},
		{"low\nnot from a client\nRULES: None.", "low", "not from a client", []string{RulesNone}, 0},
		{"urgent\nx\n**Rules:** `050-domain-example`, \"rule #2\", [rule 1].", "urgent", "x", []string{"050-domain-example.md", "rule 2", "rule 1"}, 0},
		{"urgent\nx\nRules: [2, 1, rule 2, #1]", "urgent", "x", []string{"rule 2", "rule 1"}, 0},
		{"urgent\nx\nRule: GUIDE.MD; instructions", "urgent", "x", []string{"guide.md", "instructions"}, 0},
		{"urgent\nx\nRules: rule 9, nope.md, rule 1", "urgent", "x", []string{"rule 1"}, 2},
		{"urgent\nx\nRules: nope", "urgent", "x", nil, 1},
		{"urgent\nx\nRules: none, rule 1", "urgent", "x", []string{"rule 1"}, 0},
		{"urgent\nx\nRules:", "urgent", "x", nil, 0},                   // lists nothing: ignored
		{"urgent\nx\nRules: , ;", "urgent", "x", nil, 0},               // lists nothing: ignored
		{"urgent\nx\ny\nRules: rule 1", "urgent", "x", nil, 0},         // fourth line: not read
		{"urgent\nRule 1 says so", "urgent", "Rule 1 says so", nil, 0}, // no colon: a reason
		{"```\nurgent\nfrom bob\nRules: rule 1\n```", "urgent", "from bob", []string{"rule 1"}, 0},
	} {
		c, r, ok := parseReply(tc.reply, def)
		if !ok || c != tc.category || r != tc.reason {
			t.Errorf("parseReply(%q) = %q, %q, %v; want %q, %q", tc.reply, c, r, ok, tc.category, tc.reason)
		}
		rules, unknown := parseCitation(replyLines(tc.reply)).resolve(labels)
		if !slices.Equal(rules, tc.rules) || unknown != tc.unknown {
			t.Errorf("citation of %q = %v, %d; want %v, %d", tc.reply, rules, unknown, tc.rules, tc.unknown)
		}
	}
	// A Rules line in place of the category is still no category.
	if _, _, ok := parseReply("Rules: rule 1\nurgent", def); ok {
		t.Error("a reply starting with the Rules line was read")
	}
}

// The accepted ways of naming a label: exact, any case, a file name
// without .md, and an inline rule by number; a file name wins over a
// number.
func TestResolveLabel(t *testing.T) {
	labels := []string{"instructions", "050-domain-example.md", "3.md", "A.md", "a.md", "rule 2", "rule 3"}
	for token, want := range map[string]string{
		"instructions":          "instructions",
		"INSTRUCTIONS":          "instructions",
		"050-domain-example.md": "050-domain-example.md",
		"050-Domain-Example.MD": "050-domain-example.md",
		"050-domain-example":    "050-domain-example.md",
		"rule 2":                "rule 2",
		"Rule  #2":              "rule 2",
		"rules 2":               "rule 2",
		"#2":                    "rule 2",
		"2":                     "rule 2",
		"02":                    "rule 2",
		"3":                     "3.md",
		"rule 3":                "rule 3",
		"a.md":                  "a.md",
		"A.md":                  "A.md",
		"a":                     "A.md",
	} {
		if got, ok := resolveLabel(labels, token); !ok || got != want {
			t.Errorf("resolveLabel(%q) = %q, %v; want %q", token, got, ok, want)
		}
	}
	for _, token := range []string{"", "rule 1", "1", "none", "domain-example", "rule", "#", "99999999999999999999"} {
		if got, ok := resolveLabel(labels, token); ok {
			t.Errorf("resolveLabel(%q) = %q, want no match", token, got)
		}
	}
}

// A run stores each decision's cited rules on its activity row, known
// labels only, in the model's order, unknown ones counted; an unreadable
// Rules line neither retries nor leaves the item unclassified.
func TestRunStoresCitations(t *testing.T) {
	e := citeEnv(t)
	e.model.setReply(func(_, user string) (string, error) {
		switch {
		case strings.Contains(user, "About m1\n"):
			return "urgent\nfrom a client\nRules: 050-domain-example, rule 1, rule 1, gone.md", nil
		case strings.Contains(user, "About m2\n"):
			return "low\na newsletter\nRules: none", nil
		default:
			return "urgent\nfrom a client\nRules: ???", nil
		}
	})
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Classified != 3 || rec.Unclassified != 0 {
		t.Fatalf("run = %+v", rec)
	}
	if n := e.model.count(); n != 3 {
		t.Errorf("model calls = %d, want 3 (no retry)", n)
	}
	got := map[string]ActivityEntry{}
	for _, a := range e.activity(ActivityQuery{}) {
		got[a.ItemID] = a
	}
	if a := got["m1"]; !slices.Equal(a.Rules, []string{"050-domain-example.md", "rule 1"}) || a.UnknownRules != 1 {
		t.Errorf("m1 = %v, %d", a.Rules, a.UnknownRules)
	}
	if a := got["m2"]; !slices.Equal(a.Rules, []string{RulesNone}) || a.UnknownRules != 0 {
		t.Errorf("m2 = %v, %d", a.Rules, a.UnknownRules)
	}
	if a := got["m3"]; a.Rules != nil || a.UnknownRules != 0 || a.Category != "urgent" { // "???" lists nothing: ignored
		t.Errorf("m3 = %+v", a)
	}
}

// writeCitelessStateDB writes a state.db whose activity table has every
// column but rules and unknown_rules, with one row.
func writeCitelessStateDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE activity (
  id INTEGER PRIMARY KEY, classifier TEXT NOT NULL, at INTEGER NOT NULL, item_id TEXT,
  fields TEXT NOT NULL,
  category TEXT, reason TEXT, model TEXT, action TEXT, outcome TEXT NOT NULL, error TEXT,
  dry_run INTEGER NOT NULL, rerun INTEGER NOT NULL, action_args TEXT, count INTEGER, last_at INTEGER,
  replies TEXT, missing_fields TEXT, shortened_fields TEXT, detail TEXT)`,
		`INSERT INTO activity (classifier, at, item_id, fields, category, outcome, dry_run, rerun) VALUES ('c', 1, 'old', '{}', 'low', 'recorded', 0, 0)`,
	} {
		if _, err = db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

// An existing state.db gains the rules and unknown_rules columns on open;
// its rows read with no citation and new rows store theirs.
func TestStateMigrationAddsRules(t *testing.T) {
	base := filepath.Join(t.TempDir(), "classifiers")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCitelessStateDB(t, filepath.Join(base, classifierfs.StateFile))
	ctx := context.Background()
	st, err := openState(ctx, base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, st)
	var typ string
	if err = st.db.QueryRowContext(ctx, `SELECT type FROM pragma_table_info('activity') WHERE name = 'unknown_rules'`).Scan(&typ); err != nil || typ != "INTEGER" {
		t.Fatalf("unknown_rules column = %q, %v", typ, err)
	}
	e := ActivityEntry{
		ItemID: "new", Category: "urgent", Outcome: OutcomeRecorded, At: time.UnixMilli(2).UTC(),
		Rules: []string{"rule 1"}, UnknownRules: 2,
	}
	if err = st.addActivity(ctx, "c", e); err != nil {
		t.Fatal(err)
	}
	got, err := st.activity(ctx, "c", ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(got[0].Rules, []string{"rule 1"}) || got[0].UnknownRules != 2 || got[1].Rules != nil || got[1].UnknownRules != 0 {
		t.Errorf("activity = %+v", got)
	}
	var stored string
	if err = st.db.QueryRowContext(ctx, `SELECT rules FROM activity WHERE item_id = 'new'`).Scan(&stored); err != nil || stored != `["rule 1"]` {
		t.Errorf("stored rules = %q, %v", stored, err)
	}
}

// citeRun runs citeDefinition over m1..m3 with fixed citations: m1 the
// domain file and rule 1, m2 none, m3 a file since removed.
func citeRun(t *testing.T) *env {
	t.Helper()
	e := citeEnv(t)
	e.model.setReply(func(_, user string) (string, error) {
		switch {
		case strings.Contains(user, "About m1\n"):
			return "urgent\nfrom a client\nRules: 050-domain-example.md, rule 1", nil
		case strings.Contains(user, "About m2\n"):
			return "low\na newsletter\nRules: none", nil
		default:
			return "urgent\nfrom a client\nRules: 070-old.md, mystery", nil
		}
	})
	e.instr.setDir("files/classifiers/inbox/rules",
		NamedText{Name: "050-domain-example.md", Text: "DOMAIN rule."},
		NamedText{Name: "060-news.md", Text: "NEWS rule."},
		NamedText{Name: "070-old.md", Text: "OLD rule."})
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Classified != 3 {
		t.Fatalf("run = %+v", rec)
	}
	// 070-old.md is removed after the run.
	e.instr.setDir("files/classifiers/inbox/rules",
		NamedText{Name: "050-domain-example.md", Text: "DOMAIN rule."},
		NamedText{Name: "060-news.md", Text: "NEWS rule."})
	return e
}

// classifier_activity's rule keeps the rows citing that rule, in any
// accepted form, none the rows citing no rule; a removed file cited in the
// log is still found; an unknown label is refused.
func TestActivityRuleFilter(t *testing.T) {
	e := citeRun(t)
	ids := func(q ActivityQuery) []string {
		entries := e.activity(q)
		out := make([]string, 0, len(entries))
		for _, a := range entries {
			out = append(out, a.ItemID)
		}
		slices.Sort(out)
		return out
	}
	for rule, want := range map[string][]string{
		"050-domain-example":    {"m1"},
		"050-domain-example.md": {"m1"},
		"#1":                    {"m1"},
		"Rule 1":                {"m1"},
		"none":                  {"m2"},
		"NONE":                  {"m2"},
		"070-old":               {"m3"},
		"060-news.md":           nil,
		"rule 2":                nil,
	} {
		if got := ids(ActivityQuery{Rule: rule}); !slices.Equal(got, want) {
			t.Errorf("rule %q = %v, want %v", rule, got, want)
		}
	}
	for _, rule := range []string{"nope", "rule 9", "mystery"} {
		_, err := e.svc.Activity(e.scope, "inbox-priority", ActivityQuery{Rule: rule})
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "is not one of this classifier's rules") {
			t.Errorf("rule %q: err = %v, want a refusal", rule, err)
		}
	}
}

// classifier_status counts, over the activity window, each current rule's
// citations, the rules never cited, the decisions citing none or an unknown
// label, and cited labels whose file is gone.
func TestStatusRuleUsage(t *testing.T) {
	e := citeRun(t)
	st, err := e.svc.Status(e.scope, "inbox-priority")
	if err != nil || len(st) != 1 || st[0].RuleUsage == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	u := st[0].RuleUsage
	want := RuleUsage{
		Hours: 72, Decisions: 3,
		Cited:           map[string]int{"050-domain-example.md": 1, "rule 1": 1},
		NeverCited:      []string{"instructions", "guide.md", "060-news.md", "rule 2"},
		None:            1,
		Unknown:         1,
		NoLongerPresent: map[string]int{"070-old.md": 1},
	}
	if got, want := mustJSON(t, u), mustJSON(t, want); string(got) != string(want) {
		t.Errorf("rule usage = %s\nwant %s", got, want)
	}

	// Outside the window nothing counts.
	e.clock.Advance(73 * time.Hour)
	st, err = e.svc.Status(e.scope, "")
	if err != nil || len(st) != 1 || st[0].RuleUsage == nil || st[0].RuleUsage.Decisions != 0 || st[0].RuleUsage.Cited != nil {
		t.Errorf("status after the window = %+v, %v", st, err)
	}

	// Markdown that cannot be read gives no usage.
	e.instr.setErr("files/classifiers/inbox/guide.md", errBoom)
	if st, err = e.svc.Status(e.scope, "inbox-priority"); err != nil || st[0].RuleUsage != nil {
		t.Errorf("status with unreadable Markdown = %+v, %v", st, err)
	}
}

// classifier_test with exclude_rules classifies the item twice, the second
// time without the excluded text, reports whether the category changed,
// and stores nothing; an unknown label is refused before any call.
func TestTestExcludeRules(t *testing.T) {
	e := citeEnv(t)
	e.model.setReply(func(system, _ string) (string, error) {
		if strings.Contains(system, "DOMAIN rule.") {
			return "urgent\nthe domain rule\nRules: 050-domain-example.md", nil
		}
		return "low\nno rule\nRules: none", nil
	})
	res, err := e.svc.TestExcluding(context.Background(), e.scope, "inbox-priority", "m1", []string{"050-domain-example"})
	if err != nil {
		t.Fatal(err)
	}
	if e.model.count() != 2 {
		t.Fatalf("model calls = %d, want 2", e.model.count())
	}
	if !strings.Contains(e.model.systems[0], "DOMAIN rule.") || strings.Contains(e.model.systems[1], "DOMAIN rule.") ||
		strings.Contains(e.model.systems[1], "050-domain-example.md") || !strings.Contains(e.model.systems[1], "NEWS rule.") {
		t.Errorf("prompts:\n%s\n---\n%s", e.model.systems[0], e.model.systems[1])
	}
	if len(e.tools.callsOf("mail_read")) != 1 {
		t.Errorf("the item was fetched %d times, want once", len(e.tools.callsOf("mail_read")))
	}
	if res.Category != "urgent" || !slices.Equal(res.Rules, []string{"050-domain-example.md"}) ||
		res.WithoutRules == nil || res.WithoutRules.Category != "low" || !slices.Equal(res.WithoutRules.Rules, []string{RulesNone}) ||
		res.WithoutRules.Action != "mail_move" || res.WithoutRules.ActionArgs == "" ||
		res.Changed == nil || !*res.Changed || !slices.Equal(res.ExcludeRules, []string{"050-domain-example.md"}) {
		t.Errorf("result = %+v, without %+v", res, res.WithoutRules)
	}
	if !strings.Contains(res.SystemPrompt, "DOMAIN rule.") {
		t.Error("the system prompt shown is not the full one")
	}
	if len(e.processed("inbox-priority")) != 0 || len(e.activity(ActivityQuery{})) != 0 || len(e.tools.callsOf("mail_move")) != 0 {
		t.Error("the test stored something or acted")
	}

	// Excluding a rule that did not decide: the category is unchanged.
	res, err = e.svc.TestExcluding(context.Background(), e.scope, "inbox-priority", "m1", []string{"rule 2"})
	if err != nil || res.Changed == nil || *res.Changed || res.WithoutRules.Category != "urgent" {
		t.Errorf("unchanged result = %+v, %v", res, err)
	}

	// An unknown label (none is not a rule) is refused before any call.
	calls := e.model.count()
	for _, bad := range []string{"nope.md", "none", "rule 3"} {
		_, err = e.svc.TestExcluding(context.Background(), e.scope, "inbox-priority", "m1", []string{"rule 1", bad})
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "exclude_rules entry") || !strings.Contains(err.Error(), bad) {
			t.Errorf("exclude %q: err = %v", bad, err)
		}
	}
	if e.model.count() != calls {
		t.Error("a refused test called the model")
	}

	// Without exclude_rules there is one call and no second decision.
	res, err = e.svc.Test(context.Background(), e.scope, "inbox-priority", "m1")
	if err != nil || res.WithoutRules != nil || res.Changed != nil || e.model.count() != calls+1 {
		t.Errorf("plain test = %+v, %v", res, err)
	}
}

// A first decision that fails (the fetch) is not classified again.
func TestTestExcludeRulesFetchFailed(t *testing.T) {
	e := citeEnv(t)
	e.tools.set("mail_read", func(map[string]any) (string, error) { return "", errBoom })
	res, err := e.svc.TestExcluding(context.Background(), e.scope, "inbox-priority", "m1", []string{"rule 1"})
	if err != nil || res.Outcome != OutcomeFailed || res.WithoutRules != nil || res.Changed != nil || e.model.count() != 0 {
		t.Errorf("result = %+v, %v; %d model calls", res, err, e.model.count())
	}
}

// The tools pass rule and exclude_rules through, refuse bad values in one
// sentence, and show citations inside the untrusted wrapping.
func TestRuleTools(t *testing.T) {
	e := citeRun(t)
	defs := Tools(e.svc, fakeToolHost{scope: e.scope})
	wrapped := func(res string) bool {
		return strings.HasPrefix(res, "<<untrusted>>") && strings.HasSuffix(res, "<</untrusted>>")
	}
	res := call(t, defs, "activity", map[string]any{"id": "inbox-priority", "rule": "050-domain-example"})
	if res.IsError || !wrapped(res.ForLLM) || !strings.Contains(res.ForLLM, `"item_id": "m1"`) || strings.Contains(res.ForLLM, `"item_id": "m2"`) ||
		!strings.Contains(res.ForLLM, `"rules": [`) {
		t.Errorf("activity by rule = %s", res.ForLLM)
	}
	for want, args := range map[string]map[string]any{
		"rule must name a rule, or none.": {"id": "inbox-priority", "rule": " "},
		`rule "nope" is not one of this classifier's rules; use a rule's file name, rule N or instructions.`: {"id": "inbox-priority", "rule": "nope"},
	} {
		if got := call(t, defs, "activity", args); !got.IsError || got.ForLLM != want {
			t.Errorf("activity %v = %s, want %s", args, got.ForLLM, want)
		}
	}
	res = call(t, defs, "status", map[string]any{"id": "inbox-priority"})
	if res.IsError || !wrapped(res.ForLLM) || !strings.Contains(res.ForLLM, `"rule_usage"`) || !strings.Contains(res.ForLLM, `"never_cited"`) {
		t.Errorf("status = %s", res.ForLLM)
	}
	res = call(t, defs, "test", map[string]any{"id": "inbox-priority", "item_id": "m1", "exclude_rules": []any{"rule 2"}})
	if res.IsError || !wrapped(res.ForLLM) || !strings.Contains(res.ForLLM, `"without_rules"`) || !strings.Contains(res.ForLLM, `"changed": false`) {
		t.Errorf("test with exclude_rules = %s", res.ForLLM)
	}
	for _, bad := range []any{"rule 1", []any{1.0}, []any{""}} {
		if res := call(t, defs, "test", map[string]any{"id": "inbox-priority", "exclude_rules": bad}); !res.IsError || res.ForLLM != "exclude_rules must be a list of strings." {
			t.Errorf("exclude_rules %v = %s", bad, res.ForLLM)
		}
	}
	if res := call(t, defs, "test", map[string]any{"id": "inbox-priority", "exclude_rules": []any{"nope"}}); !res.IsError || !strings.Contains(res.ForLLM, `exclude_rules entry "nope"`) {
		t.Errorf("unknown exclusion = %s", res.ForLLM)
	}
	if res := call(t, defs, "rerun", map[string]any{"id": "inbox-priority", "item_id": "m2"}); res.IsError || !wrapped(res.ForLLM) || !strings.Contains(res.ForLLM, `"none"`) {
		t.Errorf("rerun = %s", res.ForLLM)
	}
}
