package agent

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// limitsCall is one provider call: the wire model, and the max_tokens and
// thinking_level it was sent (nil when absent).
type limitsCall struct {
	model     string
	maxTokens any
	thinking  any
}

// limitsProvider records every call and fails the wire models in fail with a
// rate limit, so the fallback chain moves on. It supports thinking, so a
// model's thinking_level is sent.
type limitsProvider struct {
	mu    sync.Mutex
	fail  map[string]bool
	calls []limitsCall
	// tools is the number of tool definitions of each call.
	tools []int
	// system is the content of the latest "[System: ...]" message sent.
	system string
}

func (p *limitsProvider) Chat(_ context.Context, msgs []providers.Message, tools []providers.ToolDefinition, model string, opts map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, limitsCall{model: model, maxTokens: opts["max_tokens"], thinking: opts["thinking_level"]})
	p.tools = append(p.tools, len(tools))
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "[System: ") {
			p.system = m.Content
		}
	}
	if p.fail[model] {
		return nil, errors.New(rateLimited)
	}
	return &providers.LLMResponse{Content: "from " + model}, nil
}

func (*limitsProvider) GetDefaultModel() string { return "alice-wire" }

func (*limitsProvider) SupportsThinking() bool { return true }

// limitsConfig has an agent whose models are models, in that order, or else
// Alice Model, Bob Model and Plain Model. Alice Model has its own max_tokens,
// context window and thinking level, Bob Model smaller limits and no
// thinking, Plain Model none of these (so the agents.defaults values apply)
// and no tools.
func limitsConfig(t *testing.T, models ...string) *config.Config {
	t.Helper()
	if len(models) == 0 {
		models = []string{"Alice Model", "Bob Model", "Plain Model"}
	}
	prov := config.Provider{Name: "Alice Co", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1/v1", APIKey: "k"}
	return &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            models,
				MaxTokens:         2048,
				ContextWindow:     64000,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{{ID: "main", Name: "Main", Default: true}},
		},
		Providers: []config.Provider{prov},
		Models: []config.ModelConfig{
			{ModelName: "Alice Model", Model: "alice-wire", Provider: prov.Name, Enabled: true, MaxTokens: 32768, ContextWindow: 200000, ThinkingLevel: "high"},
			{ModelName: "Bob Model", Model: "bob-wire", Provider: prov.Name, Enabled: true, MaxTokens: 4096, ContextWindow: 32000},
			{ModelName: "Plain Model", Model: "plain-wire", Provider: prov.Name, Enabled: true, NoTools: true},
		},
	}
}

// newLimitsLoop is a loop on limitsConfig(models...) whose agent calls p.
func newLimitsLoop(t *testing.T, p *limitsProvider, models ...string) (*AgentLoop, *AgentInstance) {
	t.Helper()
	al := mustNewAgentLoop(t, limitsConfig(t, models...), bus.NewMessageBus(), p, nil)
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	if len(agent.Candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(agent.Candidates))
	}
	return al, agent
}

// runLimitsTurn runs one whole turn of agent on sessionKey.
func runLimitsTurn(t *testing.T, al *AgentLoop, agent *AgentInstance, sessionKey string) {
	t.Helper()
	if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey: sessionKey, Channel: "cli", ChatID: "direct", UserMessage: "hi",
	}); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
}

// Each request carries the max_tokens and thinking level of the model it is
// sent to: the first model, the one /model selected, a fallback candidate
// either way round, or the defaults for a model that sets none.
func TestRequestLimits_FollowModelCalled(t *testing.T) {
	tests := []struct {
		name   string
		active int
		fail   map[string]bool
		want   []limitsCall
	}{
		{"first model", 0, nil, []limitsCall{{"alice-wire", 32768, "high"}}},
		{"model selected with /model", 1, nil, []limitsCall{{"bob-wire", 4096, nil}}},
		{"fallback from the first model", 0, map[string]bool{"alice-wire": true}, []limitsCall{{"alice-wire", 32768, "high"}, {"bob-wire", 4096, nil}}},
		{"fallback from the selected model", 1, map[string]bool{"bob-wire": true}, []limitsCall{{"bob-wire", 4096, nil}, {"alice-wire", 32768, "high"}}},
		{"model without max_tokens", 2, nil, []limitsCall{{"plain-wire", 2048, nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &limitsProvider{fail: tt.fail}
			al, agent := newLimitsLoop(t, p)
			const sessionKey = "limits"
			if err := al.setActiveModelIndex(agent, sessionKey, tt.active); err != nil {
				t.Fatalf("setActiveModelIndex: %v", err)
			}
			opts := processOptions{SessionKey: sessionKey, Channel: "cli", ChatID: "direct", UserMessage: "hi"}
			cm, release := al.getContextManager(agent, sessionKey)
			defer release()
			runIteration(t, al, agent, []providers.Message{{Role: "user", Content: "hi"}}, opts, cm)

			if len(p.calls) != len(tt.want) {
				t.Fatalf("calls = %v, want %v", p.calls, tt.want)
			}
			for i, want := range tt.want {
				if p.calls[i] != want {
					t.Errorf("call %d = %v, want %v", i, p.calls[i], want)
				}
			}
		})
	}
}

// Without a fallback chain the request goes straight to the turn's model,
// with that model's max_tokens and thinking level.
func TestRequestLimits_DirectPath(t *testing.T) {
	tests := []struct {
		name   string
		active int
		want   limitsCall
	}{
		{"first model", 0, limitsCall{"Alice Model", 32768, "high"}},
		{"model selected with /model", 1, limitsCall{"Bob Model", 4096, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &limitsProvider{}
			al, agent := newLimitsLoop(t, p)
			al.fallback = nil
			const sessionKey = "direct"
			if err := al.setActiveModelIndex(agent, sessionKey, tt.active); err != nil {
				t.Fatalf("setActiveModelIndex: %v", err)
			}
			runLimitsTurn(t, al, agent, sessionKey)
			if len(p.calls) != 1 || p.calls[0] != tt.want {
				t.Fatalf("calls = %v, want [%v]", p.calls, tt.want)
			}
		})
	}
}

// A model's no_tools follows /model both ways: an agent whose first model
// takes no tools sends them after /model to one that does, and the reverse.
func TestNoTools_FollowsActiveModel(t *testing.T) {
	tests := []struct {
		name      string
		models    []string
		active    int
		wantTools bool
	}{
		{"first model takes no tools", []string{"Plain Model", "Alice Model", "Bob Model"}, 0, false},
		{"/model to a model that takes tools", []string{"Plain Model", "Alice Model", "Bob Model"}, 1, true},
		{"first model takes tools", nil, 0, true},
		{"/model to a model that takes no tools", nil, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &limitsProvider{}
			al, agent := newLimitsLoop(t, p, tt.models...)
			const sessionKey = "tools"
			if err := al.setActiveModelIndex(agent, sessionKey, tt.active); err != nil {
				t.Fatalf("setActiveModelIndex: %v", err)
			}
			runLimitsTurn(t, al, agent, sessionKey)
			if len(p.tools) == 0 {
				t.Fatal("no model call")
			}
			if got := p.tools[0] > 0; got != tt.wantTools {
				t.Fatalf("tools sent = %d, want some: %v", p.tools[0], tt.wantTools)
			}
		})
	}
}

// A late (async) tool result is capped for the context window of the model
// the session runs on: Bob Model's 32000 tokens cap it, Alice Model's 200000
// do not.
func TestAsyncResultCap_UsesActiveModelWindow(t *testing.T) {
	tests := []struct {
		name          string
		active        int
		wantTruncated bool
	}{
		{"large window", 0, false},
		{"small window selected with /model", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &limitsProvider{}
			al, agent := newLimitsLoop(t, p)
			sessionKey := routing.BuildAgentMainSessionKey(agent.ID)
			if err := al.setActiveModelIndex(agent, sessionKey, tt.active); err != nil {
				t.Fatalf("setActiveModelIndex: %v", err)
			}
			if _, err := al.processSystemMessage(context.Background(), bus.InboundMessage{
				Channel:  "system",
				SenderID: "async:agent_spawn",
				ChatID:   "chat-1",
				Content:  strings.Repeat("r", 100_000),
				Metadata: map[string]string{bus.MetaOriginChannel: "telegram"},
			}, nil); err != nil {
				t.Fatalf("processSystemMessage: %v", err)
			}
			truncated := strings.Contains(p.system, "[output truncated: kept ")
			if truncated != tt.wantTruncated {
				t.Fatalf("truncated = %v, want %v (message %d chars)", truncated, tt.wantTruncated, len(p.system))
			}
			if truncated && len(p.system) > toolResultCap(32000)+512 {
				t.Fatalf("message is %d chars, Bob Model's cap is %d", len(p.system), toolResultCap(32000))
			}
		})
	}
}

// Context-window retries halve each model's own max_tokens, never below the
// floor, without changing the options the candidates share.
func TestRequestOptions_HalvedMaxTokensPerModel(t *testing.T) {
	agent := &AgentInstance{MaxTokens: 1000, ThinkingLevel: ThinkingOff, modelLimits: map[string]runLimits{
		"Alice Model": {maxTokens: 32768, thinkingLevel: ThinkingOff},
		"Bob Model":   {maxTokens: 600, thinkingLevel: ThinkingOff},
	}}
	turn := &llmTurn{agent: agent}
	d := &llmDispatch{llmOpts: map[string]any{"prompt_cache_key": "main"}, maxTokensHalvings: 2}
	for model, want := range map[string]int{"Alice Model": 8192, "Bob Model": 300, "Other Model": 500} {
		if got := turn.requestOptions(d, false, &mockProvider{}, model)["max_tokens"]; got != want {
			t.Errorf("%s: max_tokens = %v, want %d", model, got, want)
		}
	}
	if _, ok := d.llmOpts["max_tokens"]; ok {
		t.Error("the shared options were changed")
	}
}

// contextWindowOf is the context window of the session's manager, derived
// from its token estimate and the share of the window it fills.
func contextWindowOf(t *testing.T, al *AgentLoop, agent *AgentInstance, sessionKey string) int {
	t.Helper()
	cm, release := al.getContextManager(agent, sessionKey)
	defer release()
	st := cm.Stats()
	if st.EstimatedTokens == 0 || st.ContextWindowPct == 0 {
		t.Fatalf("stats carry no estimate: %+v", st)
	}
	return int(math.Round(float64(st.EstimatedTokens) * 100 / st.ContextWindowPct))
}

// addWindowHistory gives the session some history to measure.
func addWindowHistory(t *testing.T, agent *AgentInstance, sessionKey string) {
	t.Helper()
	for range 4 {
		addMessage(t, agent.Sessions, sessionKey, "user", "a question about the weather")
		addMessage(t, agent.Sessions, sessionKey, "assistant", "a long answer about the weather")
	}
}

// A manager built for a session whose active model was saved earlier takes
// that model's context window.
func TestContextWindow_BuiltForSavedActiveModel(t *testing.T) {
	al, agent := newLimitsLoop(t, &limitsProvider{})
	const sessionKey = "saved"
	addWindowHistory(t, agent, sessionKey)
	if err := al.setActiveModelIndex(agent, sessionKey, 1); err != nil {
		t.Fatalf("setActiveModelIndex: %v", err)
	}
	// Read the index back from the session store, as after a restart.
	al.activeModelMu.Lock()
	delete(al.activeModelIdx, activeModelCacheKey(agent.ID, sessionKey))
	al.activeModelMu.Unlock()

	if got := contextWindowOf(t, al, agent, sessionKey); got != 32000 {
		t.Fatalf("context window = %d, want Bob Model's 32000", got)
	}
}

// A turn trims and compacts to the context window of the model it runs on,
// also when the session's manager was built (or being built) for the model
// active before /model changed it.
func TestContextWindow_AppliedAtTurnStart(t *testing.T) {
	al, agent := newLimitsLoop(t, &limitsProvider{})
	const sessionKey = "window"
	addWindowHistory(t, agent, sessionKey)
	cm, release := al.getContextManager(agent, sessionKey)
	defer release() // held across the switch, as a manager mid-build would be
	if got := contextWindowOf(t, al, agent, sessionKey); got != 200000 {
		t.Fatalf("first model: context window = %d, want 200000", got)
	}

	for _, step := range []struct {
		active int
		want   int
	}{{1, 32000}, {2, 64000}, {0, 200000}} {
		if err := al.setActiveModelIndex(agent, sessionKey, step.active); err != nil {
			t.Fatalf("setActiveModelIndex: %v", err)
		}
		runLimitsTurn(t, al, agent, sessionKey)
		if got := contextWindowOf(t, al, agent, sessionKey); got != step.want {
			t.Errorf("after /model %d and a turn: context window = %d, want %d", step.active, got, step.want)
		}
	}
	got, releaseGot := al.getContextManager(agent, sessionKey)
	defer releaseGot()
	if got != cm {
		t.Error("the session's manager was replaced; want the window changed in place")
	}
}

// The turn's assembly counts the tool schemas of the model the turn chose,
// not of whatever /model selected since.
func TestAssembleRequest_UsesTurnModel(t *testing.T) {
	al, agent := newLimitsLoop(t, &limitsProvider{})
	const sessionKey = "assemble"
	// Plain Model, which takes no tools, is active now; Alice Model took
	// them when the turn started, and the other way round.
	if err := al.setActiveModelIndex(agent, sessionKey, 2); err != nil {
		t.Fatalf("setActiveModelIndex: %v", err)
	}
	opts := processOptions{SessionKey: sessionKey, turnModel: &turnModel{model: "Alice Model", limits: agent.limitsFor("Alice Model")}}
	if got := al.assembleRequest(context.Background(), agent, nil, opts).ToolDefinitionTokens; got == 0 {
		t.Error("turn on Alice Model: no tool schemas counted")
	}
	if err := al.setActiveModelIndex(agent, sessionKey, 0); err != nil {
		t.Fatalf("setActiveModelIndex: %v", err)
	}
	opts.turnModel = &turnModel{model: "Plain Model", limits: agent.limitsFor("Plain Model")}
	if got := al.assembleRequest(context.Background(), agent, nil, opts).ToolDefinitionTokens; got != 0 {
		t.Errorf("turn on Plain Model: %d tool schema tokens counted, want 0", got)
	}
}
