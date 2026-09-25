package commands

import (
	"context"
	"strings"
	"testing"
)

// toggleCommand describes a boolean on/off command and how its getter and
// setter are wired into the Runtime. Every such command shares one contract,
// tested once below; a new toggle gets a row, not a file.
type toggleCommand struct {
	name string
	text string // the command as typed, e.g. "/tools"
	def  func() Definition
	wire func(rt *Runtime, get func() bool, set func(bool))
}

var toggleCommands = []toggleCommand{
	{
		name: "reasoning", text: "/reasoning", def: reasoningCommand,
		wire: func(rt *Runtime, get func() bool, set func(bool)) {
			rt.GetExposeReasoning, rt.SetExposeReasoning = get, set
		},
	},
	{
		name: "tools", text: "/tools", def: toolsCommand,
		wire: func(rt *Runtime, get func() bool, set func(bool)) {
			rt.GetShowToolActivity, rt.SetShowToolActivity = get, set
		},
	},
}

func runToggle(t *testing.T, tc toggleCommand, args string, rt *Runtime) string {
	t.Helper()
	var reply string
	err := tc.def().Handler(context.Background(), Request{
		Text:  strings.TrimSpace(tc.text + " " + args),
		Reply: func(s string) error { reply = s; return nil },
	}, rt)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return reply
}

func TestToggleCommands(t *testing.T) {
	for _, tc := range toggleCommands {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("shows state", func(t *testing.T) {
				rt := &Runtime{}
				tc.wire(rt, func() bool { return false }, func(bool) {})
				if got := runToggle(t, tc, "", rt); !strings.Contains(got, "OFF") {
					t.Fatalf("expected current state OFF, got %q", got)
				}
			})

			t.Run("toggles on and off", func(t *testing.T) {
				var set *bool
				rt := &Runtime{}
				tc.wire(rt, func() bool { return false }, func(on bool) { set = &on })
				if got := runToggle(t, tc, "on", rt); !strings.Contains(got, "ON") {
					t.Fatalf("reply = %q, want ON", got)
				}
				if set == nil || !*set {
					t.Fatalf("setter(true) not called; set=%v", set)
				}

				set = nil
				if got := runToggle(t, tc, "off", rt); !strings.Contains(got, "OFF") {
					t.Fatalf("reply = %q, want OFF", got)
				}
				if set == nil || *set {
					t.Fatalf("setter(false) not called; set=%v", set)
				}
			})

			t.Run("invalid argument", func(t *testing.T) {
				called := false
				rt := &Runtime{}
				tc.wire(rt, func() bool { return false }, func(bool) { called = true })
				if got := runToggle(t, tc, "maybe", rt); !strings.Contains(got, "Usage") {
					t.Fatalf("reply = %q, want usage", got)
				}
				if called {
					t.Fatal("invalid arg must not toggle the flag")
				}
			})

			t.Run("unavailable", func(t *testing.T) {
				if got := runToggle(t, tc, "on", &Runtime{}); !strings.Contains(got, unavailableMsg) {
					t.Fatalf("expected unavailable reply, got %q", got)
				}
			})
		})
	}
}
