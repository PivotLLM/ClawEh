package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL}
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
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL}
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
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL}
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

	p := &SearXNGSearchProvider{baseURL: server.URL}
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
		w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL}
	_, err := p.Search(context.Background(), "test", 5)
	if err == nil {
		t.Error("Search() should error for invalid JSON response")
	}
}

func TestSearXNGSearchProvider_Search_ResultWithEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Title","url":"https://example.com","content":""}]}`))
	}))
	defer server.Close()

	p := &SearXNGSearchProvider{baseURL: server.URL}
	result, err := p.Search(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(result, "Title") {
		t.Errorf("result = %q, want 'Title'", result)
	}
}
