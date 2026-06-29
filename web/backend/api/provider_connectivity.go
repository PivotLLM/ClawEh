package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// providerTestRequest is the body of POST /api/providers/test: the (possibly
// not-yet-saved) provider settings to validate live. The setup wizard sends a
// key the user just pasted, before persisting it, so mistakes are caught up front.
type providerTestRequest struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
}

// providerTestResponse reports the outcome of a live connectivity/auth probe.
type providerTestResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// providerProbeTimeout bounds the live test so a slow/unreachable endpoint can't
// hang the wizard.
const providerProbeTimeout = 10 * time.Second

func (h *Handler) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var req providerTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProviderTest(w, providerTestResponse{Message: "Invalid request body."})
		return
	}
	writeProviderTest(w, testProviderConnectivity(req))
}

func writeProviderTest(w http.ResponseWriter, resp providerTestResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// testProviderConnectivity routes a provider to the right live probe by protocol.
// OpenAI-compatible providers (the large majority) share one probe; Anthropic
// has its own; CLI/Azure are not key-testable here and say so plainly.
func testProviderConnectivity(req providerTestRequest) providerTestResponse {
	proto := strings.ToLower(strings.TrimSpace(req.Protocol))
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	key := strings.TrimSpace(req.APIKey)

	switch {
	case strings.HasSuffix(proto, "-cli"):
		return providerTestResponse{Message: "CLI providers are checked by detection (binary on PATH), not an API key test."}
	case proto == "azure":
		return providerTestResponse{Message: "Azure needs deployment-specific settings; it can't be live-tested here — save and verify in chat."}
	case proto == "anthropic" || proto == "anthropic-messages":
		return probeAnthropicAuth(base, key)
	default: // openai, openai-chat, openai-responses, and other openai-compatible
		return probeOpenAICompatAuth(base, key)
	}
}

// probeOpenAICompatAuth validates an OpenAI-compatible endpoint with GET
// {base}/models. A bearer key is sent when provided; local providers (e.g.
// Ollama) accept the call without one.
func probeOpenAICompatAuth(base, key string) providerTestResponse {
	if base == "" {
		return providerTestResponse{Message: "A base URL is required to test this provider."}
	}
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return providerTestResponse{Message: "The base URL is not a valid URL."}
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return doProbe(req, base)
}

// probeAnthropicAuth validates an Anthropic key with GET {base}/models using the
// x-api-key + anthropic-version headers.
func probeAnthropicAuth(base, key string) providerTestResponse {
	if key == "" {
		return providerTestResponse{Message: "An API key is required to test this provider."}
	}
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return providerTestResponse{Message: "The base URL is not a valid URL."}
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	return doProbe(req, base)
}

func doProbe(req *http.Request, base string) providerTestResponse {
	client := &http.Client{Timeout: providerProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return providerTestResponse{Message: fmt.Sprintf("Could not reach %s — check the base URL and your network. (%v)", base, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	return classifyProbeStatus(resp.StatusCode, base)
}

// classifyProbeStatus turns an HTTP status into a clear, user-facing verdict.
func classifyProbeStatus(code int, base string) providerTestResponse {
	switch {
	case code >= 200 && code < 300:
		return providerTestResponse{OK: true, Message: "Success — the endpoint accepted the key."}
	case code == http.StatusTooManyRequests:
		// Auth got far enough to be rate-limited, so the key is valid.
		return providerTestResponse{OK: true, Message: "Key accepted (the endpoint is currently rate-limiting requests)."}
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return providerTestResponse{Message: "Authentication failed — the API key was rejected."}
	case code == http.StatusNotFound:
		return providerTestResponse{Message: fmt.Sprintf("Reached the server, but %s/models was not found — check the base URL.", base)}
	default:
		return providerTestResponse{Message: fmt.Sprintf("Unexpected response from the endpoint (HTTP %d).", code)}
	}
}
