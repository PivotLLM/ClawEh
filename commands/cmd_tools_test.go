package commands

import (
	"context"
	"strings"
	"testing"
)

func runTools(t *testing.T, text string, rt *Runtime) string {
	t.Helper()
	def := toolsCommand()
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

func TestToolsCommand_ShowsState(t *testing.T) {
	rt := &Runtime{
		GetShowToolActivity: func() bool { return false },
		SetShowToolActivity: func(bool) {},
	}
	if got := runTools(t, "/tools", rt); !strings.Contains(got, "OFF") {
		t.Fatalf("expected current state OFF, got %q", got)
	}
}

func TestToolsCommand_TogglesOnOff(t *testing.T) {
	var set *bool
	rt := &Runtime{
		GetShowToolActivity: func() bool { return false },
		SetShowToolActivity: func(on bool) { set = &on },
	}
	if got := runTools(t, "/tools on", rt); !strings.Contains(got, "ON") {
		t.Fatalf("reply = %q, want ON", got)
	}
	if set == nil || *set != true {
		t.Fatalf("SetShowToolActivity(true) not called; set=%v", set)
	}

	set = nil
	if got := runTools(t, "/tools off", rt); !strings.Contains(got, "OFF") {
		t.Fatalf("reply = %q, want OFF", got)
	}
	if set == nil || *set != false {
		t.Fatalf("SetShowToolActivity(false) not called; set=%v", set)
	}
}

func TestToolsCommand_InvalidArg(t *testing.T) {
	called := false
	rt := &Runtime{
		GetShowToolActivity: func() bool { return false },
		SetShowToolActivity: func(bool) { called = true },
	}
	if got := runTools(t, "/tools maybe", rt); !strings.Contains(got, "Usage") {
		t.Fatalf("reply = %q, want usage", got)
	}
	if called {
		t.Fatal("invalid arg must not toggle the flag")
	}
}

func TestToolsCommand_Unavailable(t *testing.T) {
	if got := runTools(t, "/tools on", &Runtime{}); !strings.Contains(got, unavailableMsg) {
		t.Fatalf("expected unavailable reply, got %q", got)
	}
}
