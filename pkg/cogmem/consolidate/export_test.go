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
		AgentID: "alice", Type: store.DomainProject, Name: "ClawEh",
		Status: store.StatusActive, Summary: "Go gateway",
	})
	if err != nil {
		t.Fatalf("create domain: %v", err)
	}
	if _, err := s.AddHook(ctx, s.DB(), store.AddHookParams{
		DomainID: d.ID, Kind: store.KindRule, Text: "Run make test.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
	}); err != nil {
		t.Fatalf("add active hook: %v", err)
	}
	if _, err := s.AddHook(ctx, s.DB(), store.AddHookParams{
		DomainID: d.ID, Kind: store.KindFact, Text: "Eric prefers terse output.",
		Status: store.StatusReview, Confidence: 0.6, Source: store.SourceAssistantInferred,
	}); err != nil {
		t.Fatalf("add review hook: %v", err)
	}

	dir := t.TempDir()
	if err := WriteExport(ctx, s, dir); err != nil {
		t.Fatalf("WriteExport: %v", err)
	}

	projects := filepath.Join(dir, "GENERATED_PROJECTS.md")
	body, err := os.ReadFile(projects)
	if err != nil {
		t.Fatalf("read projects export: %v", err)
	}
	if !strings.HasPrefix(string(body), "<!-- DO NOT EDIT") {
		t.Fatalf("projects export missing banner: %q", string(body)[:40])
	}
	if !strings.Contains(string(body), "Run make test.") {
		t.Fatalf("projects export missing hook text")
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

	// Empty sections (lessons / user_learned) are skipped.
	if _, err := os.Stat(filepath.Join(dir, "GENERATED_LESSONS.md")); !os.IsNotExist(err) {
		t.Fatalf("empty lessons file should not be written")
	}
}
