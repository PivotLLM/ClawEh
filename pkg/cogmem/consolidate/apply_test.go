// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

func TestApplySupersedeEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.cogmem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Seed: a project domain with a rule hook.
	d, _ := st.CreateDomain(ctx, st.DB(), store.CreateDomainParams{
		AgentID: "alice", Type: store.DomainProject, Name: "Layout", Status: store.StatusActive,
	})
	h, _ := st.AddHook(ctx, st.DB(), store.AddHookParams{
		DomainID: d.ID, Kind: store.KindRule, Text: "Never use the color blue.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
	})

	out := Output{
		HookOps: []HookOp{{
			Op: "supersede", OldID: h.ID, Domain: d.ID, Kind: "rule",
			Text: "Use blue for the layout.", Confidence: 0.95, Status: "active",
			Source: "user_explicit", Evidence: store.Evidence{SeqStart: 512, SeqEnd: 512},
		}},
		ConflictLedger: []LedgerEntry{{Resolved: "swapped blue rule", Reason: "user said so", Evidence: store.Evidence{SeqStart: 512, SeqEnd: 512}}},
	}

	n, err := Apply(ctx, st, out, ApplyContext{AgentID: "alice", SessionKey: "agent:alice:main", Actor: "sleep_cycle"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n != 1 {
		t.Fatalf("applied = %d, want 1", n)
	}
	old, _ := st.GetHook(ctx, st.DB(), h.ID)
	if old.Status != store.StatusRetired {
		t.Fatalf("old hook status = %q, want retired", old.Status)
	}
	active, _ := st.ListHooks(ctx, st.DB(), d.ID, store.StatusActive)
	if len(active) != 1 || active[0].Text != "Use blue for the layout." {
		t.Fatalf("active hooks = %+v", active)
	}
}

func TestApplyCreateWithTmpID(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "b.cogmem.db"))
	defer st.Close()

	out := Output{
		DomainOps: []DomainOp{{Op: "create", TmpID: "t1", Type: "project", Name: "New Project", Summary: "x", Status: "active", Evidence: store.Evidence{SeqStart: 1, SeqEnd: 1}}},
		HookOps:   []HookOp{{Op: "add", Domain: "t1", Kind: "fact", Text: "a durable fact", Confidence: 0.9, Status: "active", Source: "user_explicit", Evidence: store.Evidence{SeqStart: 1, SeqEnd: 1}}},
	}
	n, err := Apply(ctx, st, out, ApplyContext{AgentID: "alice", Actor: "sleep_cycle"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n != 2 {
		t.Fatalf("applied = %d, want 2", n)
	}
	doms, _ := st.ListDomains(ctx, st.DB(), store.StatusActive)
	if len(doms) != 1 || doms[0].Name != "New Project" {
		t.Fatalf("domains = %+v", doms)
	}
	hooks, _ := st.ListHooks(ctx, st.DB(), doms[0].ID, store.StatusActive)
	if len(hooks) != 1 || hooks[0].Text != "a durable fact" {
		t.Fatalf("hooks = %+v", hooks)
	}
}
