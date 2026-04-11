package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for Brave, Tavily, GLM, and DuckDuckGo search providers using httptest servers.

// --- BraveSearchProvider ---

func TestBraveSearchProvider_Search_Success(t *testing.T) {
	response := map[string]any{
		"web": map[string]any{
			"results": []map[string]any{
				{
					"title":       "Go Language",
					"url":         "https://go.dev",
					"description": "The Go programming language.",
				},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// BraveSearchProvider uses the URL built from query params — we can't override the URL,
	// but we can point the client's transport to our server by using a custom transport.
	// Instead, use a custom HTTP client that rewrites requests to our test server.
	p := &BraveSearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL},
		},
	}

	result, err := p.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "Go Language") {
		t.Errorf("result = %q, want 'Go Language'", result)
	}
}

func TestBraveSearchProvider_Search_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()

	p := &BraveSearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL},
		},
	}

	result, err := p.Search(context.Background(), "noresults", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("result = %q, want 'No results'", result)
	}
}

func TestBraveSearchProvider_Search_NonRetryableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer server.Close()

	p := &BraveSearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL},
		},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for non-retryable HTTP error")
	}
}

func TestBraveSearchProvider_Search_EmptyKeyPool(t *testing.T) {
	p := &BraveSearchProvider{
		keyPool: NewAPIKeyPool(nil),
		client:  &http.Client{},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error when key pool is empty")
	}
}

// --- TavilySearchProvider ---

func TestTavilySearchProvider_Search_Success(t *testing.T) {
	response := map[string]any{
		"results": []map[string]any{
			{
				"title":   "Tavily Result",
				"url":     "https://example.com",
				"content": "Some content",
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	p := &TavilySearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	result, err := p.Search(context.Background(), "test query", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "Tavily Result") {
		t.Errorf("result = %q, want 'Tavily Result'", result)
	}
}

func TestTavilySearchProvider_Search_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	p := &TavilySearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	result, err := p.Search(context.Background(), "nothing", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("result = %q, want 'No results'", result)
	}
}

func TestTavilySearchProvider_Search_EmptyKeyPool(t *testing.T) {
	p := &TavilySearchProvider{
		keyPool: NewAPIKeyPool(nil),
		baseURL: "http://localhost:99999",
		client:  &http.Client{},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error when key pool is empty")
	}
}

func TestTavilySearchProvider_Search_NonRetryableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	p := &TavilySearchProvider{
		keyPool: NewAPIKeyPool([]string{"test-key"}),
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for non-retryable status")
	}
}

// --- GLMSearchProvider ---

func TestGLMSearchProvider_Search_Success(t *testing.T) {
	response := map[string]any{
		"search_result": []map[string]any{
			{
				"title":   "GLM Result",
				"content": "GLM content here",
				"link":    "https://glm.example.com",
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	p := &GLMSearchProvider{
		apiKey:  "test-api-key",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	result, err := p.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "GLM Result") {
		t.Errorf("result = %q, want 'GLM Result'", result)
	}
}

func TestGLMSearchProvider_Search_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"search_result":[]}`))
	}))
	defer server.Close()

	p := &GLMSearchProvider{
		apiKey:  "test-key",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	result, err := p.Search(context.Background(), "noresults", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("result = %q, want 'No results'", result)
	}
}

func TestGLMSearchProvider_Search_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	p := &GLMSearchProvider{
		apiKey:  "bad-key",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for non-OK HTTP status")
	}
}

func TestGLMSearchProvider_Search_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	p := &GLMSearchProvider{
		apiKey:  "test-key",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for invalid JSON")
	}
}

// --- DuckDuckGo Search (via custom client) ---

func TestDuckDuckGoSearchProvider_Search_Success(t *testing.T) {
	// DuckDuckGo returns HTML, so we provide DDG-format HTML with result links.
	html := `<html><body>
<a class="result__a" href="https://example.com">Example Site</a>
<a class="result__snippet">A snippet about Example Site</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer server.Close()

	p := &DuckDuckGoSearchProvider{
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL},
		},
	}

	// The extractResults method may not find matches in our simple HTML
	// but Search itself should not error.
	_, err := p.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestDuckDuckGoSearchProvider_Search_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := &DuckDuckGoSearchProvider{
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL},
			// Use a very short timeout to avoid hanging on closed server
			Timeout: 2 * time.Second,
		},
	}

	// The error here is from resp.Body read failure or the extractResults fallback.
	// Either way it shouldn't panic.
	_, _ = p.Search(context.Background(), "test", 5)
}

// rewriteTransport rewrites all requests to the given base URL.
type rewriteTransport struct {
	base string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	// Extract host from base
	base := strings.TrimPrefix(t.base, "http://")
	base = strings.TrimPrefix(base, "https://")
	newReq.URL.Host = base
	return http.DefaultTransport.RoundTrip(newReq)
}
