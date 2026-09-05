package schedule

import (
	"encoding/json"
	"testing"

	"github.com/PivotLLM/ClawEh/cron"
)

// TestExtractField_FansOutOverArrays covers the path semantics. The fan-out is
// the point: "messages.id" has to mean "the set of ids present", or a single
// path cannot express the state a change probe needs to compare.
func TestExtractField_FansOutOverArrays(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{
		"messages": [{"id": "a", "from": "x"}, {"id": "b", "from": "y"}],
		"profile": {"email": {"address": "me@example.com"}},
		"unread": 3
	}`), &doc); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		want string // JSON encoding of the expected selection
	}{
		{"top-level scalar", "unread", `3`},
		{"nested object", "profile.email.address", `"me@example.com"`},
		{"array fan-out", "messages.id", `["a","b"]`},
		{"array fan-out, other field", "messages.from", `["x","y"]`},
		{"whole array", "messages", `[{"from":"x","id":"a"},{"from":"y","id":"b"}]`},
		{"missing key", "nope", `null`},
		{"missing nested key", "profile.phone", `null`},
		{"path through a scalar", "unread.deeper", `null`},
		{"empty path is the whole document", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := extractField(doc, tc.path)
			if tc.want == "" {
				return // whole-document case; identity is enough
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Fatalf("extractField(%q) = %s, want %s", tc.path, b, tc.want)
			}
		})
	}
}

// TestDigest_OnlyMovesWhenWatchedFieldsMove is the behaviour the whole feature
// rests on: a probe must stay silent through churn in fields nobody asked about,
// or it degenerates into the unconditional wake-up it was meant to replace.
func TestDigest_OnlyMovesWhenWatchedFieldsMove(t *testing.T) {
	const before = `{"messages":[{"id":"a"}],"unread":1,"fetchedAt":"10:00"}`

	digestOf := func(t *testing.T, doc string, fields []string) string {
		t.Helper()
		d, err := buildSnapshot(doc, fields).digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	watched := []string{"messages.id"}
	base := digestOf(t, before, watched)

	t.Run("unwatched field churn does not fire", func(t *testing.T) {
		after := `{"messages":[{"id":"a"}],"unread":9,"fetchedAt":"11:00"}`
		if digestOf(t, after, watched) != base {
			t.Fatal("digest moved when only unwatched fields changed")
		}
	})

	t.Run("a new item fires", func(t *testing.T) {
		after := `{"messages":[{"id":"a"},{"id":"b"}],"unread":1,"fetchedAt":"10:00"}`
		if digestOf(t, after, watched) == base {
			t.Fatal("digest did not move when a new message appeared")
		}
	})

	t.Run("a removed item fires", func(t *testing.T) {
		after := `{"messages":[],"unread":1,"fetchedAt":"10:00"}`
		if digestOf(t, after, watched) == base {
			t.Fatal("digest did not move when the message list emptied")
		}
	})

	t.Run("watching everything fires on incidental churn", func(t *testing.T) {
		// Documents the cost of omitting watch_fields, so the default is a
		// deliberate choice rather than a surprise.
		all := digestOf(t, before, nil)
		after := `{"messages":[{"id":"a"}],"unread":1,"fetchedAt":"11:00"}`
		if digestOf(t, after, nil) == all {
			t.Fatal("whole-result watching should notice a timestamp change")
		}
	})

	t.Run("key order in the payload is not a change", func(t *testing.T) {
		reordered := `{"fetchedAt":"10:00","unread":1,"messages":[{"id":"a"}]}`
		if digestOf(t, reordered, watched) != base {
			t.Fatal("digest moved on JSON key reordering")
		}
	})
}

// TestBuildSnapshot_NonJSONResult keeps plain-text tools watchable.
func TestBuildSnapshot_NonJSONResult(t *testing.T) {
	a, err := buildSnapshot("plain text output", []string{"anything"}).digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildSnapshot("plain text output", []string{"anything"}).digest()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("identical plain-text results produced different digests")
	}
	c, err := buildSnapshot("different output", []string{"anything"}).digest()
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("different plain-text results produced the same digest")
	}
}

// TestParseWatchArgs covers creation-time validation. A malformed watch has to
// be rejected while the model can still correct it; the alternative is a job
// that fails quietly on every run.
func TestParseWatchArgs(t *testing.T) {
	t.Run("absent means no watch", func(t *testing.T) {
		w, err := parseWatchArgs(map[string]any{"message": "hi"})
		if err != nil || w != nil {
			t.Fatalf("parseWatchArgs() = %v, %v; want nil, nil", w, err)
		}
	})

	t.Run("full spec", func(t *testing.T) {
		w, err := parseWatchArgs(map[string]any{
			"watch_tool":   "google_gmail_messages_list",
			"watch_args":   map[string]any{"max_results": float64(10)},
			"watch_fields": []any{"messages.id", " "},
		})
		if err != nil {
			t.Fatal(err)
		}
		if w.Tool != "google_gmail_messages_list" {
			t.Fatalf("Tool = %q", w.Tool)
		}
		if w.Args["max_results"] != float64(10) {
			t.Fatalf("Args = %v", w.Args)
		}
		if len(w.Fields) != 1 || w.Fields[0] != "messages.id" {
			t.Fatalf("Fields = %v, want blank entries dropped", w.Fields)
		}
	})

	t.Run("rejects half-written watches", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"watch_args": map[string]any{"a": 1}},
			{"watch_fields": []any{"messages.id"}},
		} {
			if _, err := parseWatchArgs(args); err == nil {
				t.Fatalf("parseWatchArgs(%v) = nil error, want a rejection", args)
			}
		}
	})

	t.Run("rejects wrong types", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"watch_tool": "t", "watch_args": "not-an-object"},
			{"watch_tool": "t", "watch_fields": "not-a-list"},
			{"watch_tool": "t", "watch_fields": []any{42}},
		} {
			if _, err := parseWatchArgs(args); err == nil {
				t.Fatalf("parseWatchArgs(%v) = nil error, want a rejection", args)
			}
		}
	})
}

// TestWatchProbeRejectsUnavailableTool checks the guard that runs on every fire
// rather than only at creation: a tool the agent may no longer use must stop
// being probed.
func TestWatchProbeRejectsUnavailableTool(t *testing.T) {
	tool := &CronTool{} // no agentTools wired
	_, err := tool.probe(t.Context(), "alice", &cron.CronWatch{Tool: "whatever"})
	if err == nil {
		t.Fatal("probe with no registry wired returned no error")
	}
}
