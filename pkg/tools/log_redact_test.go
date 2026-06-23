package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

func TestRedactArgs_WriteFile_RecordsContentBytes(t *testing.T) {
	content := strings.Repeat("x", 10240)
	got := redactArgs("file_write", map[string]any{
		"path":    "/tmp/secret.txt",
		"content": content,
	})

	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", got)
	}
	if m["path"] != "/tmp/secret.txt" {
		t.Errorf("path mismatch: %v", m["path"])
	}
	if m["content_bytes"] != 10240 {
		t.Errorf("expected content_bytes=10240, got %v", m["content_bytes"])
	}
	if _, present := m["content"]; present {
		t.Error("raw content must not appear in redacted args")
	}

	encoded, _ := json.Marshal(m)
	if strings.Contains(string(encoded), content) {
		t.Error("redacted JSON must not contain the raw content body")
	}
}

func TestRedactArgs_AppendFile_RecordsContentBytes(t *testing.T) {
	got := redactArgs("file_append", map[string]any{
		"path":    "log.txt",
		"content": "hello",
	})
	m := got.(map[string]any)
	if m["content_bytes"] != 5 {
		t.Errorf("expected content_bytes=5, got %v", m["content_bytes"])
	}
}

func TestRedactArgs_ReadFile_Shape(t *testing.T) {
	got := redactArgs("file_read_bytes", map[string]any{
		"path":   "/etc/passwd",
		"offset": 100,
		"length": 4096,
	})
	m := got.(map[string]any)
	if m["path"] != "/etc/passwd" {
		t.Errorf("path: %v", m["path"])
	}
	if m["offset"] != 100 {
		t.Errorf("offset: %v", m["offset"])
	}
	if m["length"] != 4096 {
		t.Errorf("length: %v", m["length"])
	}
}

func TestRedactArgs_ReadFile_OmitsNilFields(t *testing.T) {
	got := redactArgs("file_read_bytes", map[string]any{
		"path": "/etc/passwd",
	})
	m := got.(map[string]any)
	if _, present := m["offset"]; present {
		t.Error("offset should be omitted when nil")
	}
	if _, present := m["length"]; present {
		t.Error("length should be omitted when nil")
	}
}

func TestRedactArgs_EditFile_RecordsByteLengths(t *testing.T) {
	got := redactArgs("file_edit", map[string]any{
		"path":     "src/main.go",
		"old_text": "foo",
		"new_text": "barbaz",
	})
	m := got.(map[string]any)
	if m["old_text_bytes"] != 3 {
		t.Errorf("old_text_bytes: %v", m["old_text_bytes"])
	}
	if m["new_text_bytes"] != 6 {
		t.Errorf("new_text_bytes: %v", m["new_text_bytes"])
	}
	if _, present := m["old_text"]; present {
		t.Error("raw old_text must not appear")
	}
	if _, present := m["new_text"]; present {
		t.Error("raw new_text must not appear")
	}
}

func TestRedactArgs_WebFetch_PreservesURLDropsBody(t *testing.T) {
	got := redactArgs("web_fetch", map[string]any{
		"url":      "https://example.com/api",
		"body":     "secret-token=abc",
		"headers":  map[string]any{"Authorization": "Bearer xyz"},
		"maxChars": 5000,
	})
	m := got.(map[string]any)
	if m["url"] != "https://example.com/api" {
		t.Errorf("url: %v", m["url"])
	}
	if _, present := m["body"]; present {
		t.Error("body must be stripped")
	}
	if _, present := m["headers"]; present {
		t.Error("headers must be stripped")
	}
}

func TestRedactArgs_HTTPPrefix_PreservesMethodURL(t *testing.T) {
	got := redactArgs("http_post", map[string]any{
		"url":     "https://api.example.com/v1/users",
		"method":  "POST",
		"body":    "password=hunter2",
		"headers": map[string]any{"Cookie": "session=abc"},
	})
	m := got.(map[string]any)
	if m["url"] != "https://api.example.com/v1/users" {
		t.Errorf("url: %v", m["url"])
	}
	if m["method"] != "POST" {
		t.Errorf("method: %v", m["method"])
	}
	if _, present := m["body"]; present {
		t.Error("body must be stripped from http_post")
	}
	if _, present := m["headers"]; present {
		t.Error("headers must be stripped from http_post")
	}
}

func TestRedactArgs_Default_TruncatesUnknownTool(t *testing.T) {
	args := map[string]any{
		"blob": strings.Repeat("Z", 4096),
	}
	got := redactArgs("unknown_tool", args)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string for default, got %T", got)
	}
	if !strings.Contains(s, "more)") {
		t.Errorf("expected truncation suffix, got %q", s[:min(80, len(s))])
	}
	if len([]rune(s)) > 200+32 {
		t.Errorf("truncated string longer than expected: %d runes", len([]rune(s)))
	}
}

func TestRedactArgs_NilArgs(t *testing.T) {
	got := redactArgs("anything", nil)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected empty map, got %T", got)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// --- registry integration: confirm INF leaks redacted summary, DBG carries raw ---

type leakyTool struct {
	mockRegistryTool
}

func TestRegistry_ExecuteWithContext_OmitsArgsAtInfo(t *testing.T) {
	var buf safeBuf
	restore := logger.RedirectForTest(&buf)
	defer restore()

	r := NewToolRegistry()
	r.Register(&leakyTool{
		mockRegistryTool: mockRegistryTool{
			name:   "file_write",
			desc:   "writes",
			params: map[string]any{},
			result: SilentResult("ok"),
		},
	})

	secret := strings.Repeat("S", 10240)
	r.ExecuteWithContext(context.Background(), "file_write", map[string]any{
		"path":    "/tmp/diary.txt",
		"content": secret,
	}, "", "", nil)

	out := buf.String()

	var infLine string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		msg, _ := ev["message"].(string)
		level, _ := ev["level"].(string)
		if msg == "Tool execution started" && level == "info" {
			infLine = line
		}
	}

	if infLine == "" {
		t.Fatal("expected INF 'Tool execution started' line")
	}
	if !strings.Contains(infLine, "file_write") {
		t.Errorf("execution line should name the tool: %s", infLine)
	}
	// Arguments are no longer logged at all — the line carries only the tool name.
	if strings.Contains(infLine, `"args"`) {
		t.Fatalf("tool args must not be logged: %s", infLine)
	}
	// The secret must not appear anywhere in the logs.
	if strings.Contains(out, secret) {
		t.Fatal("raw secret content leaked into logs")
	}
}

func TestRegistry_ExecuteWithContext_TruncatesErrorForLLM(t *testing.T) {
	var buf safeBuf
	restore := logger.RedirectForTest(&buf)
	defer restore()

	long := strings.Repeat("E", 2000)
	r := NewToolRegistry()
	r.Register(&mockRegistryTool{
		name:   "boom",
		desc:   "errors",
		params: map[string]any{},
		result: ErrorResult(long),
	})

	r.Execute(context.Background(), "boom", nil)

	out := buf.String()
	if !strings.Contains(out, "Tool execution failed") {
		t.Fatalf("expected error log, got: %s", out[:min(200, len(out))])
	}
	// Locate the error log line and inspect the captured "error" field.
	var errField string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if msg, _ := ev["message"].(string); msg == "Tool execution failed" {
			errField, _ = ev["error"].(string)
			break
		}
	}
	if errField == "" {
		t.Fatal("error field not found in failure log line")
	}
	if len([]rune(errField)) > 500 {
		t.Errorf("error field not truncated to 500 runes: got %d", len([]rune(errField)))
	}
	if !strings.HasSuffix(errField, "...") {
		t.Errorf("expected truncation marker, got tail %q", tail(errField, 20))
	}
}

// safeBuf is a sync-safe bytes.Buffer wrapper for zerolog writers under -race.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
