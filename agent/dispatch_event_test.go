package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// readLogRecords decodes one JSON object per line out of buf.
func readLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("failed to decode log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// finishRecords returns every "LLM finish" record in buf.
func finishRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range readLogRecords(t, buf) {
		if rec["message"] == "LLM finish" {
			out = append(out, rec)
		}
	}
	return out
}

// assertFields fails for each key of want whose value in got differs, and for
// each key of absent that got carries.
func assertFields(t *testing.T, got, want map[string]any, absent ...string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v (%T), want %v (%T)", k, got[k], got[k], v, v)
		}
	}
	for _, k := range absent {
		if v, has := got[k]; has {
			t.Errorf("unexpected %s = %v", k, v)
		}
	}
}

// cloneOfAlice is a temporary clone of alice, as a spawned worker runs.
func cloneOfAlice() *AgentInstance {
	id := "1a2b3c4d-0000-4000-8000-000000000000"
	return &AgentInstance{
		ID:   id,
		Spec: agentreg.Spec{ID: id, Origin: agentreg.OriginTemp, SourceID: "alice"},
	}
}

func TestEmitLLMFinishEvent_SuccessPath_UsesProviderStatus(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	resp := &providers.LLMResponse{
		Status: &providers.DispatchStatus{
			Success:             true,
			Model:               "claude-haiku-4-5-20251001",
			NumTurns:            1,
			InputTokens:         12,
			OutputTokens:        4,
			CacheReadTokens:     3,
			CacheCreationTokens: 100,
			StopReason:          "end_turn",
			CostUSD:             new(0.005),
			DurationMs:          150,
			BytesSent:           512,
			BytesReceived:       1024,
		},
	}

	emitLLMFinishEvent(llmFinish{
		caller:         llmCaller{agentID: "alice", label: "alice"},
		kind:           llmKindAgent,
		iteration:      3,
		ourProvider:    "Anthropic",
		ourModel:       "Haiku",
		requestedModel: "Haiku",
		elapsedMs:      100,
		resp:           resp,
	})

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"level":                 "info",
		"agent_id":              "alice",
		"agent":                 "alice",
		"kind":                  "agent",
		"iteration":             float64(3),
		"our_provider":          "Anthropic",
		"our_model":             "Haiku",
		"model":                 "claude-haiku-4-5-20251001",
		"success":               true,
		"input_tokens":          float64(12),
		"prompt_tokens":         float64(115),
		"cache_creation_tokens": float64(100),
		"cost":                  0.005,
		"bytes_sent":            float64(512),
		"bytes_received":        float64(1024),
	}, "provider", "cost_usd", "error", "parent_agent_id")
}

func TestEmitLLMFinishEvent_ErrorPathWithoutStatus_SynthesizesFallback(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	emitLLMFinishEvent(llmFinish{
		caller:         llmCaller{agentID: "bob", label: "bob"},
		kind:           llmKindAgent,
		iteration:      1,
		ourProvider:    "OpenAI",
		ourModel:       "GPT",
		requestedModel: "gpt-4o",
		elapsedMs:      50,
		err:            errors.New(strings.Repeat("x", 600)),
	})

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	finish := records[0]
	assertFields(t, finish, map[string]any{
		"success":     false,
		"model":       "gpt-4o",
		"stop_reason": "error",
		"cost":        costUnknown,
		"duration_ms": float64(50),
	})
	errStr, ok := finish["error"].(string)
	if !ok {
		t.Fatalf("error field missing or wrong type: %v", finish["error"])
	}
	if len(errStr) != 500 {
		t.Errorf("error len = %d, want 500 (truncated)", len(errStr))
	}
}

func TestEmitLLMFinishEvent_ErrorPathWithPartialStatus_BytesPreserved(t *testing.T) {
	resp := &providers.LLMResponse{
		Status: &providers.DispatchStatus{
			Success:    false,
			Model:      "claude-sonnet-4-5",
			StopReason: "error",
			BytesSent:  200,
		},
	}
	got := llmFinish{
		caller: llmCaller{agentID: "alice", label: "alice"},
		kind:   llmKindAgent,
		resp:   resp,
		err:    errors.New("network failed"),
	}.fields()
	assertFields(t, got, map[string]any{
		"bytes_sent":     int64(200),
		"bytes_received": int64(0),
		"error":          "network failed",
	})
}

// A cost the provider reported is printed as the number, zero included;
// unknown is printed when the provider returned none (a nil CostUSD) or
// there is no status at all.
func TestLLMFinishFields_Cost(t *testing.T) {
	tests := []struct {
		name   string
		status *providers.DispatchStatus
		want   any
	}{
		{name: "reported", status: &providers.DispatchStatus{Success: true, CostUSD: new(0.0123)}, want: 0.0123},
		{name: "reported zero", status: &providers.DispatchStatus{Success: true, CostUSD: new(0.0)}, want: 0.0},
		{name: "not reported", status: &providers.DispatchStatus{Success: true}, want: costUnknown},
		{name: "no status", status: nil, want: costUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := llmFinish{
				caller: llmCaller{agentID: "alice", label: "alice"},
				kind:   llmKindAgent,
				resp:   &providers.LLMResponse{Status: tt.status},
			}.fields()
			if got["cost"] != tt.want {
				t.Errorf("cost = %v, want %v", got["cost"], tt.want)
			}
		})
	}
}

// A main-loop call names the agent, its session and turn, kind=agent and no
// parent.
func TestLLMTurnEmitFinish_MainAgent(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	turn := &llmTurn{
		agent:     &AgentInstance{ID: "alice"},
		model:     "Sonnet",
		iteration: 2,
		opts:      processOptions{SessionKey: "agent:alice:main", Channel: "telegram", ChatID: "42"},
	}
	resp := &providers.LLMResponse{Status: &providers.DispatchStatus{Success: true, Model: "claude-sonnet-4-5", InputTokens: 7}}
	turn.emitFinish(withTurnID(context.Background(), "abcd1234"), "Claude CLI", "Sonnet", "Sonnet", 10*time.Millisecond, resp, nil)

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1", len(records))
	}
	assertFields(t, records[0], map[string]any{
		"agent_id":     "alice",
		"agent":        "alice",
		"kind":         "agent",
		"iteration":    float64(2),
		"our_provider": "Claude CLI",
		"our_model":    "Sonnet",
		"model":        "claude-sonnet-4-5",
		"session_key":  "agent:alice:main",
		"channel":      "telegram",
		"chat_id":      "42",
		"turn_id":      "abcd1234",
		"input_tokens": float64(7),
	}, "parent_agent_id", "provider")
}

// A clone's call (a spawned worker) is kind=subagent, labelled as the clone
// and naming the agent it was cloned from. Empty session fields are left off.
func TestLLMTurnEmitFinish_Subagent(t *testing.T) {
	clone := cloneOfAlice()
	turn := &llmTurn{agent: clone, model: "Haiku", iteration: 1}

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	turn.emitFinish(context.Background(), "Anthropic", "Haiku", "Haiku", 5*time.Millisecond, nil, nil)

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1", len(records))
	}
	assertFields(t, records[0], map[string]any{
		"agent_id":        clone.ID,
		"agent":           "alice (clone 1a2b3c4d)",
		"parent_agent_id": "alice",
		"kind":            "subagent",
		"our_provider":    "Anthropic",
		"our_model":       "Haiku",
	}, "session_key", "channel", "chat_id", "turn_id")
}

// failoverProvider fails the primary's wire model with a rate limit and
// answers the backup's.
type failoverProvider struct{}

func (failoverProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, model string, _ map[string]any) (*providers.LLMResponse, error) {
	if model == "primary-wire" {
		return nil, errors.New("status 429: rate limit exceeded")
	}
	return &providers.LLMResponse{
		Content: "from backup",
		Status:  &providers.DispatchStatus{Success: true, Model: "backup-wire-2026"},
	}, nil
}

func (failoverProvider) GetDefaultModel() string { return "primary-wire" }

// After a failover each call has its line, and the answering one names the
// candidate that answered: its provider and its model_name, not the primary's.
func TestRequestModel_FailoverNamesAnsweringModel(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}, fallback: providers.NewFallbackChain(providers.NewCooldownTracker())}
	turn := &llmTurn{
		al:    al,
		agent: &AgentInstance{ID: "bob", Provider: failoverProvider{}},
		candidates: []providers.FallbackCandidate{
			{Provider: "Primary Co", Model: "primary-wire", Alias: "Primary"},
			{Provider: "Backup Co", Model: "backup-wire", Alias: "Backup"},
		},
		model:    "Primary",
		messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	d := &llmDispatch{provider: "Primary Co", runProvider: failoverProvider{}, runModel: "primary-wire"}

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	resp, err := turn.requestModel(context.Background(), d)
	if err != nil {
		t.Fatalf("requestModel: %v", err)
	}
	if resp.Content != "from backup" {
		t.Fatalf("content = %q, want the backup's answer", resp.Content)
	}

	records := finishRecords(t, buf)
	if len(records) != 2 {
		t.Fatalf("finish records = %d, want one per call (2): %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"our_provider": "Primary Co",
		"our_model":    "Primary",
		"model":        "primary-wire",
		"success":      false,
		"error":        "status 429: rate limit exceeded",
	})
	assertFields(t, records[1], map[string]any{
		"our_provider": "Backup Co",
		"our_model":    "Backup",
		"model":        "backup-wire-2026",
		"success":      true,
	})
}

// TestRunLLMIteration_EmitsDispatchAndFinishPerCall drives the real loop
// through one LLM call and verifies it logs exactly one "LLM dispatch" and one
// "LLM finish" event, with the finish carrying the provider's DispatchStatus.
func TestRunLLMIteration_EmitsDispatchAndFinishPerCall(t *testing.T) {
	al := newTestAgentLoop(t).al

	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{{
			Content: "hi from Alice",
			Status: &providers.DispatchStatus{
				Success:       true,
				Model:         "gpt-4o-2024-11-20",
				InputTokens:   3,
				OutputTokens:  2,
				StopReason:    "stop",
				BytesSent:     128,
				BytesReceived: 256,
			},
		}},
		errors: []error{nil},
	}

	messages := []providers.Message{{Role: "user", Content: "hi"}}
	opts := processOptions{
		SessionKey:   "dispatch-events",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "hi",
		SendResponse: false,
	}
	cm, releaseTestCM := al.getContextManager(agent, opts.SessionKey)
	defer releaseTestCM()

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	it := runIteration(t, al, agent, messages, opts, cm)
	if it.content != "hi from Alice" {
		t.Errorf("content = %q, want the provider's answer", it.content)
	}

	var dispatches, finishes int
	var finish map[string]any
	for _, rec := range readLogRecords(t, buf) {
		switch rec["message"] {
		case "LLM dispatch":
			dispatches++
		case "LLM finish":
			finishes++
			finish = rec
		}
	}
	if dispatches != 1 {
		t.Errorf("dispatch count = %d, want 1", dispatches)
	}
	if finishes != 1 {
		t.Errorf("finish count = %d, want 1", finishes)
	}
	if finish == nil {
		t.Fatal("missing finish record")
	}
	if v, ok := finish["success"].(bool); !ok || !v {
		t.Errorf("success = %v, want true", finish["success"])
	}
	if v, ok := finish["bytes_sent"].(float64); !ok || v != 128 {
		t.Errorf("bytes_sent = %v, want 128", finish["bytes_sent"])
	}
	if v, ok := finish["bytes_received"].(float64); !ok || v != 256 {
		t.Errorf("bytes_received = %v, want 256", finish["bytes_received"])
	}
	if finish["model"] != "gpt-4o-2024-11-20" {
		t.Errorf("model = %v, want gpt-4o-2024-11-20", finish["model"])
	}
	assertFields(t, finish, map[string]any{
		"kind":        "agent",
		"agent":       agent.ID,
		"our_model":   "test-model",
		"session_key": "dispatch-events",
		"channel":     "cli",
		"chat_id":     "direct",
		"cost":        costUnknown,
	}, "provider", "parent_agent_id")
}

// openAIStub serves one canned chat completion for every request.
func openAIStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"model":"wire-2026","choices":[{"message":{"role":"assistant","content":"{\"summary\":\"ok\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sideModelLoop is a loop whose config has one model, alias, reached
// through Bob's API at srv.
func sideModelLoop(srv *httptest.Server, alias string) *AgentLoop {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "Bob's API", Protocol: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "dummy"},
		},
		Models: []config.ModelConfig{
			{ModelName: alias, Model: "wire", Provider: "Bob's API", Enabled: true},
		},
	}
	cfg.Summarization.Models = []string{alias}
	return &AgentLoop{cfg: cfg, dispatcher: providers.NewProviderDispatcher(cfg)}
}

// A compression call (context engine or cognitive memory) leaves one finish
// line naming the summarization model that answered.
func TestCompressModelCaller_EmitsFinish(t *testing.T) {
	al := sideModelLoop(openAIStub(t), "Summarizer")
	alice := &AgentInstance{ID: "alice", Provider: &mockProvider{}, Model: "Primary"}
	caller, _ := al.newCompressModelCaller(alice, "agent:alice:main")

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	if _, err := caller.Complete(withTurnID(context.Background(), "feed0001"), ctxengine.ModelRequest{System: "s", User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"kind":         "compression",
		"agent_id":     "alice",
		"agent":        "alice",
		"our_provider": "Bob's API",
		"our_model":    "Summarizer",
		"session_key":  "agent:alice:main",
		"turn_id":      "feed0001",
		"success":      true,
	}, "iteration", "parent_agent_id")
}

// A vision-describe call leaves one finish line naming the vision model.
func TestVisionClient_EmitsFinish(t *testing.T) {
	al := sideModelLoop(openAIStub(t), "Eyes")
	clone := cloneOfAlice()
	client, ok := al.buildVisionLLMClient(al.cfg, clone, "Eyes")
	if !ok {
		t.Fatal("vision client not built")
	}

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	if _, err := client.Complete(context.Background(), []providers.Message{{Role: "user", Content: "describe"}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"kind":            "vision",
		"agent":           "alice (clone 1a2b3c4d)",
		"parent_agent_id": "alice",
		"our_provider":    "Bob's API",
		"our_model":       "Eyes",
		"success":         true,
	}, "iteration", "session_key")
}

// A summarization model that resolves to no models entry runs on the agent's
// own provider: the line names it by the configured string and by the
// defaults' provider.
func TestCompressModelCaller_UnresolvedModelNamesAgentProvider(t *testing.T) {
	al := sideModelLoop(openAIStub(t), "Main")
	al.cfg.Agents.Defaults.Models = []string{"Main"}
	al.cfg.Summarization.Models = []string{"bob-summarizer"}
	alice := &AgentInstance{ID: "alice", Provider: &mockProvider{}, Model: "Main"}
	caller, _ := al.newCompressModelCaller(alice, "agent:alice:main")

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	if _, err := caller.Complete(context.Background(), ctxengine.ModelRequest{System: "s", User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"kind":         "compression",
		"our_provider": "Bob's API",
		"our_model":    "bob-summarizer",
		"session_key":  "agent:alice:main",
	})
}

// Memory consolidation runs the compression chain but serves no
// conversation, so its line carries no session_key.
func TestMemoryModelCaller_FinishHasNoSessionKey(t *testing.T) {
	al := sideModelLoop(openAIStub(t), "Summarizer")
	alice := &AgentInstance{ID: "alice", Provider: &mockProvider{}, Model: "Primary"}
	caller := al.NewMemoryModelCaller(alice)

	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()
	if _, err := caller.Complete(context.Background(), consolidate.ModelRequest{System: "s", User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	records := finishRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("finish records = %d, want 1: %v", len(records), records)
	}
	assertFields(t, records[0], map[string]any{
		"kind":      "compression",
		"agent_id":  "alice",
		"our_model": "Summarizer",
	}, "session_key")
}

// prompt_tokens is the whole prompt: the provider's usage count when it gave
// one, otherwise the uncached input plus the cache reads and writes.
func TestLLMFinishFields_PromptTokens(t *testing.T) {
	status := &providers.DispatchStatus{Success: true, InputTokens: 10, CacheReadTokens: 200, CacheCreationTokens: 30}
	tests := []struct {
		name string
		resp *providers.LLMResponse
		want int
	}{
		{name: "from usage", resp: &providers.LLMResponse{Status: status, Usage: &providers.UsageInfo{PromptTokens: 240}}, want: 240},
		{name: "summed from status", resp: &providers.LLMResponse{Status: status}, want: 240},
		{name: "no status", resp: nil, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := llmFinish{caller: llmCaller{agentID: "alice", label: "alice"}, kind: llmKindAgent, resp: tt.resp}.fields()
			if got["prompt_tokens"] != tt.want {
				t.Errorf("prompt_tokens = %v, want %d", got["prompt_tokens"], tt.want)
			}
		})
	}
}
