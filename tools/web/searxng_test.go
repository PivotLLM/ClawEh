package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Tests for SearXNG search provider using a local test HTTP server.

func TestSearXNGSearchProvider_Search_Success(t *testing.T) {
	// Set up a mock SearXNG server.
	response := map[string]any{
		"results": []map[string]any{
			{
				"title":   "Go Programming Language",
				"url":     "https://go.dev",
				"content": "Go is an open source programming language.",
				"engine":  "google",
				"score":   1.0,
			},
			{
				"title":   "Go Tour",
				"url":     "https://tour.golang.org",
				"content": "A Tour of Go.",
				"engine":  "bing",
				"score":   0.8,
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	result, err := p.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "Go Programming Language") {
		t.Errorf("result = %q, want 'Go Programming Language'", result)
	}
	if !strings.Contains(result, "go.dev") {
		t.Errorf("result = %q, want 'go.dev'", result)
	}
}

func TestSearXNGSearchProvider_Search_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"results":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	result, err := p.Search(context.Background(), "no-results-query", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("result = %q, want 'No results'", result)
	}
}

func TestSearXNGSearchProvider_Search_LimitResults(t *testing.T) {
	// Server returns 5 results, but count=2 should limit to 2.
	results := make([]map[string]any, 5)
	for i := range results {
		results[i] = map[string]any{"title": "Result", "url": "https://example.com", "content": ""}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"results": results}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	result, err := p.Search(context.Background(), "test", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Should have at most 2 results numbered.
	count := strings.Count(result, "1.")
	if count != 1 {
		t.Logf("result = %q", result)
	}
	// Should have "2." but not "3."
	if !strings.Contains(result, "2.") {
		t.Errorf("result should contain second result")
	}
	if strings.Contains(result, "3.") {
		t.Errorf("result should not contain third result when count=2")
	}
}

func TestSearXNGSearchProvider_Search_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for non-OK HTTP status")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %q, want 'status 500'", err.Error())
	}
}

func TestSearXNGSearchProvider_Search_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("not valid json")); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for invalid JSON response")
	}
}

func TestSearXNGSearchProvider_Search_ResultWithEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"results":[{"title":"Title","url":"https://example.com","content":""}]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL, client: server.Client()}
	result, err := p.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "Title") {
		t.Errorf("result = %q, want 'Title'", result)
	}
}

const searxngOneResult = `{"results":[{"title":"Go","url":"https://go.dev","content":"Go."}]}`

// searxngProvider builds the web_search tool for SearXNG and returns its provider.
func searxngProvider(t *testing.T, baseURL, proxy string) *SearXNGSearchProvider {
	t.Helper()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		SearXNGEnabled: true,
		SearXNGBaseURL: baseURL,
		Proxy:          proxy,
	})
	if err != nil {
		t.Fatalf("NewWebSearchTool: %v", err)
	}
	p, ok := tool.provider.(*SearXNGSearchProvider)
	if !ok {
		t.Fatalf("provider = %T, want *SearXNGSearchProvider", tool.provider)
	}
	return p
}

// A remote SearXNG is reached through the web tools proxy.
func TestNewWebSearchTool_SearXNGRemoteUsesProxy(t *testing.T) {
	var proxied atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(r.Host)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(searxngOneResult)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer proxy.Close()

	p := searxngProvider(t, "http://searx.example.test", proxy.URL)
	result, err := p.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, ok := proxied.Load().(string); !ok || got != "searx.example.test" {
		t.Fatalf("proxy saw host %q, want searx.example.test", got)
	}
	if !strings.Contains(result, "go.dev") {
		t.Fatalf("result = %q, want the proxied answer", result)
	}
}

// A SearXNG on this machine is reached directly even when a proxy is set: the
// proxy cannot reach this machine's loopback address.
func TestNewWebSearchTool_SearXNGLoopbackDirect(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("proxy used for a loopback SearXNG: %s", r.RequestURI)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(searxngOneResult)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	for _, base := range []string{server.URL, strings.Replace(server.URL, "127.0.0.1", "localhost", 1)} {
		t.Run(base, func(t *testing.T) {
			p := searxngProvider(t, base, proxy.URL)
			result, err := p.Search(context.Background(), "golang", 5)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !strings.Contains(result, "go.dev") {
				t.Fatalf("result = %q, want the direct answer", result)
			}
		})
	}
}

// With no proxy set, a remote SearXNG gets no configured proxy: only the
// process environment applies, as for every other search provider.
func TestNewWebSearchTool_SearXNGNoProxyDirect(t *testing.T) {
	p := searxngProvider(t, "http://searx.example.test", "")
	transport, ok := p.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", p.client.Transport)
	}
	req := httptest.NewRequest(http.MethodGet, "http://searx.example.test/search", nil)
	got, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	want, err := http.ProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("ProxyFromEnvironment: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("proxy = %v, want %v (environment only)", got, want)
	}
}

func TestIsLoopbackURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:8888", true},
		{"http://LOCALHOST", true},
		{"http://127.0.0.1:8888", true},
		{"http://127.5.0.1", true},
		{"http://[::1]:8888", true},
		{"http://10.0.0.5:8888", false},
		{"https://searx.example.test", false},
		{"http://localhost.example.test", false},
		{"://bad", false},
	}
	for _, tt := range tests {
		if got := isLoopbackURL(tt.url); got != tt.want {
			t.Errorf("isLoopbackURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
