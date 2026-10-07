package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Deleting a provider used to corrupt the ones after it. LoadConfig starts from
// DefaultConfig() and unmarshals the file over it, and Go's decoder reuses slice
// elements by index — so once a deletion shifted everything down, each provider
// was decoded onto a different default and inherited whichever omitempty flags
// that default set.
func TestLoadConfig_DeletingAProviderDoesNotAlterTheOthers(t *testing.T) {
	cfg := DefaultConfig()
	var kept []Provider
	for _, p := range cfg.Providers {
		if p.Name == "OpenAI" {
			continue
		}
		kept = append(kept, p)
	}
	cfg.Providers = kept
	cfg.Models = nil

	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Providers) != len(kept) {
		t.Fatalf("loaded %d providers, saved %d", len(back.Providers), len(kept))
	}
	for i, want := range kept {
		got := back.Providers[i]
		if got != want {
			t.Errorf("provider %d (%s) changed across save/load:\n got %+v\nwant %+v", i, want.Name, got, want)
		}
	}
}

// The flags are the part that used to leak, so check them by name: each
// provider loads with exactly the strict_compat, no_parallel_tool_calls and
// require_reasoning_content it was saved with.
func TestLoadConfig_ProviderFlagsDoNotLeakToNeighbours(t *testing.T) {
	cfg := DefaultConfig()
	var kept []Provider
	for _, p := range cfg.Providers {
		if p.Name == "OpenAI" {
			continue
		}
		kept = append(kept, p)
	}
	cfg.Providers = kept
	cfg.Models = nil

	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := make(map[string]Provider, len(kept))
	for _, p := range kept {
		saved[p.Name] = p
	}
	for _, p := range back.Providers {
		want := saved[p.Name]
		if p.StrictCompat != want.StrictCompat {
			t.Errorf("%s: strict_compat = %v, saved %v", p.Name, p.StrictCompat, want.StrictCompat)
		}
		if p.NoParallelToolCalls != want.NoParallelToolCalls {
			t.Errorf("%s: no_parallel_tool_calls = %v, saved %v", p.Name, p.NoParallelToolCalls, want.NoParallelToolCalls)
		}
		if p.RequireReasoningContent != want.RequireReasoningContent {
			t.Errorf("%s: require_reasoning_content = %v, saved %v", p.Name, p.RequireReasoningContent, want.RequireReasoningContent)
		}
	}
}

// An empty providers list must still load: it means "no providers", not "use
// the seeded catalogue". A user who deletes them all should not find them back.
func TestLoadConfig_EmptyProviderListIsHonoured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"providers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Providers) != 0 {
		t.Errorf("an explicit empty list loaded %d providers", len(back.Providers))
	}
}
