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

// TestDomainTriggerWildcardsAreDecorative confirms '*' wrappers are stripped and
// behave identically to the bare substring (matching is always "contains").
func TestDomainTriggerWildcardsAreDecorative(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "a", Type: DomainProject, Name: "Dev",
		Triggers: "*github*, *mail*",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Stored form has the asterisks stripped — same as the bare words.
	if d.Triggers != "github,mail" {
		t.Fatalf("triggers = %q, want %q", d.Triggers, "github,mail")
	}
	// "*github*" matches an MCP tool from the github server.
	if tok, ok := d.MatchTrigger("mcp_github_search"); !ok || tok != "github" {
		t.Fatalf("MatchTrigger github = %q,%v", tok, ok)
	}
	// "*mail*" matches anywhere in the name.
	if _, ok := d.MatchTrigger("google_gmail_send"); !ok {
		t.Fatalf("*mail* should match google_gmail_send")
	}
	if _, ok := d.MatchTrigger("web_fetch"); ok {
		t.Fatalf("web_fetch should not match")
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

func TestEnsureTypeValuesNormalizesLegacy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.cogmem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Seed a project domain + memory, then force legacy type values directly.
	d, _ := s.CreateDomain(ctx, s.DB(), CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "P"})
	m, _ := s.AddMemory(ctx, s.DB(), AddMemoryParams{DomainID: d.ID, Type: TypeFact, Text: "x", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	if _, err := s.DB().ExecContext(ctx, `UPDATE memories SET type='lesson' WHERE id=?`, m.ID); err != nil {
		t.Fatalf("force legacy memory type: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE domains SET type='repo' WHERE id=?`, d.ID); err != nil {
		t.Fatalf("force legacy domain type: %v", err)
	}
	_ = s.Close()

	// Reopen → migrate() runs ensureTypeValues and normalizes the legacy values.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	gm, _ := s2.GetMemory(ctx, s2.DB(), m.ID)
	if gm.Type != TypeFact {
		t.Fatalf("memory type = %q, want fact", gm.Type)
	}
	gd, _ := s2.GetDomain(ctx, s2.DB(), d.ID, false)
	if gd.Type != DomainProject {
		t.Fatalf("domain type = %q, want project", gd.Type)
	}
}

func TestDedupeActiveMemories(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	db := s.DB()
	d, _ := s.CreateDomain(ctx, db, CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "P"})
	other, _ := s.CreateDomain(ctx, db, CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "Q"})

	add := func(domainID, text string) {
		_, _ = s.AddMemory(ctx, db, AddMemoryParams{DomainID: domainID, Type: TypeFact, Text: text, Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	}
	// Three identical "The Frame" in domain d (the runaway-loop case) + one unique.
	add(d.ID, "The Frame")
	add(d.ID, "The Frame")
	add(d.ID, "The Frame")
	add(d.ID, "unique fact")
	// Same text in a DIFFERENT domain must NOT be treated as a duplicate.
	add(other.ID, "The Frame")

	n, err := s.DedupeActiveMemories(ctx)
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if n != 2 {
		t.Fatalf("retired = %d, want 2 (two of the three dups)", n)
	}
	// d keeps exactly one "The Frame" + the unique fact.
	act, _ := s.ListMemories(ctx, db, d.ID, StatusActive)
	frames := 0
	for _, m := range act {
		if m.Text == "The Frame" {
			frames++
		}
	}
	if frames != 1 || len(act) != 2 {
		t.Fatalf("domain d active = %d (frames=%d), want 2 (1 frame + 1 unique)", len(act), frames)
	}
	// Other domain's "The Frame" is untouched.
	oa, _ := s.ListMemories(ctx, db, other.ID, StatusActive)
	if len(oa) != 1 {
		t.Fatalf("other domain active = %d, want 1", len(oa))
	}
	// Idempotent.
	if n2, _ := s.DedupeActiveMemories(ctx); n2 != 0 {
		t.Fatalf("second dedupe retired %d, want 0", n2)
	}
}

func TestDedupeGeneralDomains(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.cogmem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The seeded general, plus a memory in it.
	g1, _ := s.GeneralDomain(ctx, s.DB())
	_, _ = s.AddMemory(ctx, s.DB(), AddMemoryParams{DomainID: g1.ID, Type: TypeRule, Text: "rule one", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})

	// Simulate the pre-fix race: a second general inserted directly (bypassing the
	// guarded seed), with its own memory. Drop the unique index first so the raw
	// insert is possible, mimicking an older database.
	if _, err := s.DB().ExecContext(ctx, `DROP INDEX IF EXISTS idx_one_general`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `
		INSERT INTO domains(id, agent_id, session_key, type, name, status, version, summary, state_json, schema_name, schema_version, triggers, created_at, updated_at)
		VALUES('dDUP02','','','general','General',?,1,'','{}','domain',1,'',?,?)`,
		string(StatusActive), now()+1, now()+1); err != nil {
		t.Fatalf("insert dup: %v", err)
	}
	_, _ = s.AddMemory(ctx, s.DB(), AddMemoryParams{DomainID: "dDUP02", Type: TypeFact, Text: "fact two", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	_ = s.Close()

	// Reopen → migrate() dedupes and re-creates the unique index.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	doms, _ := s2.ListDomains(ctx, s2.DB(), StatusActive)
	generals := 0
	var gid string
	for _, d := range doms {
		if d.Type == DomainGeneral {
			generals++
			gid = d.ID
		}
	}
	if generals != 1 {
		t.Fatalf("general domains after dedupe = %d, want 1", generals)
	}
	// Both memories survived, merged onto the surviving general.
	mems, _ := s2.ListMemories(ctx, s2.DB(), gid, StatusActive)
	if len(mems) != 2 {
		t.Fatalf("merged memories = %d, want 2", len(mems))
	}
	// The unique index now blocks a second general.
	if _, err := s2.DB().ExecContext(ctx, `
		INSERT INTO domains(id, agent_id, session_key, type, name, status, version, summary, state_json, schema_name, schema_version, triggers, created_at, updated_at)
		VALUES('dDUP03','','','general','General','active',1,'','{}','domain',1,'',?,?)`,
		now(), now()); err == nil {
		t.Fatal("expected unique-index violation inserting a second general")
	}
}

func TestPurgeNonActive(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	db := s.DB()

	// Active project domain with an active + a retired memory.
	keep, _ := s.CreateDomain(ctx, db, CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "Keep", Status: StatusActive})
	_, _ = s.AddMemory(ctx, db, AddMemoryParams{DomainID: keep.ID, Type: TypeFact, Text: "active fact", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	_, _ = s.AddMemory(ctx, db, AddMemoryParams{DomainID: keep.ID, Type: TypeFact, Text: "old fact", Status: StatusRetired, Confidence: 0.9, Source: SourceUserExplicit})

	// Archived domain with a (still-active) memory — both should go.
	arch, _ := s.CreateDomain(ctx, db, CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "Arch", Status: StatusActive})
	_, _ = s.AddMemory(ctx, db, AddMemoryParams{DomainID: arch.ID, Type: TypeFact, Text: "archived domain fact", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	if err := s.ArchiveDomain(ctx, db, arch.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Dry run reports counts without deleting: 1 retired memory + 1 archived
	// domain's memory = 2 memories, 1 domain.
	st, err := s.PurgeNonActive(ctx, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if st.Memories != 2 || st.Domains != 1 {
		t.Fatalf("dry run counts = %+v, want {2,1}", st)
	}
	// Nothing deleted yet.
	if doms, _ := s.ListDomains(ctx, db, StatusActive); len(doms) != 2 { // Keep + General
		t.Fatalf("dry run deleted domains: %d active remain", len(doms))
	}

	// Apply.
	st, err = s.PurgeNonActive(ctx, true)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if st.Memories != 2 || st.Domains != 1 {
		t.Fatalf("purge counts = %+v, want {2,1}", st)
	}

	// Only the active "Keep" memory + the seeded general remain; archived domain gone.
	all, _ := s.ListDomains(ctx, db) // all statuses
	for _, d := range all {
		if d.ID == arch.ID {
			t.Fatalf("archived domain survived purge")
		}
	}
	mems, _ := s.ListMemories(ctx, db, keep.ID) // all statuses
	if len(mems) != 1 || mems[0].Text != "active fact" {
		t.Fatalf("kept memories = %+v, want only 'active fact'", mems)
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

	h, err := s.AddMemory(ctx, s.DB(), AddMemoryParams{
		DomainID: base.ID, Type: TypeRule, Text: "Never use blue.",
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
	h2, err := s.SupersedeMemory(ctx, s.DB(), h.ID, AddMemoryParams{
		DomainID: base.ID, Type: TypeRule, Text: "Use blue for the layout.",
		Status: StatusActive, Confidence: 0.95, Source: SourceUserExplicit,
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	old, _ := s.GetMemory(ctx, s.DB(), h.ID)
	if old.Status != StatusRetired {
		t.Fatalf("old hook status = %q, want retired", old.Status)
	}
	if h2.SupersedesMemoryID == nil || *h2.SupersedesMemoryID != h.ID {
		t.Fatalf("supersede link not set on %s", h2.ID)
	}
	active, _ := s.ListMemories(ctx, s.DB(), base.ID, StatusActive)
	if len(active) != 1 || active[0].Text != "Use blue for the layout." {
		t.Fatalf("active hooks = %+v", active)
	}
}

func TestSearchAndPending(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _ := s.CreateDomain(ctx, s.DB(), CreateDomainParams{AgentID: "a", Type: DomainProject, Name: "P"})
	_, _ = s.AddMemory(ctx, s.DB(), AddMemoryParams{DomainID: d.ID, Type: TypeFact, Text: "The BioTech report targets Q3.", Status: StatusActive, Confidence: 0.9, Source: SourceUserExplicit})
	_, _ = s.AddMemory(ctx, s.DB(), AddMemoryParams{DomainID: d.ID, Type: TypeFact, Text: "Eric likes terse output.", Status: StatusReview, Confidence: 0.6, Source: SourceAssistantInferred})

	hits, err := s.SearchMemories(ctx, s.DB(), "biotech", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search biotech: %v hits=%d", err, len(hits))
	}
	// Review hook is not returned by active search.
	if h, _ := s.SearchMemories(ctx, s.DB(), "terse", 10); len(h) != 0 {
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

func TestDomainKeywordTriggers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), CreateDomainParams{
		AgentID: "a", Type: DomainProject, Name: "Briefing",
		KeywordTriggers: "  Morning Routine , weekly report ,, ",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Normalized: trimmed, lowercased, empties dropped.
	if d.KeywordTriggers != "morning routine,weekly report" {
		t.Fatalf("keyword_triggers = %q, want normalized", d.KeywordTriggers)
	}

	// Whole-phrase, word-boundary, case-insensitive matches.
	if kw, ok := d.MatchKeyword("Time for the MORNING ROUTINE, everyone"); !ok || kw != "morning routine" {
		t.Fatalf("MatchKeyword phrase = %q,%v", kw, ok)
	}
	if _, ok := d.MatchKeyword("please file the weekly report by noon"); !ok {
		t.Fatalf("expected 'weekly report' to match")
	}
	// The individual words must NOT match on their own (phrase only).
	if _, ok := d.MatchKeyword("good morning"); ok {
		t.Fatalf("bare 'morning' should not match the phrase 'morning routine'")
	}
	// No substring-in-word false positives.
	if _, ok := d.MatchKeyword("this is legitimate work"); ok {
		t.Fatalf("should not match inside another word")
	}

	// Update replaces; empty clears.
	empty := ""
	if err := s.UpdateDomain(ctx, s.DB(), d.ID, UpdateDomainParams{ExpectedVersion: 1, KeywordTriggers: &empty}); err != nil {
		t.Fatalf("update clear: %v", err)
	}
	got, _ := s.GetDomain(ctx, s.DB(), d.ID, false)
	if got.KeywordTriggers != "" || len(got.KeywordPhrases()) != 0 {
		t.Fatalf("keyword triggers not cleared: %q", got.KeywordTriggers)
	}
}
