// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/tools"
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

func (s *stubVisionClient) Complete(_ context.Context, msgs []providers.Message) (llmcontext.LLMReply, error) {
	s.calls++
	for _, m := range msgs {
		if len(m.Media) > 0 {
			s.gotMedia = m.Media
		}
	}
	if s.err != nil {
		return llmcontext.LLMReply{}, s.err
	}
	return llmcontext.LLMReply{Content: s.reply}, nil
}

func (s *stubVisionClient) Model() string { return s.model }

func TestDescribeImages_ReturnsDescription(t *testing.T) {
	al := &AgentLoop{cfg: &config.Config{}}
	stub := &stubVisionClient{reply: "a red square", model: "vmodel"}
	agent := &AgentInstance{ID: "a1", VisionClients: []llmcontext.LLMClient{stub}}

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
	agent := &AgentInstance{ID: "a1", VisionClients: []llmcontext.LLMClient{bad, good}}

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
	agent := &AgentInstance{ID: "a1", Model: "deepseek", VisionClients: []llmcontext.LLMClient{stub}}

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
	agent := &AgentInstance{ID: "a1", Model: "gpt5", VisionClients: []llmcontext.LLMClient{stub}}

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
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Tools.Register(flowAImageTool{})
	if agent.Config != nil {
		agent.Config.Tools = []string{"*"}
	}
	stub := &stubVisionClient{reply: "two cats", model: "vmodel"}
	agent.VisionClients = []llmcontext.LLMClient{stub}

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

	_, _, _, _, _, err := al.runLLMIteration(
		context.Background(), agent,
		[]providers.Message{{Role: "user", Content: "how many cats?"}}, opts, cm,
	)
	if err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	// The vision side-model must have received the tool's image.
	if len(stub.gotMedia) != 1 {
		t.Fatalf("vision model did not receive the tool image: %v", stub.gotMedia)
	}
	// The 2nd model dispatch must have seen the injected description.
	sp := agent.Provider.(*sequenceProvider)
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
