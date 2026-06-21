// ClawEh
// License: MIT

package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/spawnllm/openai_responses"
)

// TestCreateProviderFromConfig_OpenAIResponses verifies the factory routes the
// "openai-responses" protocol to the Responses provider and that it works
// end-to-end against a mock /responses endpoint.
func TestCreateProviderFromConfig_OpenAIResponses(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	model := &config.ModelConfig{ModelName: "gpt5-responses", Model: "gpt-5", Provider: "openai-resp"}
	prov := &config.Provider{Name: "openai-resp", Protocol: "openai-responses", BaseURL: srv.URL, APIKey: "k"}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if _, ok := provider.(*openai_responses.Provider); !ok {
		t.Fatalf("expected *openai_responses.Provider, got %T", provider)
	}
	if modelID != "gpt-5" {
		t.Errorf("modelID = %q", modelID)
	}

	out, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, modelID, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if out.Content != "ok" {
		t.Errorf("Content = %q", out.Content)
	}
	if gotPath != "/responses" {
		t.Errorf("request path = %q, want /responses", gotPath)
	}
}

// TestValidateProviders_OpenAIResponses confirms the protocol validates and
// requires a base_url.
func TestValidateProviders_OpenAIResponses(t *testing.T) {
	ok := &config.Config{Providers: []config.Provider{
		{Name: "r", Protocol: "openai-responses", BaseURL: "https://api.openai.com/v1"},
	}}
	if err := ok.ValidateProviders(); err != nil {
		t.Errorf("valid openai-responses provider rejected: %v", err)
	}

	missing := &config.Config{Providers: []config.Provider{
		{Name: "r", Protocol: "openai-responses"},
	}}
	if err := missing.ValidateProviders(); err == nil {
		t.Error("expected error: openai-responses requires base_url")
	}
}
