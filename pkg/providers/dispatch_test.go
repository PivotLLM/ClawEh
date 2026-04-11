// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers_test

import (
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// minimalCfg builds a *config.Config with one ModelConfig entry that will
// successfully create a claude-cli provider (no API key required).
func minimalCfg(modelKey string) *config.Config {
	return &config.Config{
		ModelList: []config.ModelConfig{
			{
				ModelName: "test-alias",
				Model:     modelKey,
				Enabled:   true,
			},
		},
	}
}

// TestProviderDispatcher_Get_CachesInstance verifies that calling Get twice with
// the same protocol+modelID returns the exact same provider instance.
func TestProviderDispatcher_Get_CachesInstance(t *testing.T) {
	cfg := minimalCfg("claude-cli/test")
	d := providers.NewProviderDispatcher(cfg)

	p1, err := d.Get("claude-cli", "test")
	if err != nil {
		t.Fatalf("first Get: unexpected error: %v", err)
	}
	if p1 == nil {
		t.Fatal("first Get: returned nil provider")
	}

	p2, err := d.Get("claude-cli", "test")
	if err != nil {
		t.Fatalf("second Get: unexpected error: %v", err)
	}

	if p1 != p2 {
		t.Errorf("expected cached provider instance, got different pointers: %p vs %p", p1, p2)
	}
}

// TestProviderDispatcher_Get_UnknownProtocol verifies that Get returns an error
// when no ModelConfig entry matches the requested protocol+modelID.
func TestProviderDispatcher_Get_UnknownProtocol(t *testing.T) {
	cfg := minimalCfg("claude-cli/test")
	d := providers.NewProviderDispatcher(cfg)

	_, err := d.Get("unknown-protocol", "no-such-model")
	if err == nil {
		t.Fatal("expected error for unknown protocol/model, got nil")
	}
}

// TestProviderDispatcher_Flush verifies that Flush clears the cache so that a
// subsequent Get creates a new provider instance rather than returning the old one.
func TestProviderDispatcher_Flush(t *testing.T) {
	cfg := minimalCfg("claude-cli/test")
	d := providers.NewProviderDispatcher(cfg)

	p1, err := d.Get("claude-cli", "test")
	if err != nil {
		t.Fatalf("pre-flush Get: %v", err)
	}

	// Flush with the same config (simulating a reload).
	d.Flush(cfg)

	p2, err := d.Get("claude-cli", "test")
	if err != nil {
		t.Fatalf("post-flush Get: %v", err)
	}

	if p1 == p2 {
		t.Error("expected new provider instance after Flush, but got the same pointer")
	}
}

// TestProviderDispatcher_Get_ThreadSafe exercises concurrent Gets to verify
// there are no data races. Run with: go test -race ./pkg/providers/...
func TestProviderDispatcher_Get_ThreadSafe(t *testing.T) {
	cfg := minimalCfg("claude-cli/concurrent")
	d := providers.NewProviderDispatcher(cfg)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			p, err := d.Get("claude-cli", "concurrent")
			if err != nil {
				t.Errorf("concurrent Get: %v", err)
				return
			}
			// Exercise the provider slightly to ensure no race on the cached value.
			_ = p.GetDefaultModel()
		}()
	}

	wg.Wait()
}

// TestProviderDispatcher_Get_FlushRace exercises concurrent Gets and Flushes
// together to verify the mutex correctly protects both operations.
func TestProviderDispatcher_Get_FlushRace(t *testing.T) {
	cfg := minimalCfg("claude-cli/race")
	d := providers.NewProviderDispatcher(cfg)

	var wg sync.WaitGroup

	// Half goroutines call Get, half call Flush.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.Get("claude-cli", "race")
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Flush(cfg)
		}()
	}

	wg.Wait()
}

// Compile-time check: claude-cli provider satisfies LLMProvider.
var _ providers.LLMProvider = (func() providers.LLMProvider {
	p, _, _ := providers.CreateProviderFromConfig(&config.ModelConfig{Model: "claude-cli/x"})
	return p
})()

// TestClaudeCliProvider_Chat is a lightweight smoke test confirming the
// claude-cli provider (used in dispatcher tests) satisfies the interface.
func TestClaudeCliProvider_Chat(t *testing.T) {
	cfg := minimalCfg("claude-cli/smoke")
	d := providers.NewProviderDispatcher(cfg)

	p, err := d.Get("claude-cli", "smoke")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p == nil {
		t.Fatal("Get: returned nil provider")
	}
}

// TestProviderDispatcher_SingleCreationUnderConcurrentLoad verifies that even
// when many goroutines race past the initial read-lock check simultaneously,
// the provider is created exactly once and all callers receive the same instance.
func TestProviderDispatcher_SingleCreationUnderConcurrentLoad(t *testing.T) {
	cfg := minimalCfg("claude-cli/concurrent-load")
	d := providers.NewProviderDispatcher(cfg)

	const goroutines = 50
	results := make([]providers.LLMProvider, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			p, err := d.Get("claude-cli", "concurrent-load")
			if err != nil {
				t.Errorf("goroutine %d: Get() error: %v", i, err)
				return
			}
			results[i] = p
		}()
	}

	wg.Wait()

	// All goroutines must have received a non-nil provider.
	for i, p := range results {
		if p == nil {
			t.Errorf("goroutine %d: received nil provider", i)
		}
	}

	// All returned providers must point to the same instance.
	first := results[0]
	for i, p := range results[1:] {
		if p != first {
			t.Errorf("goroutine %d: got different provider instance (%p) vs first (%p)", i+1, p, first)
		}
	}
}
