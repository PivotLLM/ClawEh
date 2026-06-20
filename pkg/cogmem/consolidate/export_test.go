// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

func TestWriteExport(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	d, err := s.CreateDomain(ctx, s.DB(), store.CreateDomainParams{
		AgentID: "alice", Name: "ClawEh",
		Status: store.StatusActive, Summary: "Go gateway",
	})
	if err != nil {
		t.Fatalf("create domain: %v", err)
	}
	if _, err := s.AddMemory(ctx, s.DB(), store.AddMemoryParams{
		DomainID: d.ID, Type: store.TypeRule, Text: "Run make test.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
	}); err != nil {
		t.Fatalf("add active hook: %v", err)
	}
	if _, err := s.AddMemory(ctx, s.DB(), store.AddMemoryParams{
		DomainID: d.ID, Type: store.TypeFact, Text: "Eric prefers terse output.",
		Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred,
	}); err != nil {
		t.Fatalf("add review hook: %v", err)
	}

	dir := t.TempDir()
	if err := WriteExport(ctx, s, dir); err != nil {
		t.Fatalf("WriteExport: %v", err)
	}

	// The non-sticky "ClawEh" domain lands in the topics export.
	topics := filepath.Join(dir, "GENERATED_TOPICS.md")
	body, err := os.ReadFile(topics)
	if err != nil {
		t.Fatalf("read topics export: %v", err)
	}
	if !strings.HasPrefix(string(body), "<!-- DO NOT EDIT") {
		t.Fatalf("topics export missing banner: %q", string(body)[:40])
	}
	if !strings.Contains(string(body), "Run make test.") {
		t.Fatalf("topics export missing hook text")
	}

	// The seeded sticky "General" domain lands in the sticky export.
	stickyBody, err := os.ReadFile(filepath.Join(dir, "GENERATED_STICKY.md"))
	if err != nil {
		t.Fatalf("read sticky export: %v", err)
	}
	if !strings.Contains(string(stickyBody), "General") {
		t.Fatalf("sticky export missing the General domain")
	}

	pendingBody, err := os.ReadFile(filepath.Join(dir, "GENERATED_PENDING.md"))
	if err != nil {
		t.Fatalf("read pending export: %v", err)
	}
	if !strings.HasPrefix(string(pendingBody), "<!-- DO NOT EDIT") {
		t.Fatalf("pending export missing banner")
	}
	if !strings.Contains(string(pendingBody), "Eric prefers terse output.") {
		t.Fatalf("pending export missing review hook")
	}
}
