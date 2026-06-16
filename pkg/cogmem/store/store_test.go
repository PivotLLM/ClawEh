// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validID checks the 6-char id format: 1-char prefix + idRandomLen Crockford chars.
func validID(id, prefix string) bool {
	if len(id) != 1+idRandomLen || id[:1] != prefix {
		return false
	}
	for _, c := range id[1:] {
		if !strings.ContainsRune(crockfordAlphabet, c) {
			return false
		}
	}
	return true
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.cogmem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenMigrateSeeds(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	rev, err := s.StableRev(ctx)
	if err != nil {
		t.Fatalf("stable rev: %v", err)
	}
	if rev != 0 {
		t.Fatalf("initial stable_rev = %d, want 0", rev)
	}
}

func TestDomainCreateAndIDs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d1, err := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "alice", SessionKey: "agent:alice:main",
		Type: DomainProject, Name: "Website Redesign", Status: StatusActive,
		Summary: "CSS grid migration",
	})
	if err != nil {
		t.Fatalf("create d1: %v", err)
	}
	if !validID(d1.ID, "d") {
		t.Fatalf("first domain id = %q, want d+5 chars", d1.ID)
	}
	d2, err := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "alice", SessionKey: "agent:alice:main",
		Type: DomainProject, Name: "BioTech",
	})
	if err != nil {
		t.Fatalf("create d2: %v", err)
	}
	if !validID(d2.ID, "d") || d2.ID == d1.ID {
		t.Fatalf("second domain id = %q (d1=%q): want distinct d+5 chars", d2.ID, d1.ID)
	}
	// stable_rev bumped twice (one per create).
	if rev, _ := s.StableRev(ctx); rev != 2 {
		t.Fatalf("stable_rev = %d, want 2", rev)
	}
	got, err := s.GetDomain(ctx, s.DB(), d1.ID, false)
	if err != nil || got.Name != "Website Redesign" {
		t.Fatalf("get d1: %v name=%q", err, got.Name)
	}
}

func TestDomainTriggersRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "a", Type: DomainProject, Name: "Email",
		Triggers: "  Google_Gmail, microsoft365_mail ,, system  ",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Stored normalized: trimmed, lowercased, empties dropped.
	if d.Triggers != "google_gmail,microsoft365_mail,system" {
		t.Fatalf("triggers = %q, want normalized", d.Triggers)
	}
	// Case-insensitive substring match against a full tool name.
	if tok, ok := d.MatchTrigger("mcp__fusion__google_gmail_messages_list"); !ok || tok != "google_gmail" {
		t.Fatalf("MatchTrigger gmail = %q,%v", tok, ok)
	}
	if tok, ok := d.MatchTrigger("mcp__fusion__system__get"); !ok || tok != "system" {
		t.Fatalf("MatchTrigger system = %q,%v", tok, ok)
	}
	if _, ok := d.MatchTrigger("web_fetch"); ok {
		t.Fatalf("web_fetch should not match")
	}

	// Update replaces triggers; empty clears them.
	empty := ""
	if err := s.UpdateDomain(ctx, s.DB(), d.ID, UpdateDomainParams{ExpectedVersion: 1, Triggers: &empty}); err != nil {
		t.Fatalf("update clear: %v", err)
	}
	got, _ := s.GetDomain(ctx, s.DB(), d.ID, false)
	if got.Triggers != "" || len(got.TriggerTokens()) != 0 {
		t.Fatalf("triggers not cleared: %q", got.Triggers)
	}
}

func TestTriggerUnderscoreInsensitive(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// Token written with double underscores is collapsed on store.
	d, _ := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "a", Type: DomainProject, Name: "M", Triggers: "fusion__google",
	})
	if d.Triggers != "fusion_google" {
		t.Fatalf("triggers = %q, want collapsed fusion_google", d.Triggers)
	}
	// Matches whether the tool name uses single or double underscores.
	for _, name := range []string{
		"mcp__fusion__google_gmail_messages_list", // double-underscore MCP separators
		"mcp_fusion_google_x",                     // single underscore
	} {
		if tok, ok := d.MatchTrigger(name); !ok || tok != "fusion_google" {
			t.Fatalf("MatchTrigger(%q) = %q,%v", name, tok, ok)
		}
	}
}

func TestDomainOptimisticConcurrency(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _ := s.CreateDomain(ctx, s.DB(), CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "P"})
	sum := "updated summary"
	if err := s.UpdateDomain(ctx, s.DB(), d.ID, UpdateDomainParams{ExpectedVersion: 1, Summary: &sum}); err != nil {
		t.Fatalf("update v1: %v", err)
	}
	// Stale version must conflict.
	if err := s.UpdateDomain(ctx, s.DB(), d.ID, UpdateDomainParams{ExpectedVersion: 1, Summary: &sum}); err != ErrVersionConflict {
		t.Fatalf("stale update err = %v, want ErrVersionConflict", err)
	}
	got, _ := s.GetDomain(ctx, s.DB(), d.ID, false)
	if got.Version != 2 || got.Summary != sum {
		t.Fatalf("after update: version=%d summary=%q", got.Version, got.Summary)
	}
}

func TestHookLifecycleAndStableRev(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// Always-on domain so active hooks affect stable_rev.
	base, _ := s.GeneralDomain(ctx, s.DB()) // the seeded always-on general domain
	revAfterDomain, _ := s.StableRev(ctx)

	h, err := s.AddHook(ctx, s.DB(), AddHookParams{
		DomainID: base.ID, Kind: KindRule, Text: "Never use blue.",
		Status: StatusActive, Confidence: 0.95, Source: SourceUserExplicit,
	})
	if err != nil {
		t.Fatalf("add hook: %v", err)
	}
	if !validID(h.ID, "h") {
		t.Fatalf("hook id = %q, want h+5 chars", h.ID)
	}
	if rev, _ := s.StableRev(ctx); rev <= revAfterDomain {
		t.Fatalf("stable_rev did not bump on always-on active hook add")
	}

	// Supersede.
	h2, err := s.SupersedeHook(ctx, s.DB(), h.ID, AddHookParams{
		DomainID: base.ID, Kind: KindRule, Text: "Use blue for the layout.",
		Status: StatusActive, Confidence: 0.95, Source: SourceUserExplicit,
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	old, _ := s.GetHook(ctx, s.DB(), h.ID)
	if old.Status != StatusRetired {
		t.Fatalf("old hook status = %q, want retired", old.Status)
	}
	if h2.SupersedesHookID == nil || *h2.SupersedesHookID != h.ID {
		t.Fatalf("supersede link not set on %s", h2.ID)
	}
	active, _ := s.ListHooks(ctx, s.DB(), base.ID, StatusActive)
	if len(active) != 1 || active[0].Text != "Use blue for the layout." {
		t.Fatalf("active hooks = %+v", active)
	}
}

func TestSearchAndPending(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _ := s.CreateDomain(ctx, s.DB(), CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "P"})
	_, _ = s.AddHook(ctx, s.DB(), AddHookParams{DomainID: d.ID, Kind: KindFact, Text: "The BioTech report targets Q3.", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	_, _ = s.AddHook(ctx, s.DB(), AddHookParams{DomainID: d.ID, Kind: KindFact, Text: "Eric likes terse output.", Status: StatusReview, Confidence: 0.6, Source: SourceAssistantInferred})

	hits, err := s.SearchHooks(ctx, s.DB(), "biotech", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search biotech: %v hits=%d", err, len(hits))
	}
	// Review hook is not returned by active search.
	if h, _ := s.SearchHooks(ctx, s.DB(), "terse", 10); len(h) != 0 {
		t.Fatalf("review hook leaked into active search")
	}
	pend, err := s.ListPending(ctx, s.DB(), 8)
	if err != nil || len(pend) != 1 || pend[0].Text != "Eric likes terse output." {
		t.Fatalf("pending = %+v err=%v", pend, err)
	}
}

func TestWatermarkAndLease(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ap := "/sessions/agent_alice_main.archive.db"
	if err := s.IncMeaningful(ctx, s.DB(), ap, 3); err != nil {
		t.Fatalf("inc: %v", err)
	}
	st, _ := s.GetState(ctx, s.DB(), ap)
	if st.MeaningfulCount != 3 {
		t.Fatalf("meaningful = %d, want 3", st.MeaningfulCount)
	}
	if err := s.SetWatermark(ctx, s.DB(), ap, 100, 100); err != nil {
		t.Fatalf("watermark: %v", err)
	}
	st, _ = s.GetState(ctx, s.DB(), ap)
	if st.ConsolidatedSeq != 100 || st.MeaningfulCount != 0 {
		t.Fatalf("after watermark: seq=%d meaningful=%d", st.ConsolidatedSeq, st.MeaningfulCount)
	}

	ok, err := s.AcquireLease(ctx, s.DB(), "consolidate:"+ap, "worker-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: %v ok=%v", err, ok)
	}
	ok2, _ := s.AcquireLease(ctx, s.DB(), "consolidate:"+ap, "worker-2", time.Minute)
	if ok2 {
		t.Fatalf("second worker acquired a held lease")
	}
	if err := s.ReleaseLease(ctx, s.DB(), "consolidate:"+ap, "worker-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	ok3, _ := s.AcquireLease(ctx, s.DB(), "consolidate:"+ap, "worker-2", time.Minute)
	if !ok3 {
		t.Fatalf("worker-2 could not acquire after release")
	}
}
