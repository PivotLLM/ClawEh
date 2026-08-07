// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// fakeLoader serves attachments from an in-memory map, recording every call so
// tests can assert a shared document is loaded exactly once.
type fakeLoader struct {
	files map[string]string
	calls []string
}

func (f *fakeLoader) load(ref string, maxBytes int) (Attachment, error) {
	f.calls = append(f.calls, ref)
	body, ok := f.files[ref]
	if !ok {
		return Attachment{}, errors.New("access denied: outside the agent's readable paths")
	}
	att := Attachment{Ref: ref, Content: body, Size: int64(len(body))}
	if maxBytes > 0 && len(body) > maxBytes {
		att.Content = body[:maxBytes]
		att.Truncated = true
	}
	return att, nil
}

func composeWith(t *testing.T, s *store.Store, opts ...Option) Result {
	t.Helper()
	res, err := New(s, opts...).Compose(context.Background(), RouteRequest{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return res
}

func TestAttachmentFromStickyMemory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	m, err := s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeRule, Text: "Write in my voice.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "files/voice.md",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	fl := &fakeLoader{files: map[string]string{"files/voice.md": "# Voice\n\nShort sentences.\n"}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if !strings.Contains(res.Stable, "[attached document: files/voice.md]") {
		t.Fatalf("stable block missing attachment marker:\n%s", res.Stable)
	}
	for _, want := range []string{"# Attached Documents", "## files/voice.md", "memory " + m.ID, "Short sentences."} {
		if !strings.Contains(res.Attachments, want) {
			t.Fatalf("attachments block missing %q:\n%s", want, res.Attachments)
		}
	}
}

func TestAttachmentFromRoutedDomain(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	d, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Name: "Writing"})
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: d.ID, Type: store.TypeRule, Text: "Voice guide.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "maestro/style.md",
	})

	fl := &fakeLoader{files: map[string]string{"maestro/style.md": "mount-sourced doc"}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if !strings.Contains(res.Routed, "[attached document: maestro/style.md]") {
		t.Fatalf("routed block missing marker:\n%s", res.Routed)
	}
	if !strings.Contains(res.Attachments, "mount-sourced doc") {
		t.Fatalf("attachments block missing mount content:\n%s", res.Attachments)
	}
}

func TestAttachmentDedupedAcrossMemories(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	for i := 0; i < 3; i++ {
		_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
			DomainID: gen.ID, Type: store.TypeFact, Text: fmt.Sprintf("note %d", i),
			Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
			FileRef: "files/voice.md",
		})
	}

	fl := &fakeLoader{files: map[string]string{"files/voice.md": "BODY"}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if len(fl.calls) != 1 {
		t.Fatalf("expected one load for a shared document, got %v", fl.calls)
	}
	if n := strings.Count(res.Attachments, "BODY"); n != 1 {
		t.Fatalf("expected document injected once, got %d copies:\n%s", n, res.Attachments)
	}
}

func TestAttachmentTruncationIsAnnounced(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeRule, Text: "big doc",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "files/big.md",
	})

	fl := &fakeLoader{files: map[string]string{"files/big.md": strings.Repeat("x", 100)}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load), WithFileMaxBytes(10))

	if !strings.Contains(res.Attachments, "TRUNCATED: showing the first 10 of 100 bytes") {
		t.Fatalf("expected truncation notice:\n%s", res.Attachments)
	}
}

func TestAttachmentBudgetExhaustionIsAnnounced(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	// Priority orders the render, so "a" is loaded first and eats the budget.
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeFact, Text: "first", Priority: 2,
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "files/a.md",
	})
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeFact, Text: "second", Priority: 1,
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "files/b.md",
	})

	fl := &fakeLoader{files: map[string]string{
		"files/a.md": strings.Repeat("a", 50),
		"files/b.md": "never included",
	}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load), WithFileTotalMaxBytes(50))

	if strings.Contains(res.Attachments, "never included") {
		t.Fatalf("second document should not fit the budget:\n%s", res.Attachments)
	}
	if !strings.Contains(res.Attachments, "attachment budget (50 bytes) is exhausted") {
		t.Fatalf("expected budget notice:\n%s", res.Attachments)
	}
}

func TestUnreadableAttachmentIsReportedNotSilent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeRule, Text: "voice",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "/etc/shadow.md",
	})

	fl := &fakeLoader{files: map[string]string{}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if !strings.Contains(res.Attachments, "Not included: access denied") {
		t.Fatalf("expected an explicit unavailable note:\n%s", res.Attachments)
	}
}

func TestPendingMemoryDocumentIsNamedButNotLoaded(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeRule, Text: "maybe my voice",
		Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred,
		FileRef: "files/voice.md",
	})

	fl := &fakeLoader{files: map[string]string{"files/voice.md": "BODY"}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if !strings.Contains(res.Stable, "[attached document: files/voice.md]") {
		t.Fatalf("pending memory should still name its document:\n%s", res.Stable)
	}
	if len(fl.calls) != 0 {
		t.Fatalf("unconfirmed memory must not load its document, got %v", fl.calls)
	}
}

func TestNoLoaderMeansMarkersOnly(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypeRule, Text: "voice",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
		FileRef: "files/voice.md",
	})

	res := composeWith(t, s)

	if res.Attachments != "" {
		t.Fatalf("no loader configured, expected no attachments block:\n%s", res.Attachments)
	}
	if !strings.Contains(res.Stable, "[attached document: files/voice.md]") {
		t.Fatalf("marker should still render:\n%s", res.Stable)
	}
}

func TestMemoryWithoutFileRefAddsNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	gen, _ := s.GeneralDomain(ctx, db)
	_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
		DomainID: gen.ID, Type: store.TypePreference, Text: "Be concise.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
	})

	fl := &fakeLoader{files: map[string]string{}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load))

	if res.Attachments != "" || len(fl.calls) != 0 {
		t.Fatalf("plain memory must not trigger attachment work: %q %v", res.Attachments, fl.calls)
	}
	if strings.Contains(res.Stable, "attached document") {
		t.Fatalf("unexpected marker:\n%s", res.Stable)
	}
}

func TestDroppedRoutedDomainDoesNotAttach(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	db := s.DB()
	// Two topic domains; maxChars is tight enough that only the first section fits.
	for _, name := range []string{"First", "Second"} {
		d, _ := s.CreateDomain(ctx, db, store.CreateDomainParams{AgentID: "a", Name: name,
			Summary: strings.Repeat("summary ", 20)})
		_, _ = s.AddMemory(ctx, db, store.AddMemoryParams{
			DomainID: d.ID, Type: store.TypeFact, Text: name + " note",
			Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
			FileRef: "files/" + strings.ToLower(name) + ".md",
		})
	}

	fl := &fakeLoader{files: map[string]string{"files/first.md": "A", "files/second.md": "B"}}
	res := composeWith(t, s, WithAttachmentLoader(fl.load), WithMaxChars(200), WithTopKDomains(5))

	if len(res.Loaded) != 1 {
		t.Fatalf("expected the char budget to drop a domain, loaded=%v", res.Loaded)
	}
	if len(fl.calls) != 1 {
		t.Fatalf("only the rendered domain's document should load, got %v", fl.calls)
	}
}
