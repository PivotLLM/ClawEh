// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// TestNewDispatch_TemperatureInRequest checks the temperature an HTTP model
// receives: none when neither the defaults nor the agent set one (the model's
// own default applies), otherwise the configured value, the agent's winning.
func TestNewDispatch_TemperatureInRequest(t *testing.T) {
	ptr := func(f float64) *float64 { return &f }
	tests := []struct {
		name        string
		defaultTemp *float64
		agentTemp   *float64
		wantPresent bool
		wantTemp    float64
	}{
		{name: "unset sends none", wantPresent: false},
		{name: "default is sent", defaultTemp: ptr(0.7), wantPresent: true, wantTemp: 0.7},
		{name: "zero default is sent", defaultTemp: ptr(0), wantPresent: true, wantTemp: 0},
		{name: "agent overrides default", defaultTemp: ptr(0.7), agentTemp: ptr(1.3), wantPresent: true, wantTemp: 1.3},
		{name: "agent alone is sent", agentTemp: ptr(0.4), wantPresent: true, wantTemp: 0.4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer srv.Close()

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					BaseDir: t.TempDir(),
					Defaults: config.AgentDefaults{
						Models:      []string{"alice-model"},
						Temperature: tt.defaultTemp,
					},
				},
				Providers: []config.Provider{
					{Name: "test", Protocol: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "dummy"},
				},
				Models: []config.ModelConfig{
					{ModelName: "alice-model", Model: "test-model", Provider: "test", Enabled: true},
				},
			}
			agentCfg := &config.AgentConfig{ID: "alice", Temperature: tt.agentTemp}
			inst := mustNewAgentInstance(t, agentCfg, &cfg.Agents.Defaults, cfg, &mockProvider{})

			al := &AgentLoop{cfg: cfg, dispatcher: providers.NewProviderDispatcher(cfg)}
			turn := &llmTurn{
				al:         al,
				agent:      inst,
				candidates: []providers.FallbackCandidate{{Provider: "test", Model: "test-model", Alias: "alice-model"}},
				model:      "test-model",
			}
			d := turn.newDispatch(nil, 0)

			msgs := []providers.Message{{Role: "user", Content: "hi"}}
			if _, err := d.runProvider.Chat(context.Background(), msgs, nil, d.runModel, d.llmOpts); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			got, present := body["temperature"]
			if present != tt.wantPresent {
				t.Fatalf("temperature present = %v (%v), want %v", present, got, tt.wantPresent)
			}
			if tt.wantPresent && got != tt.wantTemp {
				t.Fatalf("temperature = %v, want %v", got, tt.wantTemp)
			}
		})
	}
}
