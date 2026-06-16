package commands

import (
	"context"
	"strings"
	"testing"
)

func runMemory(t *testing.T, rt *Runtime) string {
	t.Helper()
	def := memoryCommand()
	var reply string
	err := def.Handler(context.Background(), Request{
		Text:  "/memory",
		Reply: func(s string) error { reply = s; return nil },
	}, rt)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return reply
}

func TestMemoryCommand_RendersStatus(t *testing.T) {
	rt := &Runtime{GetMemoryStatus: func() string { return "Active domains: 3\nActive memories: 12" }}
	got := runMemory(t, rt)
	if !strings.Contains(got, "Active domains: 3") || !strings.Contains(got, "```") {
		t.Fatalf("unexpected reply: %q", got)
	}
}

func TestMemoryCommand_NotEnabled(t *testing.T) {
	rt := &Runtime{GetMemoryStatus: func() string { return "" }}
	if got := runMemory(t, rt); !strings.Contains(got, "not enabled") {
		t.Fatalf("expected not-enabled reply, got %q", got)
	}
}

func TestMemoryCommand_Unavailable(t *testing.T) {
	if got := runMemory(t, &Runtime{}); !strings.Contains(got, "not available") {
		t.Fatalf("expected not-available reply, got %q", got)
	}
}

func TestMemoryCommand_Registered(t *testing.T) {
	for _, d := range BuiltinDefinitions() {
		if d.Name == "memory" {
			return
		}
	}
	t.Fatal("memory command not registered in BuiltinDefinitions")
}
