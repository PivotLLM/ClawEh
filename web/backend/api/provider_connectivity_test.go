package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestProviderConnectivity_OpenAICompat(t *testing.T) {
	// A fake OpenAI-compatible endpoint: 200 only when the bearer key matches.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	t.Run("valid key", func(t *testing.T) {
		got := testProviderConnectivity(providerTestRequest{Protocol: "openai-chat", BaseURL: srv.URL, APIKey: "good-key"})
		if !got.OK {
			t.Fatalf("expected OK, got %+v", got)
		}
	})

	t.Run("bad key", func(t *testing.T) {
		got := testProviderConnectivity(providerTestRequest{Protocol: "openai-chat", BaseURL: srv.URL, APIKey: "wrong"})
		if got.OK || !strings.Contains(got.Message, "Authentication failed") {
			t.Fatalf("expected auth failure, got %+v", got)
		}
	})

	t.Run("wrong base path 404", func(t *testing.T) {
		got := testProviderConnectivity(providerTestRequest{Protocol: "openai-chat", BaseURL: srv.URL + "/nope", APIKey: "good-key"})
		if got.OK || !strings.Contains(got.Message, "not found") {
			t.Fatalf("expected 404 verdict, got %+v", got)
		}
	})
}

func TestTestProviderConnectivity_Anthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "good" && r.Header.Get("anthropic-version") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if got := testProviderConnectivity(providerTestRequest{Protocol: "anthropic", BaseURL: srv.URL, APIKey: "good"}); !got.OK {
		t.Errorf("valid anthropic key should pass: %+v", got)
	}
	if got := testProviderConnectivity(providerTestRequest{Protocol: "anthropic", BaseURL: srv.URL, APIKey: "bad"}); got.OK {
		t.Errorf("bad anthropic key should fail: %+v", got)
	}
}

func TestTestProviderConnectivity_NonTestable(t *testing.T) {
	cli := testProviderConnectivity(providerTestRequest{Protocol: "claude-cli"})
	if cli.OK || !strings.Contains(cli.Message, "detection") {
		t.Errorf("CLI should report detection, got %+v", cli)
	}
	azure := testProviderConnectivity(providerTestRequest{Protocol: "azure", APIKey: "x", BaseURL: "https://x"})
	if azure.OK || !strings.Contains(azure.Message, "Azure") {
		t.Errorf("azure should report not-testable, got %+v", azure)
	}
	missing := testProviderConnectivity(providerTestRequest{Protocol: "openai-chat", BaseURL: "", APIKey: "x"})
	if missing.OK || !strings.Contains(missing.Message, "base URL is required") {
		t.Errorf("missing base URL should be flagged, got %+v", missing)
	}
}
