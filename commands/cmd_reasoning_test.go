package commands

import (
	"context"
	"strings"
	"testing"
)

func runReasoning(t *testing.T, text string, rt *Runtime) string {
	t.Helper()
	def := reasoningCommand()
	var reply string
	err := def.Handler(context.Background(), Request{
		Text:  text,
		Reply: func(s string) error { reply = s; return nil },
	}, rt)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return reply
}

func TestReasoningCommand_ShowsState(t *testing.T) {
	rt := &Runtime{
		GetExposeReasoning: func() bool { return false },
		SetExposeReasoning: func(bool) {},
	}
	if got := runReasoning(t, "/reasoning", rt); !strings.Contains(got, "OFF") {
		t.Fatalf("expected current state OFF, got %q", got)
	}
}

func TestReasoningCommand_TogglesOnOff(t *testing.T) {
	var set *bool
	rt := &Runtime{
		GetExposeReasoning: func() bool { return false },
		SetExposeReasoning: func(on bool) { set = &on },
	}
	if got := runReasoning(t, "/reasoning on", rt); !strings.Contains(got, "ON") {
		t.Fatalf("reply = %q, want ON", got)
	}
	if set == nil || *set != true {
		t.Fatalf("SetExposeReasoning(true) not called; set=%v", set)
	}

	set = nil
	if got := runReasoning(t, "/reasoning off", rt); !strings.Contains(got, "OFF") {
		t.Fatalf("reply = %q, want OFF", got)
	}
	if set == nil || *set != false {
		t.Fatalf("SetExposeReasoning(false) not called; set=%v", set)
	}
}

func TestReasoningCommand_InvalidArg(t *testing.T) {
	called := false
	rt := &Runtime{
		GetExposeReasoning: func() bool { return false },
		SetExposeReasoning: func(bool) { called = true },
	}
	if got := runReasoning(t, "/reasoning maybe", rt); !strings.Contains(got, "Usage") {
		t.Fatalf("reply = %q, want usage", got)
	}
	if called {
		t.Fatal("invalid arg must not toggle the flag")
	}
}

func TestReasoningCommand_Unavailable(t *testing.T) {
	if got := runReasoning(t, "/reasoning on", &Runtime{}); !strings.Contains(got, unavailableMsg) {
		t.Fatalf("expected unavailable reply, got %q", got)
	}
}
