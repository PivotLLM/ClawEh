package commands

import (
	"context"
	"strings"
	"testing"
	"time"
)

func runQuota(t *testing.T, rt *Runtime, text string) string {
	t.Helper()
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)
	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    text,
		Reply: func(s string) error {
			reply = s
			return nil
		},
	})
	return reply
}

func TestQuota_ListNone(t *testing.T) {
	rt := &Runtime{ListTokenQuota: func() []TokenQuotaEntry { return nil }}
	reply := runQuota(t, rt, "/quota")
	if !strings.Contains(reply, "Message-token rate limits:") || !strings.Contains(reply, "none") {
		t.Errorf("unexpected empty render:\n%s", reply)
	}
}

func TestQuota_ListEntries(t *testing.T) {
	rt := &Runtime{
		ListTokenQuota: func() []TokenQuotaEntry {
			return []TokenQuotaEntry{
				{Name: "gps", RatePerMin: 30, HitsInWindow: 3},
				{Name: "alarm", RatePerMin: 30, Blocked: true, BlockRemaining: 14 * time.Minute},
			}
		},
	}
	reply := runQuota(t, rt, "/quota list")
	if !strings.Contains(reply, "gps — 30/min — 3/30 this minute") {
		t.Errorf("missing gps active line:\n%s", reply)
	}
	if !strings.Contains(reply, "alarm — 30/min — blocked · clears in 14m") {
		t.Errorf("missing alarm blocked line:\n%s", reply)
	}
}

func TestQuota_ResetAll(t *testing.T) {
	var gotName string
	rt := &Runtime{
		ResetTokenQuota: func(name string) int {
			gotName = name
			return 2
		},
	}
	reply := runQuota(t, rt, "/quota reset")
	if gotName != "" {
		t.Errorf("reset-all passed name %q, want empty", gotName)
	}
	if !strings.Contains(reply, "Cleared 2 token block(s).") {
		t.Errorf("unexpected reset-all reply:\n%s", reply)
	}
}

func TestQuota_ResetByName(t *testing.T) {
	rt := &Runtime{
		ResetTokenQuota: func(name string) int {
			if name == "gps" {
				return 1
			}
			return 0
		},
	}
	if reply := runQuota(t, rt, "/quota reset gps"); !strings.Contains(reply, `Cleared block for token "gps".`) {
		t.Errorf("unexpected reset-by-name reply:\n%s", reply)
	}
	if reply := runQuota(t, rt, "/quota reset ghost"); !strings.Contains(reply, `No active block for token "ghost".`) {
		t.Errorf("unexpected reset-miss reply:\n%s", reply)
	}
}

func TestQuota_Unavailable(t *testing.T) {
	// Nil hooks → the feature reports unavailable rather than panicking.
	if reply := runQuota(t, &Runtime{}, "/quota"); reply != unavailableMsg {
		t.Errorf("list with nil hook = %q, want unavailable", reply)
	}
	if reply := runQuota(t, &Runtime{}, "/quota reset"); reply != unavailableMsg {
		t.Errorf("reset with nil hook = %q, want unavailable", reply)
	}
}
