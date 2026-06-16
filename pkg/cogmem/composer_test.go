// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "c.cogmem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStableBlockEmpty(t *testing.T) {
	c := New(newStore(t))
	txt, _, err := c.StableBlock(context.Background())
	if err != nil || txt != "" {
		t.Fatalf("empty stable block: %q err=%v", txt, err)
	}
}

func TestStableBlockContent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	base, _ := s.GeneralDomain(ctx, db) // the seeded always-on general domain
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: base.ID, Type: store.TypePreference, Text: "Be concise.", Status: store.StatusActive, Confidence: 0.95, Source: store.SourceUserExplicit})
	proj, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Website Redesign", Summary: "CSS grid migration"})
	_ = proj
	// A pending (review) inference.
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: base.ID, Type: store.TypePreference, Text: "Prefers tabs.", Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred})

	c := New(s)
	txt, rev, err := c.StableBlock(ctx)
	if err != nil {
		t.Fatalf("stable block: %v", err)
	}
	if rev == 0 {
		t.Fatalf("expected non-zero stable_rev")
	}
	for _, want := range []string{"Be concise.", "Pending (unconfirmed", "Prefers tabs.", "Projects / Topics", "Website Redesign"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("stable block missing %q:\n%s", want, txt)
		}
	}
}

func TestPendingDigestThrottledToOncePerSession(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	base, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: base.ID, Type: store.TypePreference, Text: "Prefers tabs.", Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred})

	c := New(s)

	// First call surfaces the pending digest.
	first, _, err := c.StableBlock(ctx)
	if err != nil {
		t.Fatalf("first stable block: %v", err)
	}
	if !strings.Contains(first, "Prefers tabs.") || !strings.Contains(first, "Pending") {
		t.Fatalf("first call should surface pending:\n%s", first)
	}

	// Second call (same session/composer) must not re-surface the same pending memory.
	second, _, err := c.StableBlock(ctx)
	if err != nil {
		t.Fatalf("second stable block: %v", err)
	}
	if strings.Contains(second, "Prefers tabs.") {
		t.Fatalf("second call should not re-surface already-asked pending memory:\n%s", second)
	}

	// A newly added pending memory IS surfaced on the next call.
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: base.ID, Type: store.TypeFact, Text: "Uses Linux.", Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred})
	third, _, err := c.StableBlock(ctx)
	if err != nil {
		t.Fatalf("third stable block: %v", err)
	}
	if !strings.Contains(third, "Uses Linux.") {
		t.Fatalf("third call should surface the new pending memory:\n%s", third)
	}
	if strings.Contains(third, "Prefers tabs.") {
		t.Fatalf("third call should not re-surface the old pending memory:\n%s", third)
	}
}

func TestRoutedBlockToolTrigger(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	// Two domains; "Email" is older (less recent) but trigger-matched.
	email, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{
		AgentID: "a", Type: store.DomainProject, Name: "Email", Summary: "mail prefs",
		Triggers: "google_gmail,microsoft365_mail",
	})
	other, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Other", Summary: "misc"})
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: email.ID, Type: store.TypePreference, Text: "Archive newsletters.", Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit})
	// "Other" is the most recently touched, so recency alone would rank it first.
	_ = s.Touch(ctx, db, email.ID)
	_ = s.Touch(ctx, db, other.ID)

	c := New(s, WithTopKDomains(1))
	res, err := c.RoutedBlock(ctx, RouteRequest{
		RecentTools: []string{"mcp__fusion__system__get", "mcp__fusion__google_gmail_messages_list"},
		Trace:       true,
	})
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	// The tool-triggered Email domain must win the single slot over more-recent Other.
	if len(res.Loaded) != 1 || res.Loaded[0] != email.ID {
		t.Fatalf("expected tool-triggered %s first, got %v", email.ID, res.Loaded)
	}
	if len(res.Trace) != 1 || res.Trace[0].Signal != "tool:google_gmail" {
		t.Fatalf("trace = %+v, want signal tool:google_gmail", res.Trace)
	}
	if !strings.Contains(res.Text, "Archive newsletters.") {
		t.Fatalf("routed text missing email hook:\n%s", res.Text)
	}
}

func TestRoutedBlockToolTriggerNoDuplicate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	// A domain that is BOTH tool-triggered AND the most recent must appear once.
	d, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{
		AgentID: "a", Type: store.DomainProject, Name: "Email", Summary: "mail", Triggers: "gmail",
	})
	_ = s.Touch(ctx, db, d.ID)

	c := New(s, WithTopKDomains(8))
	res, err := c.RoutedBlock(ctx, RouteRequest{RecentTools: []string{"mcp__fusion__google_gmail_messages_list"}, Trace: true})
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	if len(res.Loaded) != 1 || res.Loaded[0] != d.ID {
		t.Fatalf("domain should appear exactly once, got %v", res.Loaded)
	}
	if c := strings.Count(res.Text, "Active Context: "+d.ID); c != 1 {
		t.Fatalf("domain rendered %d times, want 1:\n%s", c, res.Text)
	}
	if res.Trace[0].Signal != "tool:gmail" {
		t.Fatalf("trigger should take precedence over recency, got %q", res.Trace[0].Signal)
	}
}

func TestRoutedBlockLexicalMatch(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	// "BioTech" is older/less recent; "Other" is the most recently touched.
	bio, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "BioTech", Summary: "research report"})
	other, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Other", Summary: "misc"})
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: bio.ID, Type: store.TypeFact, Text: "The biotech report targets Q3.", Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit})
	_ = s.Touch(ctx, db, bio.ID)
	_ = s.Touch(ctx, db, other.ID)

	c := New(s, WithTopKDomains(1))
	res, err := c.RoutedBlock(ctx, RouteRequest{RouteText: "what's the status of the biotech report?", Trace: true})
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	// Lexical match on "biotech" must beat the more-recent "Other" for the slot.
	if len(res.Loaded) != 1 || res.Loaded[0] != bio.ID {
		t.Fatalf("expected lexical match %s, got %v", bio.ID, res.Loaded)
	}
	if len(res.Trace) != 1 || res.Trace[0].Signal != "match:biotech" {
		t.Fatalf("trace = %+v, want match:biotech", res.Trace)
	}
}

func TestRoutedBlockSignalPriorityNoDuplicate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	// One domain is both lexically matched AND tool-triggered; it must appear once
	// and the stronger (tool) signal wins.
	email, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{
		AgentID: "a", Type: store.DomainProject, Name: "Email", Summary: "email handling", Triggers: "google_gmail",
	})
	_ = s.Touch(ctx, db, email.ID)

	c := New(s, WithTopKDomains(8))
	res, err := c.RoutedBlock(ctx, RouteRequest{
		RouteText:   "please check my email",
		RecentTools: []string{"mcp__fusion__google_gmail_messages_list"},
		Trace:       true,
	})
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	if len(res.Loaded) != 1 || res.Loaded[0] != email.ID {
		t.Fatalf("domain should appear exactly once, got %v", res.Loaded)
	}
	if res.Trace[0].Signal != "tool:google_gmail" {
		t.Fatalf("tool signal should win over lexical/recency, got %q", res.Trace[0].Signal)
	}
}

func TestRouteTokensStopwordsAndLength(t *testing.T) {
	got := routeTokens("What is the STATUS of the BioTech report, please?")
	want := map[string]bool{"status": true, "biotech": true, "report": true}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want exactly %v", got, want)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Fatalf("unexpected token %q in %v", tok, got)
		}
	}
}

func TestRoutedBlockRecency(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	d1, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Old", Summary: "old"})
	d2, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Recent", Summary: "recent"})
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{DomainID: d2.ID, Type: store.TypeFact, Text: "key fact", Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit})
	// d2 is touched (more recent) than d1.
	_ = s.Touch(ctx, db, d1.ID)
	_ = s.Touch(ctx, db, d2.ID)

	c := New(s, WithTopKDomains(1))
	res, err := c.RoutedBlock(ctx, RouteRequest{Trace: true})
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	if len(res.Loaded) != 1 || res.Loaded[0] != d2.ID {
		t.Fatalf("expected most-recent %s, got %v", d2.ID, res.Loaded)
	}
	if !strings.Contains(res.Text, "Recent") || !strings.Contains(res.Text, "key fact") {
		t.Fatalf("routed text:\n%s", res.Text)
	}
	if len(res.Trace) != 1 || res.Trace[0].Signal != "recency" {
		t.Fatalf("trace = %+v", res.Trace)
	}
}
