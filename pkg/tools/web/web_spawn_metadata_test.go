package web

import (
	"testing"
)

// Tests for WebSearchTool and WebFetchTool Name/Description/Parameters.

func TestWebSearchTool_Metadata(t *testing.T) {
	// Use DuckDuckGo which doesn't need API keys
	opts := WebSearchToolOptions{DuckDuckGoEnabled: true, DuckDuckGoMaxResults: 3}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool() error = %v", err)
	}

	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q, want web_search", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters().properties should be a map")
	}
	if _, ok := props["query"]; !ok {
		t.Error("Parameters() should include 'query'")
	}
}

func TestWebFetchTool_Metadata(t *testing.T) {
	tool, err := NewWebFetchTool(5000, 1<<20)
	if err != nil {
		t.Fatalf("NewWebFetchTool() error = %v", err)
	}

	if tool.Name() != "web_fetch" {
		t.Errorf("Name() = %q, want web_fetch", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}
