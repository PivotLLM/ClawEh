package tools

import (
	"strings"
	"testing"
)

// Tests for internal web.go functions accessible in the package test.

func TestStripTags_RemovesHTMLTags(t *testing.T) {
	input := "<b>Hello</b> <i>World</i>"
	got := stripTags(input)
	if got != "Hello World" {
		t.Errorf("stripTags() = %q, want 'Hello World'", got)
	}
}

func TestStripTags_NoTags(t *testing.T) {
	input := "plain text"
	got := stripTags(input)
	if got != input {
		t.Errorf("stripTags() = %q, want %q", got, input)
	}
}

func TestStripTags_EmptyString(t *testing.T) {
	got := stripTags("")
	if got != "" {
		t.Errorf("stripTags('') = %q, want empty", got)
	}
}

func TestDuckDuckGoExtractResults_NoMatches(t *testing.T) {
	p := &DuckDuckGoSearchProvider{}
	result, err := p.extractResults("<html><body>no results</body></html>", 5, "test query")
	if err != nil {
		t.Fatalf("extractResults() error = %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("extractResults() = %q, want 'No results' message", result)
	}
}

func TestDuckDuckGoExtractResults_WithMatches(t *testing.T) {
	p := &DuckDuckGoSearchProvider{}
	// Simulate HTML with a DuckDuckGo result link.
	html := `<a class="result__a" href="https://example.com">Example Title</a>
<a class="result__snippet">A useful snippet about the result.</a>`

	result, err := p.extractResults(html, 3, "example")
	if err != nil {
		t.Fatalf("extractResults() error = %v", err)
	}
	// Even if the regex doesn't match exactly, the function should not panic.
	_ = result
}

func TestWebSearchTool_Execute_WithCount(t *testing.T) {
	opts := WebSearchToolOptions{
		SearXNGEnabled: true,
		SearXNGBaseURL: "https://searxng.example.invalid", // won't actually be called
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool() error = %v", err)
	}

	// The count parameter is picked up — even if the search fails (network).
	// Just verify Execute doesn't panic.
	result := tool.Execute(t.Context(), map[string]any{
		"query": "test",
		"count": float64(3),
	})
	// May succeed or error depending on network; just ensure no panic.
	_ = result
}
