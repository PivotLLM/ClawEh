// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// newCheckCompressManager builds a Manager for CheckAndCompress tests.
func newCheckCompressManager(store *mockStore, clients []LLMClient, opts ...Option) *Manager {
	baseOpts := []Option{
		WithContextWindow(10000),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithRetainTokenPercent(20),
		WithRetainMinMessages(2),
		WithCompressLLM(clients...),
	}
	baseOpts = append(baseOpts, opts...)
	cm := New("sess", store, nil, nil, baseOpts...)
	return cm.(*Manager)
}

// TestCheckAndCompress_BelowThreshold verifies that CheckAndCompress returns the
// input slice unchanged when tokens (including overhead) are below the normal threshold.
func TestCheckAndCompress_BelowThreshold(t *testing.T) {
	store := newMockStore()
	// 4 chars × 100 = 400 chars → 100 tokens of content; overheadTokens = 200 → 300 total.
	// contextWindow = 10000 with normalPercent = 50 → threshold at 5000 tokens.
	// 300 tokens is well below 5000, so no compression.
	small := []providers.Message{
		{Role: "user", Content: strings.Repeat("a", 400)},
	}
	store.SetHistory("sess", small)

	compressed := false
	mgr := newCheckCompressManager(store, nil,
		WithContextWindow(10000),
		WithNormalPercent(50),
		WithOverheadTokens(200),
	)
	mgr.compressHook = func(_ bool) { compressed = true }
	mgr.msgCount = 1

	built := []providers.Message{{Role: "user", Content: "hello"}}
	result, err := mgr.CheckAndCompress(context.Background(), built)
	if err != nil {
		t.Fatalf("CheckAndCompress returned error: %v", err)
	}
	if compressed {
		t.Error("expected no compression to fire")
	}
	if len(result) != len(built) {
		t.Errorf("expected input slice unchanged (len %d), got len %d", len(built), len(result))
	}
}

// TestCheckAndCompress_OverheadPushesOverThreshold verifies that when stored history
// is within the normal threshold but adding overheadTokens pushes it over,
// compression fires at the CheckAndCompress call.
func TestCheckAndCompress_OverheadPushesOverThreshold(t *testing.T) {
	store := newMockStore()

	// contextWindow = 1000. normalPercent = 50 → trigger at 500 tokens.
	// Built messages: 1600 chars → 400 tokens from content.
	// overheadTokens = 200 → total = 600 tokens = 60% of 1000 → above normalPercent.
	built := []providers.Message{
		{Role: "user", Content: strings.Repeat("b", 1600)},
	}
	store.SetHistory("sess", built)

	var hookCalls []bool
	mgr := newCheckCompressManager(store, nil,
		WithContextWindow(1000),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithOverheadTokens(200),
	)
	mgr.compressHook = func(safetyNet bool) { hookCalls = append(hookCalls, safetyNet) }
	mgr.msgCount = 1

	_, err := mgr.CheckAndCompress(context.Background(), built)
	// err may be nil (hook short-circuits) or ErrCompressionFailed — the important
	// thing is that the hook was called, proving compression was triggered.
	_ = err

	if len(hookCalls) == 0 {
		t.Fatal("expected compression to fire when overhead pushes total over normal threshold")
	}
}

// TestCheckAndCompress_NoContextWindow returns input unchanged when contextWindow == 0.
func TestCheckAndCompress_NoContextWindow(t *testing.T) {
	store := newMockStore()
	mgr := newCheckCompressManager(store, nil,
		WithContextWindow(0),
	)
	built := makeConversation(5, 500)
	result, err := mgr.CheckAndCompress(context.Background(), built)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(built) {
		t.Errorf("expected input unchanged (len %d); got %d", len(built), len(result))
	}
}

// TestCheckAndCompress_CompressionFiredReturnsFreshSlice verifies that when
// compression fires and succeeds (via a real LLM client, not hook), the returned
// slice is the fresh Build() output rather than the stale input.
func TestCheckAndCompress_CompressionFiredReturnsFreshSlice(t *testing.T) {
	store := newMockStore()

	// contextWindow = 1000. normalPercent = 50 → trigger at 500 tokens.
	// Built: 2000 chars → 500 tokens. overheadTokens = 100 → 600 tokens → 60% → triggers.
	history := makeConversation(5, 200)
	store.SetHistory("sess", history)

	llm := &mockLLM{
		responses: []string{validSummaryJSON("fresh slice goal")},
	}

	mgr := newCheckCompressManager(store, []LLMClient{llm},
		WithContextWindow(1000),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithOverheadTokens(100),
	)
	mgr.msgCount = len(history)

	built := make([]providers.Message, len(history))
	copy(built, history)

	result, err := mgr.CheckAndCompress(context.Background(), built)
	if err != nil {
		t.Fatalf("CheckAndCompress returned unexpected error: %v", err)
	}

	// After compression the store has fewer messages; Build() produces a shorter slice.
	if len(result) >= len(built) {
		t.Errorf("expected fresh (shorter) slice after compression; got len %d (input len %d)",
			len(result), len(built))
	}
}

// TestCheckAndCompress_CompressFail_ReturnsStaleSlice verifies that when
// compression fails (LLM unavailable on normal path), CheckAndCompress returns the
// stale input and ErrCompressionFailed.
func TestCheckAndCompress_CompressFail_ReturnsStaleSlice(t *testing.T) {
	store := newMockStore()

	// Between normal (50%) and safety (80%): 600 tokens out of 1000 = 60%.
	// 2400 chars → 600 tokens content; overheadTokens=0 → 600 tokens → 60% → normal trigger.
	history := makeConversation(6, 200)
	store.SetHistory("sess", history)

	failErrors := make([]error, 20)
	for i := range failErrors {
		failErrors[i] = errors.New("llm down")
	}
	llm := &mockLLM{errors: failErrors}

	mgr := newCheckCompressManager(store, []LLMClient{llm},
		WithContextWindow(1000),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithOverheadTokens(0),
	)
	mgr.msgCount = len(history)

	built := make([]providers.Message, len(history))
	copy(built, history)

	result, err := mgr.CheckAndCompress(context.Background(), built)
	if !errors.Is(err, ErrCompressionFailed) {
		t.Errorf("expected ErrCompressionFailed; got %v", err)
	}
	if len(result) != len(built) {
		t.Errorf("expected stale slice (len %d) on failure; got len %d", len(built), len(result))
	}
}

// TestCheckAndCompress_ToolCallsCountedInTokens verifies that ToolCalls argument
// content is included in the token estimate, not just Message.Content.
func TestCheckAndCompress_ToolCallsCountedInTokens(t *testing.T) {
	// A message with empty Content but large ToolCalls arguments.
	// 4000 chars in Arguments → ~1000 tokens. contextWindow = 1000, normalPercent = 50
	// → threshold at 500 tokens. 1000 tokens > 500 → should trigger compression.
	largeArgs := strings.Repeat("x", 4000)
	msgs := []providers.Message{
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []providers.ToolCall{
				{ID: "tc1", Function: &providers.FunctionCall{
					Name:      "big_tool",
					Arguments: largeArgs,
				}},
			},
		},
	}

	tokenCount := estimateTokens(msgs)
	// ~1000 tokens from tool call arguments + overhead from JSON structure.
	if tokenCount < 900 {
		t.Errorf("expected ToolCalls arguments to be counted; got only %d tokens", tokenCount)
	}
}

// TestCheckAndCompress_CooldownPreventsDoubleFire verifies that when cooling is true,
// the normal trigger is suppressed (preventing double-firing after PreDispatchCheck).
func TestCheckAndCompress_CooldownPreventsDoubleFire(t *testing.T) {
	store := newMockStore()
	// Above normalPercent (50%) at 60% with no overhead.
	history := makeConversation(6, 200) // 2400 chars → 600 tokens → 60% of 1000
	store.SetHistory("sess", history)

	compressed := false
	mgr := newCheckCompressManager(store, nil,
		WithContextWindow(1000),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithOverheadTokens(0),
	)
	mgr.compressHook = func(_ bool) { compressed = true }
	mgr.msgCount = len(history)

	// Set cooling — simulates that PreDispatchCheck already compressed this turn.
	mgr.cooling = true
	mgr.coolingSinceCount = mgr.msgCount

	built := make([]providers.Message, len(history))
	copy(built, history)

	_, err := mgr.CheckAndCompress(context.Background(), built)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if compressed {
		t.Error("expected compression to be suppressed by cooldown")
	}
}
