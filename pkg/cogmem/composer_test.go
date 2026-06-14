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
	base, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainBaseline, Name: "baseline"})
	_, _ = s.AddHook(ctx, db, store.AddHookParams{DomainID: base.ID, Kind: store.KindPreference, Text: "Be concise.", Status: store.StatusActive, Confidence: 0.95, Source: store.SourceUserExplicit})
	proj, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Website Redesign", Summary: "CSS grid migration"})
	_ = proj
	// A pending (review) inference.
	_, _ = s.AddHook(ctx, db, store.AddHookParams{DomainID: base.ID, Kind: store.KindPreference, Text: "Prefers tabs.", Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred})

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

func TestRoutedBlockRecency(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	d1, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Old", Summary: "old"})
	d2, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Type: store.DomainProject, Name: "Recent", Summary: "recent"})
	_, _ = s.AddHook(ctx, db, store.AddHookParams{DomainID: d2.ID, Kind: store.KindFact, Text: "key fact", Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit})
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
