// ClawEh
// License: MIT

package agenttoken

import (
	"strings"
	"testing"
)

func TestRedact_ReplacesRealTokens(t *testing.T) {
	m := NewManager()
	tok := m.Issue("alice")

	in := "leaked token: " + tok + " and more"
	out := Redact(in)
	if strings.Contains(out, tok) {
		t.Errorf("Redact left raw token in output: %q", out)
	}
	if !strings.Contains(out, Redaction) {
		t.Errorf("Redact omitted placeholder: %q", out)
	}
}

func TestRedact_PreservesSentinel(t *testing.T) {
	in := "sentinel: " + SubagentSentinel
	out := Redact(in)
	if !strings.Contains(out, SubagentSentinel) {
		t.Errorf("Redact stripped sentinel; got %q", out)
	}
	if strings.Contains(out, Redaction) {
		t.Errorf("Redact substituted placeholder for sentinel: %q", out)
	}
}

func TestRedact_HandlesMultipleTokens(t *testing.T) {
	m := NewManager()
	a := m.Issue("alice")
	b := m.Issue("bob")
	in := a + " " + b + " " + SubagentSentinel
	out := Redact(in)
	if strings.Contains(out, a) || strings.Contains(out, b) {
		t.Errorf("Redact left a real token in output: %q", out)
	}
	if !strings.Contains(out, SubagentSentinel) {
		t.Errorf("Redact stripped sentinel from mixed input: %q", out)
	}
	if got := strings.Count(out, Redaction); got != 2 {
		t.Errorf("expected 2 redactions, got %d in %q", got, out)
	}
}

func TestRedact_EmptyAndNoMatch(t *testing.T) {
	if Redact("") != "" {
		t.Error("Redact mutated empty string")
	}
	in := "no tokens here"
	if Redact(in) != in {
		t.Errorf("Redact altered string with no tokens: %q", Redact(in))
	}
}
