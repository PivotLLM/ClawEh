// ClawEh
// License: MIT

package session

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
)

// TestSearchTool_BasicSearch returns the correct message for a content term.
func TestSearchTool_BasicSearch(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "the quick brown fox"),
		archiveMsg(2, "assistant", "jumped over the lazy dog"),
		archiveMsg(3, "user", "nothing relevant here"),
	}
	writeArchive(t, dir, "searchsess", msgs)

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "searchsess")
	result := tool.Execute(ctx, map[string]any{"query": "fox"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "quick brown fox") {
		t.Errorf("expected fox message, got: %s", result.ForLLM)
	}
	if containsStr(result.ForLLM, "lazy dog") {
		t.Errorf("unexpected second message in result: %s", result.ForLLM)
	}
}

// TestSearchTool_FTSOperators tests AND, OR expressions.
func TestSearchTool_FTSOperators(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "apple banana cherry"),
		archiveMsg(2, "assistant", "apple only"),
		archiveMsg(3, "user", "banana only"),
	}
	writeArchive(t, dir, "ftssess", msgs)

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "ftssess")

	// AND: only message 1 contains both apple and banana.
	r := tool.Execute(ctx, map[string]any{"query": "apple AND banana"})
	if r.IsError {
		t.Fatalf("AND query error: %s", r.ForLLM)
	}
	if !containsStr(r.ForLLM, "cherry") {
		t.Errorf("AND: expected msg1 (cherry), got: %s", r.ForLLM)
	}

	// OR: messages 1 and 2 contain apple; 1 and 3 contain banana — all 3 should appear.
	r2 := tool.Execute(ctx, map[string]any{"query": "apple OR banana"})
	if r2.IsError {
		t.Fatalf("OR query error: %s", r2.ForLLM)
	}
	if !containsStr(r2.ForLLM, "cherry") && !containsStr(r2.ForLLM, "apple only") {
		t.Errorf("OR: expected multiple results, got: %s", r2.ForLLM)
	}
}

// TestSearchTool_RoleFilter restricts results by role.
func TestSearchTool_RoleFilter(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "unique_term_xyz user message"),
		archiveMsg(2, "assistant", "unique_term_xyz assistant message"),
	}
	writeArchive(t, dir, "rolesess", msgs)

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "rolesess")

	// Filter to user only.
	r := tool.Execute(ctx, map[string]any{"query": "unique_term_xyz", "role": "user"})
	if r.IsError {
		t.Fatalf("role filter error: %s", r.ForLLM)
	}
	if !containsStr(r.ForLLM, "user message") {
		t.Errorf("expected user message in result: %s", r.ForLLM)
	}
	if containsStr(r.ForLLM, "assistant message") {
		t.Errorf("unexpected assistant message with role=user filter: %s", r.ForLLM)
	}
}

// TestSearchTool_QueryTooLong rejects queries longer than 500 characters.
func TestSearchTool_QueryTooLong(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "longsess", []memory.StoredMessage{
		archiveMsg(1, "user", "something"),
	})

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "longsess")
	longQuery := strings.Repeat("x", 501)
	r := tool.Execute(ctx, map[string]any{"query": longQuery})
	if !r.IsError {
		t.Errorf("expected error for >500 char query, got: %s", r.ForLLM)
	}
}

// TestSearchTool_MalformedFTS returns a tool error (not panic) for a bad FTS expression.
func TestSearchTool_MalformedFTS(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "badftssess", []memory.StoredMessage{
		archiveMsg(1, "user", "content"),
	})

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "badftssess")
	// Unmatched quote is a malformed FTS5 expression.
	r := tool.Execute(ctx, map[string]any{"query": `"unclosed phrase`})
	if r.IsError {
		// An error result is acceptable — FTS parse errors are surfaced as tool errors.
		return
	}
	// If it doesn't error, no panic means pass.
}

// TestSearchTool_LimitEnforcedAt100 clamps limit to 100.
func TestSearchTool_LimitEnforcedAt100(t *testing.T) {
	dir := t.TempDir()
	var msgs []memory.StoredMessage
	for i := 1; i <= 150; i++ {
		msgs = append(msgs, archiveMsg(int64(i), "user", "matchterm repetitive content"))
	}
	writeArchive(t, dir, "limitsess", msgs)

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "limitsess")
	r := tool.Execute(ctx, map[string]any{"query": "matchterm", "limit": 200})
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.ForLLM)
	}
	// Count occurrences of "[#" in the output to estimate number of results.
	count := strings.Count(r.ForLLM, "[#")
	if count > 100 {
		t.Errorf("expected at most 100 results, got %d", count)
	}
}

// TestSearchTool_SQLInjection verifies that a SQL injection attempt is inert.
func TestSearchTool_SQLInjection(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "injsess", []memory.StoredMessage{
		archiveMsg(1, "user", "safe content"),
	})

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "injsess")

	// This classic injection attempt should either return no results or a tool
	// error (FTS parse error). In either case the table must remain intact.
	_ = tool.Execute(ctx, map[string]any{"query": "x'; DROP TABLE messages; --"})

	// Verify the table is still intact by opening the archive directly.
	archivePath := dir + "/" + archiveSanitizeKey("injsess") + ".archive.db"
	a, err := memory.Open(archivePath)
	if err != nil {
		t.Fatalf("archive open after injection attempt: %v", err)
	}
	defer a.Close()
	msgs, err := a.QueryRange(1, 1)
	if err != nil {
		t.Fatalf("QueryRange after injection attempt: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message after injection attempt, got %d", len(msgs))
	}
}

// TestSearchTool_EmptyResult returns a clear message when no messages match.
func TestSearchTool_EmptyResult(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "emptysess", []memory.StoredMessage{
		archiveMsg(1, "user", "hello world"),
	})

	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "emptysess")
	r := tool.Execute(ctx, map[string]any{"query": "zzznomatch"})
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.ForLLM)
	}
	if !containsStr(r.ForLLM, "no matching") {
		t.Errorf("expected 'no matching messages' message, got: %s", r.ForLLM)
	}
}

// TestSearchTool_MissingSessionKey returns error when session key not in context.
func TestSearchTool_MissingSessionKey(t *testing.T) {
	dir := t.TempDir()
	tool := NewSessionHistorySearchTool(dir)
	r := tool.Execute(context.Background(), map[string]any{"query": "anything"})
	if !r.IsError {
		t.Errorf("expected error for missing session key, got: %s", r.ForLLM)
	}
}

// TestSearchTool_NoArchive returns "archive unavailable" when file is missing.
func TestSearchTool_NoArchive(t *testing.T) {
	dir := t.TempDir()
	tool := NewSessionHistorySearchTool(dir)
	ctx := ctxWithSession(t, "missingsess")
	r := tool.Execute(ctx, map[string]any{"query": "anything"})
	if r.IsError {
		t.Fatalf("unexpected hard error: %s", r.ForLLM)
	}
	if !containsStr(r.ForLLM, "unavailable") {
		t.Errorf("expected unavailability message, got: %s", r.ForLLM)
	}
}
