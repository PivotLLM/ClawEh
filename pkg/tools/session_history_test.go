// ClawEh
// License: MIT

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// writeArchive creates an archive.jsonl file in dir with the given StoredMessages.
func writeArchive(t *testing.T, dir, sessionKey string, msgs []memory.StoredMessage) string {
	t.Helper()
	filename := archiveSanitizeKey(sessionKey) + ".archive.jsonl"
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func archiveMsg(seq int, role, content string) memory.StoredMessage {
	return memory.StoredMessage{
		Seq: seq,
		Message: providers.Message{
			Role:    role,
			Content: content,
		},
	}
}

func ctxWithSession(t *testing.T, key string) context.Context {
	t.Helper()
	return WithSessionKey(context.Background(), key)
}

// TestSessionHistory_SingleSeq retrieves one message by exact seq.
func TestSessionHistory_SingleSeq(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "hello"),
		archiveMsg(2, "assistant", "world"),
		archiveMsg(3, "user", "bye"),
	}
	writeArchive(t, dir, "testsession", msgs)

	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "testsession")
	result := tool.Execute(ctx, map[string]any{"seq": 2})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if result.ForLLM == "" {
		t.Fatal("expected non-empty result")
	}
	if !containsStr(result.ForLLM, "world") {
		t.Errorf("expected 'world' in result, got: %s", result.ForLLM)
	}
}

// TestSessionHistory_Range retrieves multiple messages.
func TestSessionHistory_Range(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "first"),
		archiveMsg(2, "assistant", "second"),
		archiveMsg(3, "user", "third"),
		archiveMsg(4, "assistant", "fourth"),
	}
	writeArchive(t, dir, "sess1", msgs)

	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "sess1")
	result := tool.Execute(ctx, map[string]any{"seq_start": 2, "seq_end": 3})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "second") || !containsStr(result.ForLLM, "third") {
		t.Errorf("expected seq 2 and 3, got: %s", result.ForLLM)
	}
	if containsStr(result.ForLLM, "first") || containsStr(result.ForLLM, "fourth") {
		t.Errorf("unexpected out-of-range messages in result: %s", result.ForLLM)
	}
}

// TestSessionHistory_BelowMin returns "not available" for seq below archive range.
func TestSessionHistory_BelowMin(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(10, "user", "recent"),
	}
	writeArchive(t, dir, "mysession", msgs)

	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "mysession")
	result := tool.Execute(ctx, map[string]any{"seq": 2})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "not available") {
		t.Errorf("expected 'not available', got: %s", result.ForLLM)
	}
}

// TestSessionHistory_AboveMax returns "not available" for seq above archive range.
func TestSessionHistory_AboveMax(t *testing.T) {
	dir := t.TempDir()
	msgs := []memory.StoredMessage{
		archiveMsg(1, "user", "old"),
	}
	writeArchive(t, dir, "mysession", msgs)

	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "mysession")
	result := tool.Execute(ctx, map[string]any{"seq": 999})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "not available") {
		t.Errorf("expected 'not available', got: %s", result.ForLLM)
	}
}

// TestSessionHistory_NoArchive returns "not available" when archive file is missing.
func TestSessionHistory_NoArchive(t *testing.T) {
	dir := t.TempDir()
	// Don't create the archive file.
	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "nosession")
	result := tool.Execute(ctx, map[string]any{"seq": 1})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "not available") {
		t.Errorf("expected 'not available', got: %s", result.ForLLM)
	}
}

// TestSessionHistory_MissingSessionKey errors when session key not in context.
func TestSessionHistory_MissingSessionKey(t *testing.T) {
	dir := t.TempDir()
	tool := NewSessionHistoryTool(dir)
	result := tool.Execute(context.Background(), map[string]any{"seq": 1})
	if !result.IsError {
		t.Errorf("expected error for missing session key, got: %s", result.ForLLM)
	}
}

// TestSessionHistory_InvalidArgs errors on missing required parameters.
func TestSessionHistory_InvalidArgs(t *testing.T) {
	dir := t.TempDir()
	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, "s")
	result := tool.Execute(ctx, map[string]any{"seq_start": 1}) // seq_end missing
	if !result.IsError {
		t.Errorf("expected error for missing seq_end, got: %s", result.ForLLM)
	}
}

// TestSessionHistory_SessionKeyInFileName verifies that ":" in session keys is
// replaced with "_" to form the archive filename.
func TestSessionHistory_SessionKeyInFileName(t *testing.T) {
	dir := t.TempDir()
	sessionKey := "agent:main"
	msgs := []memory.StoredMessage{archiveMsg(5, "user", "content")}
	writeArchive(t, dir, sessionKey, msgs)

	tool := NewSessionHistoryTool(dir)
	ctx := ctxWithSession(t, sessionKey)
	result := tool.Execute(ctx, map[string]any{"seq": 5})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !containsStr(result.ForLLM, "content") {
		t.Errorf("expected message content, got: %s", result.ForLLM)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
