// ClawEh
// License: MIT

package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// reportTools lists one item per source tool: a_list "fail-item", b_list
// "ok-item".
type reportTools struct{}

func (reportTools) Has(string, string) error { return nil }

func (reportTools) Call(_ context.Context, _, tool string, _ map[string]any) (string, error) {
	switch tool {
	case "a_list":
		return `[{"id":"fail-item"}]`, nil
	case "b_list":
		return `[{"id":"ok-item"}]`, nil
	}
	return "", fmt.Errorf("no tool %s", tool)
}

// reportModel fails for the failing item with a long error, and answers
// "urgent" otherwise.
type reportModel struct{}

var errModelUnavailable = errors.New(strings.Repeat("the model is unavailable ", 12))

func (reportModel) Complete(_ context.Context, _, _, user string) (string, string, error) {
	if strings.Contains(user, "fail-item") {
		return "", "m", fmt.Errorf("%w: %w", errModelUnavailable, classifier.ErrNoModel)
	}
	return "urgent", "m", nil
}

type quietLogger struct{}

func (quietLogger) Debugf(string, ...any) {}
func (quietLogger) Infof(string, ...any)  {}
func (quietLogger) Warnf(string, ...any)  {}
func (quietLogger) Errorf(string, ...any) {}

// The Classifiers table shows each classifier's last run: its time, status,
// dry run and counts; a run that ended in an error is marked and its error
// cut to 200 characters.
func TestCollectAgents_ClassifiersLastRun(t *testing.T) {
	cfg, env := fixtureConfig(t)
	bob := &cfg.Agents.List[1]
	bob.Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "GPT"}
	failing := classifierDef("failing", "bob")
	failing["dry_run"] = false
	failing["source"] = map[string]any{"tool": "a_list", "items": "", "id": "id"}
	working := classifierDef("working", "bob")
	working["source"] = map[string]any{"tool": "b_list", "items": "", "id": "id"}
	writeClassifier(t, cfg, "bob", "failing", failing)
	writeClassifier(t, cfg, "bob", "working", working)

	base := classifiersDir(cfg, bob)
	svc := classifier.New(classifier.Host{
		Tools: reportTools{}, Model: reportModel{}, Logger: quietLogger{},
		Clock:    clock.NewFake(time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)),
		Ceilings: func() classifier.Ceilings { return classifierCeilings(cfg) },
		Scopes:   func() []classifier.Scope { return []classifier.Scope{{AgentID: "bob", BaseDirectory: base}} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		sums, err := classifier.Inspect(ctx, base, classifierCeilings(cfg))
		if err == nil && len(sums) == 2 && sums[0].LastRun != nil && sums[1].LastRun != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs not recorded: %+v, %v", sums, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	tb := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	rows := map[string][]string{}
	marked := map[string]bool{}
	for i, r := range tb.Rows {
		rows[r[0]] = r
		marked[r[0]] = isHighlighted(tb, i)
	}
	f := rows["failing"]
	if f == nil || !strings.HasSuffix(f[4], ", error") || f[5] != "new 1, acted 0, unclassified 0, failed 1" || !marked["failing"] {
		t.Errorf("failing row = %v marked=%v", f, marked["failing"])
	}
	if f != nil && (len([]rune(f[6])) != maxClassifierErrorChars+1 || !strings.HasSuffix(f[6], "…")) {
		t.Errorf("failing error = %q (%d runes), want cut to %d plus the mark", f[6], len([]rune(f[6])), maxClassifierErrorChars)
	}
	w := rows["working"]
	if w == nil || !strings.HasSuffix(w[4], ", ok (dry run)") || w[5] != "new 1, acted 0, unclassified 0, failed 0" || w[6] != "" || marked["working"] {
		t.Errorf("working row = %v marked=%v", w, marked["working"])
	}
}

// A classifiers folder that cannot be read is one marked row saying so.
func TestCollectAgents_ClassifiersUnreadable(t *testing.T) {
	cfg, env := fixtureConfig(t)
	bob := &cfg.Agents.List[1]
	dir := classifiersDir(cfg, bob)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil { // a file where the folder belongs
		t.Fatal(err)
	}
	tb := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	if len(tb.Rows) != 1 || !strings.HasPrefix(tb.Rows[0][6], "unreadable: ") || !isHighlighted(tb, 0) {
		t.Errorf("unreadable folder rows = %v highlight=%v", tb.Rows, tb.Highlight)
	}
	if strings.Contains(tb.Rows[0][6], classifierfs.StateFile) {
		t.Errorf("row names the state file: %v", tb.Rows[0])
	}
}

// A state.db that cannot be opened is a marked first row of the Classifiers
// table and an assessment row; none is shown while it opens (or does not
// exist yet).
func TestCollectAgents_ClassifierStateUnavailable(t *testing.T) {
	cfg, env := fixtureConfig(t)
	bob := &cfg.Agents.List[1]
	bob.Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "GPT"}
	writeClassifier(t, cfg, "bob", "inbox", classifierDef("inbox", "bob"))
	tb := findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	for _, r := range tb.Rows {
		if r[0] == classifierfs.StateFile {
			t.Fatalf("a state row with no state.db: %v", r)
		}
	}
	if err := os.Mkdir(filepath.Join(classifiersDir(cfg, bob), classifierfs.StateFile), 0o700); err != nil {
		t.Fatal(err)
	}
	tb = findTable(t, agentSub(t, collectAgents(t.Context(), cfg, env), "bob"), "Classifiers")
	if len(tb.Rows) != 2 || tb.Rows[0][0] != classifierfs.StateFile || tb.Rows[0][1] != "not run" ||
		!strings.HasPrefix(tb.Rows[0][6], "Cannot be opened: ") || !isHighlighted(tb, 0) {
		t.Errorf("rows = %v highlight=%v, want a marked state.db row first", tb.Rows, tb.Highlight)
	}
	r := assessmentRow(t, collectAssessment(t.Context(), cfg, env), "Classifier state (Bob)")
	if r[0] != actionMark || !strings.HasPrefix(r[2], "Cannot be opened, so no classifier of this agent runs: ") {
		t.Errorf("assessment row = %v", r)
	}
}

// The switch reads on, off, or on but ignored for an agent that represents a
// person.
func TestClassifiersSwitch(t *testing.T) {
	on := &config.AgentConfig{ID: "bob", Classifiers: &config.AgentClassifiersConfig{Enabled: true}}
	off := &config.AgentConfig{ID: "bob"}
	for _, tc := range []struct {
		a     *config.AgentConfig
		human bool
		want  string
	}{
		{on, false, "on"},
		{on, true, "on (ignored: the agent represents a person)"},
		{off, true, "off"},
		{off, false, "off"},
	} {
		if got := classifiersSwitch(tc.a, tc.human); got != tc.want {
			t.Errorf("classifiersSwitch(enabled=%v, human=%v) = %q, want %q", tc.a.ClassifiersEnabled(), tc.human, got, tc.want)
		}
	}
	if got := truncateText("short", 10); got != "short" {
		t.Errorf("truncateText kept = %q", got)
	}
	if got := truncateText("ééééé", 3); got != "ééé…" {
		t.Errorf("truncateText cut = %q", got)
	}
}
