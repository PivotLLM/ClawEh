// ClawEh
// License: MIT

package agenttoken

import (
	"strings"
	"sync"
	"testing"
)

func TestIssue_FormatAndUniqueness(t *testing.T) {
	m := NewManager()

	tok1 := m.Issue("alice")
	if tok1 == "" {
		t.Fatal("Issue returned empty token")
	}
	if !strings.HasPrefix(tok1, Prefix) {
		t.Errorf("token missing %q prefix: %q", Prefix, tok1)
	}
	if len(tok1) != TokenLen {
		t.Errorf("token length = %d, want %d", len(tok1), TokenLen)
	}
	if !IsValidFormat(tok1) {
		t.Errorf("Issue produced invalid format: %q", tok1)
	}

	tok2 := m.Issue("bob")
	if tok1 == tok2 {
		t.Errorf("two issued tokens collided: %q", tok1)
	}
}

func TestIssue_ReplacesExisting(t *testing.T) {
	m := NewManager()
	first := m.Issue("alice")
	second := m.Issue("alice")
	if first == second {
		t.Error("re-issuing for same agent returned the same token")
	}

	if name, ok := m.Resolve(first); ok {
		t.Errorf("old token still resolves to %q", name)
	}
	if name, ok := m.Resolve(second); !ok || name != "alice" {
		t.Errorf("new token failed to resolve: name=%q ok=%v", name, ok)
	}
}

func TestResolve_ValidToken(t *testing.T) {
	m := NewManager()
	tok := m.Issue("alice")

	name, ok := m.Resolve(tok)
	if !ok {
		t.Fatal("expected valid token to resolve")
	}
	if name != "alice" {
		t.Errorf("Resolve = %q, want %q", name, "alice")
	}
}

func TestResolve_RejectsEmpty(t *testing.T) {
	m := NewManager()
	if _, ok := m.Resolve(""); ok {
		t.Error("empty token resolved unexpectedly")
	}
}

func TestResolve_RejectsMalformed(t *testing.T) {
	m := NewManager()
	cases := []string{
		"AGT" + strings.Repeat("z", HexLen),   // bad hex
		"AGT" + strings.Repeat("0", HexLen-1), // too short
		"AGT" + strings.Repeat("0", HexLen+1), // too long
		"FOO" + strings.Repeat("0", HexLen),   // wrong prefix
		strings.Repeat("0", TokenLen),         // no prefix at all
		"agt" + strings.Repeat("0", HexLen),   // wrong case prefix
	}
	for _, c := range cases {
		if _, ok := m.Resolve(c); ok {
			t.Errorf("malformed token %q resolved unexpectedly", c)
		}
	}
}

func TestResolve_RejectsUnknown(t *testing.T) {
	m := NewManager()
	m.Issue("alice")

	other, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if _, ok := m.Resolve(other); ok {
		t.Errorf("unknown but well-formed token resolved unexpectedly")
	}
}

func TestResolve_RejectsSentinel(t *testing.T) {
	m := NewManager()
	if _, ok := m.Resolve(SubagentSentinel); ok {
		t.Error("sentinel token resolved as a real agent")
	}
}

func TestRevoke(t *testing.T) {
	m := NewManager()
	tok := m.Issue("alice")
	m.Revoke("alice")

	if _, ok := m.Resolve(tok); ok {
		t.Error("revoked token still resolves")
	}
	if got := m.TokenFor("alice"); got != "" {
		t.Errorf("TokenFor after Revoke = %q, want empty", got)
	}
}

func TestRevokeAll(t *testing.T) {
	m := NewManager()
	a := m.Issue("alice")
	b := m.Issue("bob")
	m.RevokeAll()

	if _, ok := m.Resolve(a); ok {
		t.Error("alice token still resolves after RevokeAll")
	}
	if _, ok := m.Resolve(b); ok {
		t.Error("bob token still resolves after RevokeAll")
	}
}

func TestIsSubagentSentinel(t *testing.T) {
	if !IsSubagentSentinel(SubagentSentinel) {
		t.Error("sentinel constant not recognized")
	}
	if IsSubagentSentinel("") {
		t.Error("empty string treated as sentinel")
	}
	m := NewManager()
	tok := m.Issue("alice")
	if IsSubagentSentinel(tok) {
		t.Error("real token treated as sentinel")
	}
}

func TestConcurrentIssue(t *testing.T) {
	m := NewManager()
	var wg sync.WaitGroup
	const n = 50
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i] = m.Issue("agent")
		}(i)
	}
	wg.Wait()

	// All but one will have been replaced — the surviving token should
	// resolve to "agent". Every issued token must have valid format.
	for _, tok := range tokens {
		if !IsValidFormat(tok) {
			t.Errorf("concurrent Issue produced invalid format: %q", tok)
		}
	}
	final := m.TokenFor("agent")
	if name, ok := m.Resolve(final); !ok || name != "agent" {
		t.Errorf("final TokenFor failed to resolve: name=%q ok=%v", name, ok)
	}
}
