// ClawEh
// License: MIT

package llmcontext

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/providers/protocoltypes"
)

func msg(role, content string) providers.Message {
	return providers.Message{Role: role, Content: content}
}

func toolCallMsg(content, tcID string) providers.Message {
	return providers.Message{
		Role:    "assistant",
		Content: content,
		ToolCalls: []protocoltypes.ToolCall{
			{ID: tcID},
		},
	}
}

func toolResultMsg(content, callID string) providers.Message {
	return providers.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: callID,
	}
}

// TestSelectTail_BudgetRetention verifies that messages are dropped when the
// token budget is exhausted and they don't need to be kept for the floor.
func TestSelectTail_BudgetRetention(t *testing.T) {
	// Build a history where only the last 2 messages fit in the budget.
	// estimateTokens: 2 chars/5 tokens, so 100 runes → 40 tokens.
	longContent := fmt.Sprintf("%0100d", 0) // 100 chars → ~40 tokens
	history := []providers.Message{
		msg("user", longContent),      // too old, won't fit
		msg("assistant", longContent), // too old, won't fit
		msg("user", "hi"),             // fits
		msg("assistant", "ok"),        // fits
	}
	// Budget of 10 tokens; only "hi"+"ok" fit (each ~1 token).
	got := selectTail(history, 10, 0, estimateTokens)
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	if got[0].Content != "hi" || got[1].Content != "ok" {
		t.Errorf("unexpected contents: %v", got)
	}
}

// TestSelectTail_MinFloorOverride verifies that minMessages overrides the
// budget, keeping at least that many meaningful messages.
func TestSelectTail_MinFloorOverride(t *testing.T) {
	// Each message has distinct content so noise collapse does not shrink them.
	history := []providers.Message{
		msg("user", fmt.Sprintf("%0200d", 1)),      // 200 chars → ~80 tokens
		msg("assistant", fmt.Sprintf("%0200d", 2)), // distinct
		msg("user", fmt.Sprintf("%0200d", 3)),      // distinct
		msg("assistant", fmt.Sprintf("%0200d", 4)), // distinct
	}
	// Budget of 1 token — nothing fits — but floor is 3.
	got := selectTail(history, 1, 3, estimateTokens)
	if len(got) < 3 {
		t.Fatalf("want at least 3 messages due to floor, got %d", len(got))
	}
}

// TestSelectTail_ToolGroupKeptWhole verifies that a tool-call group (assistant
// with ToolCalls + tool result) is kept as an atomic unit.
func TestSelectTail_ToolGroupKeptWhole(t *testing.T) {
	history := []providers.Message{
		msg("user", "start"),
		toolCallMsg("calling tool", "tc1"),
		toolResultMsg("tool output", "tc1"),
		msg("user", "follow up"),
	}
	// Budget large enough to fit everything.
	got := selectTail(history, 10000, 0, estimateTokens)
	if len(got) != 4 {
		t.Fatalf("want 4 messages, got %d", len(got))
	}
}

// TestSelectTail_ToolGroupDroppedWhole verifies that when a tool-call group
// cannot fit in the budget, all of its messages are dropped together.
func TestSelectTail_ToolGroupDroppedWhole(t *testing.T) {
	// Tool group has ~160 chars = ~64 tokens; follow-up has ~2 tokens.
	// Budget of 10 fits follow-up but NOT the tool group.
	longTool := fmt.Sprintf("%0200d", 0)
	history := []providers.Message{
		msg("user", "before"),
		toolCallMsg(longTool, "tc1"),
		toolResultMsg(longTool, "tc1"),
		msg("user", "after"),
	}
	got := selectTail(history, 10, 0, estimateTokens)
	// Only "after" fits; the tool group (tc1 + result) must not be partially included.
	for _, m := range got {
		if m.ToolCallID == "tc1" || (len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == "tc1") {
			t.Errorf("tool group message leaked into tail: %+v", m)
		}
	}
}

// TestSelectTail_LeadingToolGroupTrimmed verifies the retained tail never starts
// on a partial tool group: when the budget cut lands inside a tool-call sequence,
// the leading assistant tool-call + tool results are trimmed (handed to the
// summary) so the tail begins on a clean boundary the provider sanitizer accepts.
func TestSelectTail_LeadingToolGroupTrimmed(t *testing.T) {
	history := []providers.Message{
		msg("user", "old question"),
		toolCallMsg("calling tool", "tc1"),
		toolResultMsg("tool output", "tc1"),
		msg("user", "recent question"),
	}
	// Budget fits everything by tokens, but force the floor so all groups are
	// collected — then the leading tool group must still be trimmed.
	got := selectTail(history, 10000, 0, estimateTokens)
	if len(got) == 0 {
		t.Fatal("expected a non-empty tail")
	}
	first := got[0]
	if first.Role == "tool" || (first.Role == "assistant" && len(first.ToolCalls) > 0) {
		t.Fatalf("tail starts on a partial tool group: %+v", first)
	}

	// Now make the cut land mid-group: only the tool result + final user fit by
	// budget, so resolveGroup pulls in the assistant — the whole leading group
	// must be trimmed, leaving just the clean trailing user message.
	got2 := selectTail(history[1:], 10000, 0, estimateTokens) // [toolcall, toolresult, user]
	if len(got2) != 1 || got2[0].Content != "recent question" {
		t.Fatalf("expected only the trailing user message, got %+v", got2)
	}
}

// TestSelectTail_NoiseCollapsed verifies that consecutive identical same-role
// messages are collapsed to at most one in the retained tail.
func TestSelectTail_NoiseCollapsed(t *testing.T) {
	history := []providers.Message{
		msg("user", "hello"),
		msg("assistant", "hi"),
		msg("user", "hello"),   // duplicate of history[0]
		msg("assistant", "hi"), // duplicate of history[1]
		msg("user", "different"),
	}
	got := selectTail(history, 10000, 0, estimateTokens)
	// Collapsed: only one "hello" user and one "hi" assistant should survive each.
	userCount := 0
	for _, m := range got {
		if m.Role == "user" && m.Content == "hello" {
			userCount++
		}
	}
	if userCount > 1 {
		t.Errorf("expected at most 1 'hello' user message, got %d", userCount)
	}
	assistantCount := 0
	for _, m := range got {
		if m.Role == "assistant" && m.Content == "hi" {
			assistantCount++
		}
	}
	if assistantCount > 1 {
		t.Errorf("expected at most 1 'hi' assistant message, got %d", assistantCount)
	}
}

// TestSelectTail_CronNoiseCollapsed verifies cron-wrapper messages with the
// same payload are treated as noise and collapsed.
func TestSelectTail_CronNoiseCollapsed(t *testing.T) {
	wrap := func(ts, payload string) providers.Message {
		return msg("user", testCronPrefix+"2026-01-01 "+ts+":\n"+payload)
	}
	history := []providers.Message{
		wrap("10:00", "run backup"),
		msg("assistant", "done"),
		wrap("11:00", "run backup"), // same payload — noise
		msg("assistant", "done"),
	}
	got := selectTail(history, 10000, 0, estimateTokens)
	cronCount := 0
	for _, m := range got {
		if strings.HasPrefix(m.Content, testCronPrefix) {
			cronCount++
		}
	}
	if cronCount > 1 {
		t.Errorf("expected cron noise collapsed to 1, got %d cron messages", cronCount)
	}
}

// TestSelectTail_EmptyHistory returns nil for an empty input.
func TestSelectTail_EmptyHistory(t *testing.T) {
	got := selectTail(nil, 1000, 2, estimateTokens)
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

// TestSelectTail_ZeroBudget treats zero budget as unlimited.
func TestSelectTail_ZeroBudget(t *testing.T) {
	history := []providers.Message{
		msg("user", "a"),
		msg("assistant", "b"),
		msg("user", "c"),
	}
	got := selectTail(history, 0, 0, estimateTokens)
	if len(got) != 3 {
		t.Errorf("zero budget should keep all messages, got %d", len(got))
	}
}

// TestResolveGroup_NoToolCall returns single-message group.
func TestResolveGroup_NoToolCall(t *testing.T) {
	history := []providers.Message{
		msg("user", "hello"),
		msg("assistant", "world"),
	}
	g := resolveGroup(history, 1)
	if g.start != 1 || g.end != 1 {
		t.Errorf("want {1,1}, got %+v", g)
	}
}

// TestResolveGroup_ToolCall returns group from matching assistant to result.
func TestResolveGroup_ToolCall(t *testing.T) {
	history := []providers.Message{
		msg("user", "go"),
		toolCallMsg("calling", "tc42"),
		toolResultMsg("result", "tc42"),
	}
	g := resolveGroup(history, 2)
	if g.start != 1 || g.end != 2 {
		t.Errorf("want {1,2}, got %+v", g)
	}
}

// TestResolveGroup_UnmatchedToolCall falls back to single-message group.
func TestResolveGroup_UnmatchedToolCall(t *testing.T) {
	history := []providers.Message{
		msg("user", "hi"),
		toolResultMsg("orphan result", "unknown-id"),
	}
	g := resolveGroup(history, 1)
	if g.start != 1 || g.end != 1 {
		t.Errorf("want {1,1} for unmatched tool call, got %+v", g)
	}
}
