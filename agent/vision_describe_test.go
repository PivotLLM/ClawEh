// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

// stubVisionClient is a fake vision side-model: it records the image Media it was
// handed and returns a canned description (or a canned error).
type stubVisionClient struct {
	reply    string
	err      error
	model    string
	gotMedia []string
	calls    int
}

func (s *stubVisionClient) Complete(_ context.Context, msgs []providers.Message) (visionReply, error) {
	s.calls++
	for _, m := range msgs {
		if len(m.Media) > 0 {
			s.gotMedia = m.Media
		}
	}
	if s.err != nil {
		return visionReply{}, s.err
	}
	return visionReply{Content: s.reply}, nil
}

func (s *stubVisionClient) Model() string { return s.model }

func TestDescribeImages_ReturnsDescription(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}}
	stub := &stubVisionClient{reply: "a red square", model: "vmodel"}
	agent := &AgentInstance{ID: "a1", VisionClients: []visionClient{stub}}

	desc, ok := al.describeImages(context.Background(), agent, []string{"data:image/png;base64,AAA"}, "what color is it")
	if !ok || desc != "a red square" {
		t.Fatalf("describeImages = (%q, %v), want (a red square, true)", desc, ok)
	}
	// The image must actually have been carried to the vision model.
	if len(stub.gotMedia) != 1 || stub.gotMedia[0] != "data:image/png;base64,AAA" {
		t.Fatalf("vision model did not receive the image: %v", stub.gotMedia)
	}
}

func TestDescribeImages_NoVisionConfigured(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}}
	agent := &AgentInstance{ID: "a1"} // no VisionClients

	desc, ok := al.describeImages(context.Background(), agent, []string{"data:image/png;base64,AAA"}, "")
	if ok || desc != "" {
		t.Fatalf("describeImages = (%q, %v), want (\"\", false)", desc, ok)
	}
}

// When the first client errors, the next in the chain is tried.
func TestDescribeImages_FallsThroughChain(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}}
	bad := &stubVisionClient{err: errors.New("boom"), model: "bad"}
	good := &stubVisionClient{reply: "a blue circle", model: "good"}
	agent := &AgentInstance{ID: "a1", VisionClients: []visionClient{bad, good}}

	desc, ok := al.describeImages(context.Background(), agent, []string{"data:image/png;base64,AAA"}, "")
	if !ok || desc != "a blue circle" {
		t.Fatalf("describeImages = (%q, %v), want (a blue circle, true)", desc, ok)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Fatalf("chain not tried in order: bad=%d good=%d", bad.calls, good.calls)
	}
}

// Flow B: a text-only primary with a vision side-model configured folds the
// description into the message text and clears the image Media.
func TestDescribeInboundMedia_NonVisionPrimaryFoldsDescription(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{
		Models: []config.ModelConfig{{ModelName: "deepseek", Enabled: true, Vision: config.VisionOff}},
	}}
	stub := &stubVisionClient{reply: "a cat on a mat"}
	agent := &AgentInstance{ID: "a1", Model: "deepseek", VisionClients: []visionClient{stub}}

	userMsg := providers.Message{Role: "user", Content: "what is this", Media: []string{"data:image/png;base64,AAA"}}
	al.describeInboundMedia(context.Background(), agent, &userMsg)

	if userMsg.Media != nil {
		t.Fatalf("expected Media cleared, got %v", userMsg.Media)
	}
	if !strings.Contains(userMsg.Content, "a cat on a mat") {
		t.Fatalf("description not folded in: %q", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "what is this") {
		t.Fatalf("original text lost: %q", userMsg.Content)
	}
}

// A vision-capable primary keeps the image untouched (the normal path handles it).
func TestDescribeInboundMedia_VisionPrimaryUntouched(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{
		Models: []config.ModelConfig{{ModelName: "gpt5", Enabled: true, Vision: config.VisionUserMessage}},
	}}
	stub := &stubVisionClient{reply: "should not be called"}
	agent := &AgentInstance{ID: "a1", Model: "gpt5", VisionClients: []visionClient{stub}}

	userMsg := providers.Message{Role: "user", Content: "hi", Media: []string{"data:image/png;base64,AAA"}}
	al.describeInboundMedia(context.Background(), agent, &userMsg)

	if len(userMsg.Media) != 1 {
		t.Fatalf("vision primary should keep image, Media=%v", userMsg.Media)
	}
	if stub.calls != 0 {
		t.Fatalf("vision side-model should not be invoked for a vision primary")
	}
}

// No vision model configured => today's behavior: image left for the normal
// (strip) path, message unchanged.
func TestDescribeInboundMedia_NoVisionConfiguredUntouched(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{
		Models: []config.ModelConfig{{ModelName: "deepseek", Enabled: true, Vision: config.VisionOff}},
	}}
	agent := &AgentInstance{ID: "a1", Model: "deepseek"} // no VisionClients

	userMsg := providers.Message{Role: "user", Content: "hi", Media: []string{"data:image/png;base64,AAA"}}
	al.describeInboundMedia(context.Background(), agent, &userMsg)

	if len(userMsg.Media) != 1 || userMsg.Content != "hi" {
		t.Fatalf("expected message untouched, got Content=%q Media=%v", userMsg.Content, userMsg.Media)
	}
}

// flowAImageTool returns an image (like file_view_image / an MCP screenshot) plus
// a ForLLM note carrying a focus line.
type flowAImageTool struct{}

func (flowAImageTool) Name() string        { return "grab_image" }
func (flowAImageTool) Description() string { return "returns an image" }
func (flowAImageTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (flowAImageTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return &tools.ToolResult{
		ForLLM: "[viewing image cat.png]\nFocus: count the cats",
		Images: []string{"data:image/png;base64,AAA"},
	}
}

// Flow A: a tool returns an image, the active model is text-only, and a vision
// side-model is configured — the description is injected as a follow-up user
// turn the next dispatch sees.
func TestFlowA_InjectsDescriptionForNonVisionModel(t *testing.T) {
	al := newTestAgentLoop(t).al

	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Tools.Register(flowAImageTool{})
	if agent.Config != nil {
		agent.Config.Tools = []string{"*"}
	}
	stub := &stubVisionClient{reply: "two cats", model: "vmodel"}
	agent.VisionClients = []visionClient{stub}

	// Iteration 1: call the image tool. Iteration 2: plain final answer.
	resps := []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "t1", Type: "function", Name: "grab_image",
			Function: &providers.FunctionCall{Name: "grab_image", Arguments: "{}"},
		}}},
		{Content: "There are two cats."},
	}
	agent.Provider = &sequenceProvider{responses: resps, errors: []error{nil, nil}}

	opts := processOptions{
		SessionKey:   "flowa",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "how many cats?",
		SendResponse: false,
	}
	cm, release := al.getContextManager(agent, opts.SessionKey)
	defer release()

	runIteration(t, al, agent, []providers.Message{{Role: "user", Content: "how many cats?"}}, opts, cm)

	// The vision side-model must have received the tool's image.
	if len(stub.gotMedia) != 1 {
		t.Fatalf("vision model did not receive the tool image: %v", stub.gotMedia)
	}
	// The 2nd model dispatch must have seen the injected description.
	sp, ok := agent.Provider.(*sequenceProvider)
	if !ok {
		t.Fatalf("provider is %T, want *sequenceProvider", agent.Provider)
	}
	found := false
	for _, m := range sp.lastMessages {
		if strings.Contains(m.Content, "two cats") && strings.Contains(m.Content, "cannot view images") {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected vision description not found in dispatch messages: %+v", sp.lastMessages)
	}
}

// The vision-describe chain is the models ticked Vision on the Task models
// page, in page order; a ticked model that is missing is skipped, and with
// none ticked the feature is off.
func TestWireVisionClients_TaskModelsInPageOrder(t *testing.T) {
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true},
		{ModelName: "v1", Model: "v1-wire", Provider: "p", Enabled: true},
		{ModelName: "v2", Model: "v2-wire", Provider: "p", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	cfg.TaskModels.Models = []config.TaskModel{
		{Model: "v2", Vision: true},
		{Model: "gone", Vision: true},
		{Model: "alpha", Text: true},
		{Model: "v1", Vision: true, STT: true},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, providers.NewProviderDispatcher(cfg))
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}

	al.wireVisionClients(cfg, agent)
	got := make([]string, 0, len(agent.VisionClients))
	for _, c := range agent.VisionClients {
		pc, ok := c.(*providerVisionClient)
		if !ok {
			t.Fatalf("client is %T", c)
		}
		got = append(got, pc.alias+"="+pc.model)
	}
	if want := []string{"v2=v2-wire", "v1=v1-wire"}; !slices.Equal(got, want) {
		t.Fatalf("vision chain = %v, want %v", got, want)
	}
	if agent.EffectiveVisionModel != "v2" {
		t.Errorf("EffectiveVisionModel = %q, want v2", agent.EffectiveVisionModel)
	}

	cfg.TaskModels.Models = []config.TaskModel{{Model: "v1", STT: true}}
	al.wireVisionClients(cfg, agent)
	if len(agent.VisionClients) != 0 || agent.EffectiveVisionModel != "" {
		t.Fatalf("with no Vision model: %d clients, effective %q, want none", len(agent.VisionClients), agent.EffectiveVisionModel)
	}
}
