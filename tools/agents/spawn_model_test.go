package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/providers"
)

func candList() []providers.FallbackCandidate {
	return []providers.FallbackCandidate{
		{Provider: "p", Model: "flash-wire", Alias: "Flash"},
		{Provider: "p", Model: "pro-wire", Alias: "Pro"},
	}
}

func TestMatchAndPromoteCandidate(t *testing.T) {
	c := candList()
	// Match by alias (case-insensitive).
	if m, ok := MatchCandidate(c, "pro"); !ok || m.Model != "pro-wire" {
		t.Fatalf("alias match failed: %+v ok=%v", m, ok)
	}
	// Match by wire model.
	if _, ok := MatchCandidate(c, "flash-wire"); !ok {
		t.Fatal("wire-model match failed")
	}
	// No match.
	if _, ok := MatchCandidate(c, "gpt-9"); ok {
		t.Fatal("unexpected match for unknown model")
	}
	// Tolerant of surrounding quotes and extra whitespace.
	for _, in := range []string{` "Pro" `, "'pro'", "`Pro`", "  pro  ", "“Pro”"} {
		if m, ok := MatchCandidate(c, in); !ok || m.Model != "pro-wire" {
			t.Fatalf("normalized match failed for %q: %+v ok=%v", in, m, ok)
		}
	}
	// Blank never matches.
	if _, ok := MatchCandidate(c, "   "); ok {
		t.Fatal("blank model should not match")
	}
}

func TestSpawner_InvalidModel_IsError(t *testing.T) {
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:      t.TempDir(),
		Live:           NewLiveSet(),
		SelfCandidates: candList(),
		RunFull:        echoRunFull,
	})
	sp := NewSpawner(mgr)

	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "write a chapter", Model: "gpt-9",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.ForLLM, "not available") {
		t.Fatalf("expected an 'not available' error result, got: %+v", res)
	}
	// The error should list the allowed models.
	if !strings.Contains(res.ForLLM, "Flash") || !strings.Contains(res.ForLLM, "Pro") {
		t.Fatalf("error should list configured models, got: %s", res.ForLLM)
	}
}

func TestSpawner_ValidModel_Runs(t *testing.T) {
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:      t.TempDir(),
		Live:           NewLiveSet(),
		SelfCandidates: candList(),
		RunFull:        echoRunFull,
	})
	sp := NewSpawner(mgr)

	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "write a chapter", Model: "Pro",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("valid model spawn should succeed, got: %s", res.ForLLM)
	}
}
