package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func modelRuntime(active int, setErr error) *Runtime {
	return &Runtime{
		GetAgentModels: func() ([]ModelEntry, int) {
			return []ModelEntry{
				{Name: "alpha", Provider: "openai"},
				{Name: "beta", Provider: "anthropic"},
				{Name: "gamma", Provider: "openai"},
			}, active
		},
		SetActiveModel: func(idx int) (string, error) {
			if setErr != nil {
				return "", setErr
			}
			names := []string{"alpha", "beta", "gamma"}
			return names[idx], nil
		},
	}
}

func execModel(t *testing.T, rt *Runtime, text string) string {
	t.Helper()
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)
	var reply string
	res := ex.Execute(context.Background(), Request{
		Text: text,
		Reply: func(s string) error {
			reply = s
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}
	return reply
}

func TestModel_ValidInt(t *testing.T) {
	reply := execModel(t, modelRuntime(0, nil), "/model 2")
	want := "Model set to 2: gamma"
	if reply != want {
		t.Fatalf("reply=%q, want=%q", reply, want)
	}
}

func TestModel_OutOfRange(t *testing.T) {
	rt := modelRuntime(0, fmt.Errorf("model index out of range (0-2)"))
	reply := execModel(t, rt, "/model 9")
	if reply != "model index out of range (0-2)" {
		t.Fatalf("reply=%q, want range error", reply)
	}
}

func TestModel_NonIntArg(t *testing.T) {
	reply := execModel(t, modelRuntime(1, nil), "/model foo")
	if !strings.HasPrefix(reply, "Usage: /model <n>") {
		t.Fatalf("reply=%q, want usage prefix", reply)
	}
	// Usage should show the current numbered list with the active marker.
	if !strings.Contains(reply, "▶ 1: beta") {
		t.Fatalf("reply=%q, want active marker on index 1", reply)
	}
}

func TestModel_MissingArg(t *testing.T) {
	reply := execModel(t, modelRuntime(0, nil), "/model")
	if !strings.HasPrefix(reply, "Usage: /model <n>") {
		t.Fatalf("reply=%q, want usage prefix", reply)
	}
}

func TestModel_NoModels(t *testing.T) {
	rt := &Runtime{
		GetAgentModels: func() ([]ModelEntry, int) { return nil, 0 },
		SetActiveModel: func(idx int) (string, error) { return "", nil },
	}
	reply := execModel(t, rt, "/model 0")
	if !strings.Contains(reply, "no configured models") {
		t.Fatalf("reply=%q, want no-models message", reply)
	}
}

func TestListModels_NumberedWithActiveMarker(t *testing.T) {
	reply := execModel(t, modelRuntime(2, nil), "/list models")
	for _, want := range []string{
		"  0: alpha  (openai)",
		"  1: beta  (anthropic)",
		"▶ 2: gamma  (openai)",
		"Use /model <n> to switch.",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply missing %q\ngot:\n%s", want, reply)
		}
	}
}

func TestListModels_NoModels(t *testing.T) {
	rt := &Runtime{
		GetAgentModels: func() ([]ModelEntry, int) { return nil, 0 },
	}
	reply := execModel(t, rt, "/list models")
	if !strings.Contains(reply, "no configured models") {
		t.Fatalf("reply=%q, want no-models message", reply)
	}
}
