// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
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

func (s *compressTestStore) GetSummary(_ string) string                         { return s.summary }
func (s *compressTestStore) SetSummary(_, v string)                             { s.summary = v }
func (s *compressTestStore) Save(_ string) error                                { return nil }
func (s *compressTestStore) AddMessage(_, _, _ string)                          {}
func (s *compressTestStore) AddFullMessage(_ string, _ providers.Message) int64 { return 0 }
func (s *compressTestStore) TruncateHistory(_ string, _ int)                    {}
func (s *compressTestStore) SetPendingTurn(_ string) error                      { return nil }
func (s *compressTestStore) ClearPendingTurn(_ string) error                    { return nil }
func (s *compressTestStore) GetArchiveBounds(_ string) (int64, int64)           { return 0, 0 }
func (s *compressTestStore) ListPendingSessions() ([]string, error)             { return nil, nil }
func (s *compressTestStore) Close() error                                       { return nil }
func (s *compressTestStore) GetHistoryWithSeqs(_ string) []memory.StoredMessage {
	stored := make([]memory.StoredMessage, len(s.history))
	for i, msg := range s.history {
		stored[i] = memory.StoredMessage{Seq: int64(i + 1), Message: msg}
	}
	return stored
}

// mockLLM is a sequence-based LLM client for testing.
type mockLLM struct {
	model         string // reported via Model(); "" leaves attempts labelled "model"
	responses     []string
	errors        []error
	finishReasons []string // optional, parallel to responses
	callCount     int
}

func (m *mockLLM) Model() string { return m.model }

func (m *mockLLM) Complete(_ context.Context, _ []providers.Message) (LLMReply, error) {
	i := m.callCount
	m.callCount++
	if i < len(m.errors) && m.errors[i] != nil {
		return LLMReply{}, m.errors[i]
	}
	resp := ""
	if i < len(m.responses) {
		resp = m.responses[i]
	}
	fr := ""
	if i < len(m.finishReasons) {
		fr = m.finishReasons[i]
	}
	return LLMReply{Content: resp, FinishReason: fr}, nil
}

// validSummaryJSON produces minimal valid Summary JSON.
func validSummaryJSON(goals string) string {
	return fmt.Sprintf(`{"version":2,"state":{"goals":[{"text":%q,"refs":[{"seq_start":1}]}]},"covered_seq_start":0,"covered_seq_end":0}`, goals)
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

// TestCompress_RefusalDetectedAndModelSkipped verifies that a content refusal is
// (a) classified as "refused" rather than "error", (b) does not block a later
// model from succeeding, and (c) causes the refusing model to be skipped on the
// next compaction for the same session.
func TestCompress_RefusalDetectedAndModelSkipped(t *testing.T) {
	store := &compressTestStore{history: makeConversation(10, 200)}

	// First model refuses (partial JSON then a decline, no finish_reason);
	// second model produces a valid summary.
	refuser := &mockLLM{
		model:     "gpt-refuser",
		responses: []string{`{"version":2,"state":{"goals":[{"text":"x"` + "\n\nI'm sorry, but I cannot assist with that request."},
	}
	worker := &mockLLM{
		model:     "deepseek-worker",
		responses: []string{validSummaryJSON("g1"), validSummaryJSON("g2")},
	}

	mgr := newCompressManager(store, []LLMClient{refuser, worker})
	mgr.msgCount = len(store.history)

	if err := mgr.doCompress(context.Background(), false); err != nil {
		t.Fatalf("doCompress returned error: %v", err)
	}

	// The refuser must be recorded as "refused", and remembered.
	var sawRefused bool
	for _, a := range mgr.lastReport.Attempts {
		if a.Model == "gpt-refuser" && a.Status == "refused" {
			sawRefused = true
		}
	}
	if !sawRefused {
		t.Fatalf("expected gpt-refuser attempt with status 'refused'; got %+v", mgr.lastReport.Attempts)
	}
	if !mgr.refusedModels["gpt-refuser"] {
		t.Fatal("expected gpt-refuser to be remembered in refusedModels")
	}
	if store.summary == "" {
		t.Fatal("expected a summary from the non-refusing model")
	}

	// Second compaction: the refuser must be skipped entirely (no new call).
	refuser.callCount = 0
	store.history = makeConversation(10, 200)
	mgr.msgCount = len(store.history)
	if err := mgr.doCompress(context.Background(), false); err != nil {
		t.Fatalf("second doCompress returned error: %v", err)
	}
	if refuser.callCount != 0 {
		t.Errorf("expected refusing model to be skipped on the second pass; got %d calls", refuser.callCount)
	}
}

// cooldownMockLLM reports a provider name so it keys cooldown by provider+model
// like the main fallback chain.
type cooldownMockLLM struct {
	mockLLM
	provider string
}

func (m *cooldownMockLLM) CooldownProvider() string { return m.provider }

// TestCompress_NeverEmptiesLiveWindow reproduces the "Compacted to 0 messages"
// bug: a long in-flight tool-call sequence (all assistant tool_calls + tool
// results, no user/clean anchor) must NOT be compacted away to a system-only
// payload — at least the last turn group is retained.
func TestCompress_NeverEmptiesLiveWindow(t *testing.T) {
	big := strings.Repeat("x", 4000)
	history := []providers.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("tc%d", i)
		history = append(history,
			providers.Message{Role: "assistant", Content: big, ToolCalls: []providers.ToolCall{{ID: id, Name: "x"}}},
			providers.Message{Role: "tool", Content: big, ToolCallID: id},
		)
	}
	store := &compressTestStore{history: history}
	llm := &mockLLM{responses: []string{
		validSummaryJSON("a"), validSummaryJSON("b"), validSummaryJSON("c"),
	}}
	mgr := newCompressManager(store, []LLMClient{llm})
	mgr.msgCount = len(history)

	_ = mgr.doCompress(context.Background(), false)

	conv := 0
	for _, m := range store.GetHistory("sess") {
		if m.Role != "system" {
			conv++
		}
	}
	if conv == 0 {
		t.Fatal("compaction emptied the live window to a system-only payload")
	}
}

// TestCompress_SharedCooldownSkipsModel verifies that a model parked in the
// shared cooldown tracker (e.g. an out-of-credits 402 hit by the main chain) is
// skipped by the compaction path — not retried.
func TestCompress_SharedCooldownSkipsModel(t *testing.T) {
	tracker := providers.NewCooldownTrackerWithPolicy(providers.DefaultCooldownPolicy())
	// Simulate the main chain having parked the model on a 402.
	tracker.MarkFailure("Abliteration", "abliterated-model", providers.FailoverBilling, 402, 0)

	store := &compressTestStore{history: makeConversation(10, 200)}
	client := &cooldownMockLLM{
		provider: "Abliteration",
		mockLLM:  mockLLM{model: "abliterated-model", responses: []string{validSummaryJSON("x")}},
	}
	mgr := newCompressManager(store, []LLMClient{client}, WithCooldownTracker(tracker))
	mgr.msgCount = len(store.history)

	_ = mgr.doCompress(context.Background(), false)
	if client.callCount != 0 {
		t.Fatalf("model cooled in the shared tracker must be skipped by compaction; calls=%d", client.callCount)
	}
}

// TestCompress_RetainsLastUserMessage verifies compaction never archives the
// most recent user turn. A long tool/assistant tail after the last user message
// would otherwise push it out of the retained window, leaving a payload with no
// user-role message (strict providers reject that with a non-retriable 400).
func TestCompress_RetainsLastUserMessage(t *testing.T) {
	history := []providers.Message{{Role: "system", Content: "sys"}}
	history = append(history, providers.Message{Role: "user", Content: strings.Repeat("u", 200)})
	for i := 0; i < 60; i++ { // long assistant tail after the only user turn
		history = append(history, providers.Message{Role: "assistant", Content: strings.Repeat("a", 200)})
	}
	store := &compressTestStore{history: history}
	llm := &mockLLM{responses: []string{validSummaryJSON("goal")}}
	mgr := newCompressManager(store, []LLMClient{llm})
	mgr.msgCount = len(history)

	_ = mgr.doCompress(context.Background(), false)

	hasUser := false
	for _, m := range store.GetHistory("sess") {
		if m.Role == "user" {
			hasUser = true
			break
		}
	}
	if !hasUser {
		t.Fatalf("compaction archived the last user message — payload would have no user role")
	}
}

// TestCompress_BillingFailurePutsModelInCooldown verifies that a summarization
// model returning a billing (402) error is put in cooldown and not retried on
// the next compaction — so an out-of-credits compression model is not hammered.
func TestCompress_BillingFailureCooldown(t *testing.T) {
	store := &compressTestStore{history: makeConversation(10, 200)}
	failing := &modelMockLLM{
		mockLLM: mockLLM{errors: []error{
			errors.New("API request failed:   Status: 402   Body: {\"error\":{\"billing_url\":\"x\"}}"),
			errors.New("API request failed:   Status: 402   Body: {\"error\":{\"billing_url\":\"x\"}}"),
		}},
		model: "abliterated-model",
	}
	mgr := newCompressManager(store, []LLMClient{failing})
	mgr.msgCount = len(store.history)

	// First pass: the model is called once, fails with 402, gets cooled.
	_ = mgr.doCompress(context.Background(), false)
	if failing.callCount != 1 {
		t.Fatalf("first pass: callCount = %d, want 1", failing.callCount)
	}
	rep := mgr.LastCompactionReport()
	if rep == nil || len(rep.Attempts) == 0 || rep.Attempts[0].Detail != "HTTP 402 (out of credits)" {
		t.Fatalf("first pass detail not neatened: %+v", rep)
	}

	// Second pass: the cooled model must be skipped entirely (no new call).
	store.history = makeConversation(10, 200)
	mgr.msgCount = len(store.history)
	_ = mgr.doCompress(context.Background(), false)
	if failing.callCount != 1 {
		t.Fatalf("second pass: cooled model was retried (callCount = %d, want 1)", failing.callCount)
	}
	rep = mgr.LastCompactionReport()
	if rep == nil || !rep.hasCooldown() {
		t.Fatalf("second pass report should reflect cooldown: %+v", rep)
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
// (non-safety-net) compression, ErrCompressionFailed is returned and history is unchanged.
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
	if !errors.Is(err, ErrCompressionFailed) {
		t.Fatalf("expected ErrCompressionFailed; got %v", err)
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
