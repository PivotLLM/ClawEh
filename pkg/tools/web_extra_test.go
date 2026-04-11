package tools

import (
	"testing"
)

// Tests for NewWebSearchTool additional provider paths and NewWebFetchToolWithProxy.

func TestNewWebSearchTool_SearXNG(t *testing.T) {
	opts := WebSearchToolOptions{
		SearXNGEnabled:    true,
		SearXNGBaseURL:    "https://searxng.example.com",
		SearXNGMaxResults: 5,
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(SearXNG) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool for SearXNG")
	}
	if tool.maxResults != 5 {
		t.Errorf("maxResults = %d, want 5", tool.maxResults)
	}
}

func TestNewWebSearchTool_DuckDuckGoDefaultMaxResults(t *testing.T) {
	// DuckDuckGoMaxResults = 0 means use default (5)
	opts := WebSearchToolOptions{
		DuckDuckGoEnabled:    true,
		DuckDuckGoMaxResults: 0,
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.maxResults != 5 {
		t.Errorf("maxResults = %d, want default 5", tool.maxResults)
	}
}

func TestNewWebSearchTool_NoProvider_ReturnsNil(t *testing.T) {
	// No provider configured — should return nil, nil.
	opts := WebSearchToolOptions{}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(no provider) error = %v", err)
	}
	if tool != nil {
		t.Error("expected nil tool when no provider configured")
	}
}

func TestNewWebSearchTool_Brave(t *testing.T) {
	opts := WebSearchToolOptions{
		BraveEnabled:    true,
		BraveAPIKeys:    []string{"brave-key"},
		BraveMaxResults: 7,
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(Brave) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool for Brave")
	}
	if tool.maxResults != 7 {
		t.Errorf("maxResults = %d, want 7", tool.maxResults)
	}
}

func TestNewWebSearchTool_Tavily(t *testing.T) {
	opts := WebSearchToolOptions{
		TavilyEnabled:    true,
		TavilyAPIKeys:    []string{"tvly-key"},
		TavilyMaxResults: 4,
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(Tavily) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool for Tavily")
	}
	if tool.maxResults != 4 {
		t.Errorf("maxResults = %d, want 4", tool.maxResults)
	}
}

func TestNewWebSearchTool_GLM(t *testing.T) {
	opts := WebSearchToolOptions{
		GLMSearchEnabled:    true,
		GLMSearchAPIKey:     "glm-key",
		GLMSearchMaxResults: 3,
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(GLM) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool for GLM")
	}
	if tool.maxResults != 3 {
		t.Errorf("maxResults = %d, want 3", tool.maxResults)
	}
}

func TestNewWebSearchTool_GLMDefaultEngine(t *testing.T) {
	opts := WebSearchToolOptions{
		GLMSearchEnabled: true,
		GLMSearchAPIKey:  "glm-key",
		// No GLMSearchEngine — should default to "search_std"
	}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool(GLM default engine) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
}

func TestNewWebFetchToolWithProxy_NoProxy(t *testing.T) {
	tool, err := NewWebFetchToolWithProxy(5000, "", 1<<20)
	if err != nil {
		t.Fatalf("NewWebFetchToolWithProxy() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name() != "web_fetch" {
		t.Errorf("Name() = %q, want web_fetch", tool.Name())
	}
}

func TestNewWebFetchToolWithProxy_DefaultMaxChars(t *testing.T) {
	// maxChars <= 0 should use default.
	tool, err := NewWebFetchToolWithProxy(0, "", 0)
	if err != nil {
		t.Fatalf("NewWebFetchToolWithProxy(0, ...) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.maxChars <= 0 {
		t.Errorf("maxChars = %d, want default value > 0", tool.maxChars)
	}
}

func TestIsObviousPrivateHost_Localhost(t *testing.T) {
	if !isObviousPrivateHost("localhost") {
		t.Error("localhost should be private")
	}
}

func TestIsObviousPrivateHost_LocalhostSubdomain(t *testing.T) {
	if !isObviousPrivateHost("foo.localhost") {
		t.Error("foo.localhost should be private")
	}
}

func TestIsObviousPrivateHost_Empty(t *testing.T) {
	if !isObviousPrivateHost("") {
		t.Error("empty host should be private")
	}
}

func TestIsObviousPrivateHost_PublicDomain(t *testing.T) {
	if isObviousPrivateHost("example.com") {
		t.Error("example.com should not be private")
	}
}

func TestIsObviousPrivateHost_PrivateIP(t *testing.T) {
	if !isObviousPrivateHost("192.168.1.1") {
		t.Error("192.168.1.1 should be private")
	}
}

func TestIsObviousPrivateHost_PublicIP(t *testing.T) {
	if isObviousPrivateHost("8.8.8.8") {
		t.Error("8.8.8.8 should not be private")
	}
}

func TestWebSearchTool_Execute_MissingQuery(t *testing.T) {
	opts := WebSearchToolOptions{DuckDuckGoEnabled: true, DuckDuckGoMaxResults: 3}
	tool, err := NewWebSearchTool(opts)
	if err != nil {
		t.Fatalf("NewWebSearchTool() error = %v", err)
	}

	result := tool.Execute(t.Context(), map[string]any{})
	if !result.IsError {
		t.Error("Execute() should error for missing query")
	}
}
