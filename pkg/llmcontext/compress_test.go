// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// compressTestStore is a minimal in-memory SessionStore for compress tests.
type compressTestStore struct {
	history []providers.Message
	summary string
}

func (s *compressTestStore) GetHistory(_ string) []providers.Message {
	cp := make([]providers.Message, len(s.history))
	copy(cp, s.history)
	return cp
}

func (s *compressTestStore) SetHistory(_ string, h []providers.Message) {
	cp := make([]providers.Message, len(h))
	copy(cp, h)
	s.history = cp
}

func (s *compressTestStore) GetSummary(_ string) string        { return s.summary }
func (s *compressTestStore) SetSummary(_, v string)            { s.summary = v }
func (s *compressTestStore) Save(_ string) error                           { return nil }
func (s *compressTestStore) AddMessage(_, _, _ string)                     {}
func (s *compressTestStore) AddFullMessage(_ string, _ providers.Message)  {}
func (s *compressTestStore) TruncateHistory(_ string, _ int)               {}
func (s *compressTestStore) SetPendingTurn(_ string) error                 { return nil }
func (s *compressTestStore) ClearPendingTurn(_ string) error               { return nil }
func (s *compressTestStore) GetArchiveBounds(_ string) (int, int)          { return 0, 0 }
func (s *compressTestStore) Close() error                                  { return nil }

// mockLLM is a sequence-based LLM client for testing.
type mockLLM struct {
	responses []string
	errors    []error
	callCount int
}

func (m *mockLLM) Complete(_ context.Context, _ []providers.Message) (providers.Message, error) {
	i := m.callCount
	m.callCount++
	if i < len(m.errors) && m.errors[i] != nil {
		return providers.Message{}, m.errors[i]
	}
	resp := ""
	if i < len(m.responses) {
		resp = m.responses[i]
	}
	return providers.Message{Role: "assistant", Content: resp}, nil
}

// validSummaryJSON produces minimal valid Summary JSON.
func validSummaryJSON(goals string) string {
	return fmt.Sprintf(`{"version":1,"state":{"goals":%q},"covered_seq_start":0,"covered_seq_end":0}`, goals)
}

// makeConversation builds a slice of alternating user/assistant messages.
// Each message content is repeated to produce the desired approximate token count.
func makeConversation(pairs int, charsPerMessage int) []providers.Message {
	msgs := make([]providers.Message, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		msgs = append(msgs,
			providers.Message{Role: "user", Content: strings.Repeat("u", charsPerMessage)},
			providers.Message{Role: "assistant", Content: strings.Repeat("a", charsPerMessage)},
		)
	}
	return msgs
}

// newCompressManager builds a Manager wired for compress tests (no compressHook).
func newCompressManager(store *compressTestStore, clients []LLMClient, opts ...Option) *Manager {
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

// TestCompress_PrimarySuccess verifies that a single successful LLM client
// produces a stored summary and shrinks the history.
func TestCompress_PrimarySuccess(t *testing.T) {
	store := &compressTestStore{
		history: makeConversation(10, 200), // 10 pairs × 2 msgs × 200 chars = 4000 chars → ~1600 tokens
	}

	llm := &mockLLM{
		responses: []string{validSummaryJSON("test goal")},
	}

	mgr := newCompressManager(store, []LLMClient{llm})
	mgr.msgCount = len(store.history)

	err := mgr.doCompress(context.Background(), false)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if store.summary == "" {
		t.Error("expected summary to be stored")
	}
	if _, parseErr := unmarshalSummary(store.summary); parseErr != nil {
		t.Errorf("stored summary is not valid: %v", parseErr)
	}
	if len(store.history) >= 20 {
		t.Errorf("expected history to shrink; got %d messages", len(store.history))
	}
	if llm.callCount == 0 {
		t.Error("expected LLM to be called")
	}
}

// TestCompress_FallbackSuccess verifies that when the first client fails the
// second client is tried and a successful result is persisted.
func TestCompress_FallbackSuccess(t *testing.T) {
	store := &compressTestStore{
		history: makeConversation(10, 200),
	}

	primary := &mockLLM{
		errors: []error{errors.New("primary failed")},
	}
	fallback := &mockLLM{
		responses: []string{validSummaryJSON("fallback goal")},
	}

	mgr := newCompressManager(store, []LLMClient{primary, fallback})
	mgr.msgCount = len(store.history)

	err := mgr.doCompress(context.Background(), false)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if store.summary == "" {
		t.Error("expected summary to be stored via fallback client")
	}
	if primary.callCount == 0 {
		t.Error("expected primary client to be tried")
	}
	if fallback.callCount == 0 {
		t.Error("expected fallback client to be tried")
	}
}

// TestCompress_AllFail_Normal verifies that when all clients fail on a normal
// (non-safety-net) compression, nil is returned and history is unchanged.
func TestCompress_AllFail_Normal(t *testing.T) {
	origHistory := makeConversation(10, 200)
	store := &compressTestStore{history: origHistory}

	llm := &mockLLM{
		errors: []error{
			errors.New("fail1"),
			errors.New("fail2"),
			errors.New("fail3"),
			errors.New("fail4"), // extra to cover aggressive iterations
			errors.New("fail5"),
			errors.New("fail6"),
		},
	}

	mgr := newCompressManager(store, []LLMClient{llm})
	mgr.msgCount = len(store.history)

	err := mgr.doCompress(context.Background(), false)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if len(store.history) != len(origHistory) {
		t.Errorf("expected history unchanged (%d); got %d", len(origHistory), len(store.history))
	}
}

// TestCompress_AllFail_Safety_Drop verifies that when all clients fail on the
// safety-net path, oldest messages are dropped to reduce context size.
func TestCompress_AllFail_Safety_Drop(t *testing.T) {
	// Build a history large enough to exceed safetyPercent (80% of 10000 = 8000 tokens).
	// Each char ≈ 0.4 tokens; need >20000 chars. Use 40 pairs × 600 chars each.
	store := &compressTestStore{
		history: makeConversation(40, 600),
	}

	// All calls fail.
	errList := make([]error, 20)
	for i := range errList {
		errList[i] = errors.New("fail")
	}
	llm := &mockLLM{errors: errList}

	mgr := newCompressManager(store, []LLMClient{llm},
		WithContextWindow(10000),
		WithSafetyPercent(80),
		WithRetainMinMessages(2),
	)
	mgr.msgCount = len(store.history)
	origLen := len(store.history)

	err := mgr.doCompress(context.Background(), true)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if len(store.history) >= origLen {
		t.Errorf("expected messages to be dropped; history length unchanged at %d", len(store.history))
	}
}

// TestCompress_StaleSummary verifies that when all clients fail on the
// safety-net path and an existing summary is present, it is retained and
// oldest messages are still dropped to reduce context size.
func TestCompress_StaleSummary(t *testing.T) {
	store := &compressTestStore{
		history: makeConversation(40, 600),
		summary: validSummaryJSON("existing goal"),
	}

	errList := make([]error, 20)
	for i := range errList {
		errList[i] = errors.New("fail")
	}
	llm := &mockLLM{errors: errList}

	mgr := newCompressManager(store, []LLMClient{llm},
		WithContextWindow(10000),
		WithSafetyPercent(80),
		WithRetainMinMessages(2),
	)
	mgr.msgCount = len(store.history)
	origLen := len(store.history)

	err := mgr.doCompress(context.Background(), true)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	// History must have been trimmed.
	if len(store.history) >= origLen {
		t.Errorf("expected history to shrink; got %d (orig %d)", len(store.history), origLen)
	}

	// The stale summary must still be present (not cleared).
	if store.summary == "" {
		t.Error("expected stale summary to be retained")
	}
}

// TestCompress_NotifyCallback verifies that the notifyCallback is called with
// "compression started" and "compression complete" messages.
func TestCompress_NotifyCallback(t *testing.T) {
	store := &compressTestStore{
		history: makeConversation(10, 200),
	}

	llm := &mockLLM{
		responses: []string{validSummaryJSON("notify goal")},
	}

	var notifications []string
	mgr := newCompressManager(store, []LLMClient{llm},
		WithNotifyCallback(func(msg string) {
			notifications = append(notifications, msg)
		}),
	)
	mgr.msgCount = len(store.history)

	err := mgr.doCompress(context.Background(), false)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if len(notifications) < 2 {
		t.Fatalf("expected at least 2 notifications; got %d: %v", len(notifications), notifications)
	}
	if notifications[0] != "compression started" {
		t.Errorf("expected first notification 'compression started'; got %q", notifications[0])
	}
	found := false
	for _, n := range notifications {
		if n == "compression complete" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'compression complete' notification; got %v", notifications)
	}
}

// TestCompress_CoolingSetOnLowGain verifies that cooling is enabled when the
// LLM succeeds but the compression gain is below defaultMinCompressionGain
// and the final context percentage is still at or above normalPercent.
func TestCompress_CoolingSetOnLowGain(t *testing.T) {
	// Context window: 10000 tokens. normalPercent: 50 → trigger at 5000 tokens.
	// retainTokenPercent: 90 → budget = 9000 tokens; tail covers nearly everything,
	// so gain will be very small (tail ≈ whole conversation).
	// History: 10 pairs × 200 chars → ~1600 tokens → 16% — but we want to be above
	// normalPercent after "compression". We use retainTokenPercent=90 so the budget
	// is large, meaning the tail covers almost everything, giving near-zero gain,
	// but we need finalPct >= normalPercent (50%).
	//
	// Set contextWindow small so the same history is at/above normalPercent.
	// 10 pairs × 200 chars = 4000 chars → ~1600 tokens.
	// contextWindow = 2000: 1600/2000 = 80% → above normalPercent=50.
	// budget = 2000 * 90 / 100 = 1800 tokens → tail covers most messages.
	// gain ≈ tiny → cooling should be set.
	store := &compressTestStore{
		history: makeConversation(10, 200),
	}

	llm := &mockLLM{
		responses: []string{
			validSummaryJSON("low gain goal"),
			validSummaryJSON("low gain goal 2"),
			validSummaryJSON("low gain goal 3"),
		},
	}

	mgr := newCompressManager(store, []LLMClient{llm},
		WithContextWindow(2000),
		WithNormalPercent(50),
		WithSafetyPercent(90),
		WithRetainTokenPercent(90),
		WithRetainMinMessages(2),
	)
	mgr.msgCount = len(store.history)

	err := mgr.doCompress(context.Background(), false)
	if err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	if !mgr.cooling {
		// It's possible the LLM succeeded AND gain was sufficient; check the gain value.
		// Only fail if gain is genuinely low but cooling wasn't set.
		if mgr.lastCompressionGain < defaultMinCompressionGain {
			t.Errorf("expected cooling=true when gain (%.4f) < defaultMinCompressionGain (%.4f)",
				mgr.lastCompressionGain, defaultMinCompressionGain)
		}
		// If gain is large enough, cooling correctly stays false — test passes.
	}
}
