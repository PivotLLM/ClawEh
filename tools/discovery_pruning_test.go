package tools

import (
	"context"
	"testing"
)

// ttlOf reads a registered tool's current TTL (white-box; same package).
func ttlOf(t *testing.T, r *ToolRegistry, name string) int {
	t.Helper()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	return e.TTL
}

// Using a revealed tool resets its TTL to the promote baseline, so an actively
// used tool never idles out (the ttl_max "reset on use" contract).
func TestDiscovery_ResetOnUse(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHidden(&mockTool{name: "a"})
	r.PromoteTools([]string{"a"}, 3, 0)

	r.TickTTL()
	r.TickTTL()
	if got := ttlOf(t, r, "a"); got != 1 {
		t.Fatalf("after 2 ticks TTL=%d, want 1", got)
	}
	// Execute resets TTL back to the promote baseline.
	r.Execute(context.Background(), "a", nil)
	if got := ttlOf(t, r, "a"); got != 3 {
		t.Fatalf("after use TTL=%d, want 3 (reset on use)", got)
	}
}

// The visible budget prunes the lowest-remaining-TTL revealed tools first, and a
// just-promoted tool (at max TTL) is never the one evicted.
func TestDiscovery_VisibleBudgetEvictsLowestTTLFirst(t *testing.T) {
	r := NewToolRegistry()
	for _, n := range []string{"low", "mid", "high", "new"} {
		r.RegisterHidden(&mockTool{name: n})
	}
	r.PromoteTools([]string{"low"}, 1, 0)
	r.PromoteTools([]string{"mid"}, 5, 0)
	r.PromoteTools([]string{"high"}, 9, 0)

	// Promoting a 4th tool with budget 3 leaves 4 visible → evict the 1 lowest.
	r.PromoteTools([]string{"new"}, 7, 3)

	if got := ttlOf(t, r, "low"); got != 0 {
		t.Errorf("lowest-TTL tool should be evicted, TTL=%d", got)
	}
	for _, n := range []string{"mid", "high", "new"} {
		if got := ttlOf(t, r, n); got == 0 {
			t.Errorf("%s should remain visible, got TTL=0", n)
		}
	}
	if got := visibleCount(r); got != 3 {
		t.Errorf("visible count = %d, want 3 (the budget)", got)
	}
}

// Ties in remaining TTL are broken by name (ascending) for deterministic eviction.
func TestDiscovery_VisibleBudgetTieBreakByName(t *testing.T) {
	r := NewToolRegistry()
	for _, n := range []string{"aaa", "bbb", "keep"} {
		r.RegisterHidden(&mockTool{name: n})
	}
	r.PromoteTools([]string{"aaa"}, 2, 0)
	r.PromoteTools([]string{"bbb"}, 2, 0)
	// keep is promoted with a higher TTL and a budget of 2 → 3 visible, evict 1.
	// The two TTL=2 tools tie; "aaa" < "bbb" so "aaa" is evicted.
	r.PromoteTools([]string{"keep"}, 9, 2)

	if got := ttlOf(t, r, "aaa"); got != 0 {
		t.Errorf("aaa should be evicted (tie broken by name), TTL=%d", got)
	}
	if got := ttlOf(t, r, "bbb"); got == 0 {
		t.Error("bbb should survive the tie")
	}
	if got := ttlOf(t, r, "keep"); got == 0 {
		t.Error("keep should survive")
	}
}

// Always-on (core) tools count toward the visible budget: with the budget
// partly consumed by core tools, fewer revealed tools fit before pruning. Core
// tools are never evicted.
func TestDiscovery_VisibleBudgetCountsAlwaysOnTools(t *testing.T) {
	r := NewToolRegistry()
	// Two always-on (core) tools.
	r.Register(&mockTool{name: "core1"})
	r.Register(&mockTool{name: "core2"})
	for _, n := range []string{"r_lo", "r_mid", "r_hi"} {
		r.RegisterHidden(&mockTool{name: n})
	}
	r.PromoteTools([]string{"r_lo"}, 1, 0)
	r.PromoteTools([]string{"r_mid"}, 5, 0)
	// Budget 4: 2 core + 3 revealed = 5 visible → evict the 1 lowest-TTL revealed.
	r.PromoteTools([]string{"r_hi"}, 9, 4)

	if got := ttlOf(t, r, "r_lo"); got != 0 {
		t.Errorf("with 2 always-on tools counted, r_lo should be evicted at budget 4, TTL=%d", got)
	}
	for _, n := range []string{"r_mid", "r_hi"} {
		if ttlOf(t, r, n) == 0 {
			t.Errorf("%s should remain visible", n)
		}
	}
	// Core tools stay visible regardless of budget pressure.
	for _, n := range []string{"core1", "core2"} {
		if _, ok := r.Get(n); !ok {
			t.Errorf("always-on tool %s must never be evicted", n)
		}
	}
}

// When the always-on set alone meets the budget, every revealed tool is hidden.
func TestDiscovery_VisibleBudgetAlwaysOnAtCapEvictsAllRevealed(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "core1"})
	r.Register(&mockTool{name: "core2"})
	r.RegisterHidden(&mockTool{name: "r1"})
	// Budget 2, already 2 core → the revealed tool cannot fit.
	r.PromoteTools([]string{"r1"}, 9, 2)
	if got := ttlOf(t, r, "r1"); got != 0 {
		t.Errorf("revealed tool should be evicted when core fills the budget, TTL=%d", got)
	}
}

// A budget of 0 (or negative) disables pruning entirely.
func TestDiscovery_VisibleBudgetDisabled(t *testing.T) {
	r := NewToolRegistry()
	for _, n := range []string{"t1", "t2", "t3"} {
		r.RegisterHidden(&mockTool{name: n})
		r.PromoteTools([]string{n}, 5, 0)
	}
	if got := visibleCount(r); got != 3 {
		t.Errorf("no budget should keep all visible, count=%d want 3", got)
	}
}

// A reveal-together group unlocks every member when any one is promoted; tools in
// other groups (or ungrouped) are unaffected.
func TestDiscovery_RevealTogetherPromotesWholeGroup(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHiddenGroup(&mockTool{name: "svc_a"}, "svc", true)
	r.RegisterHiddenGroup(&mockTool{name: "svc_b"}, "svc", true)
	r.RegisterHiddenGroup(&mockTool{name: "svc_c"}, "svc", true)
	r.RegisterHidden(&mockTool{name: "other"})

	r.PromoteTools([]string{"svc_a"}, 5, 0)

	for _, n := range []string{"svc_a", "svc_b", "svc_c"} {
		if got := ttlOf(t, r, n); got != 5 {
			t.Errorf("%s should be revealed with the group, TTL=%d want 5", n, got)
		}
	}
	if got := ttlOf(t, r, "other"); got != 0 {
		t.Errorf("ungrouped tool must not be revealed, TTL=%d", got)
	}
}

// Without the reveal-together flag, promoting one group member leaves the rest
// hidden (the default, all-or-nothing off).
func TestDiscovery_GroupWithoutRevealTogetherIsIndependent(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHiddenGroup(&mockTool{name: "g_a"}, "g", false)
	r.RegisterHiddenGroup(&mockTool{name: "g_b"}, "g", false)

	r.PromoteTools([]string{"g_a"}, 5, 0)

	if got := ttlOf(t, r, "g_a"); got != 5 {
		t.Errorf("g_a should be revealed, TTL=%d", got)
	}
	if got := ttlOf(t, r, "g_b"); got != 0 {
		t.Errorf("g_b should stay hidden without reveal_together, TTL=%d", got)
	}
}

// visibleCount counts revealed (non-core, TTL>0) tools.
func visibleCount(r *ToolRegistry) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, e := range r.tools {
		if !e.IsCore && e.TTL > 0 {
			n++
		}
	}
	return n
}
