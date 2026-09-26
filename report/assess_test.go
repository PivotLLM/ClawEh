// ClawEh
// License: MIT

package report

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAssess_MatchesCollect: the JSON rows are the PDF's assessment table,
// row for row, with Action standing in for the "*" mark.
func TestAssess_MatchesCollect(t *testing.T) {
	cfg, env := fixtureConfig(t)
	a := Assess(t.Context(), cfg, env)
	r := Collect(t.Context(), cfg, env)
	var tb Table
	for _, s := range r.Sections {
		if s.Title == "Security assessment" {
			tb = s.Tables[0]
		}
	}
	if len(tb.Rows) == 0 {
		t.Fatal("Collect has no assessment rows")
	}
	if len(a.Assessment) != len(tb.Rows) {
		t.Fatalf("Assess has %d rows, Collect has %d", len(a.Assessment), len(tb.Rows))
	}
	marked := 0
	for i, want := range tb.Rows {
		got := a.Assessment[i]
		if got.Item != want[1] || got.Status != want[2] || got.Action != (want[0] == actionMark) {
			t.Errorf("row %d = %+v, want %q", i, got, want)
		}
		if got.Action {
			marked++
		}
	}
	if marked == 0 {
		t.Error("the fixture (open telegram channel, exposed device gateway) should earn at least one action mark")
	}

	id := a.Identity
	if id.Name != "ClawEh" || id.Version != env.Version || id.Build != env.BuildTime || !id.GeneratedAt.Equal(env.Now) {
		t.Errorf("identity = %+v", id)
	}
	if id.Platform != "linux/amd64 on testbox" {
		t.Errorf("platform = %q", id.Platform)
	}
}

func TestAssess_NilConfigDoesNotPanic(t *testing.T) {
	a := Assess(t.Context(), nil, Environment{})
	if len(a.Assessment) == 0 || a.Identity.GeneratedAt.IsZero() || a.Identity.Version == "" {
		t.Errorf("nil config assessment: %+v", a)
	}
}

// TestAssess_JSONNoSecrets: the JSON the WebUI receives carries the same
// guarantee as the rendered report.
func TestAssess_JSONNoSecrets(t *testing.T) {
	cfg, env := fixtureConfig(t)
	b, err := json.Marshal(Assess(t.Context(), cfg, env))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range allSecrets {
		if strings.Contains(string(b), s) {
			t.Errorf("secret %q appears in the assessment JSON", s)
		}
	}
	for _, want := range []string{`"action":`, `"item":`, `"status":`, `"generated_at":`, `"platform":"linux/amd64 on testbox"`} {
		contains(t, string(b), want, "assessment JSON shape")
	}
}
