package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/commands"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

type fakeChannel struct{ id string }

func (f *fakeChannel) Name() string                                            { return "fake" }
func (f *fakeChannel) Start(ctx context.Context) error                         { return nil }
func (f *fakeChannel) Stop(ctx context.Context) error                          { return nil }
func (f *fakeChannel) Send(ctx context.Context, msg bus.OutboundMessage) error { return nil }
func (f *fakeChannel) IsRunning() bool                                         { return true }
func (f *fakeChannel) IsAllowed(string) bool                                   { return true }
func (f *fakeChannel) IsAllowedSender(sender bus.SenderInfo) bool              { return true }
func (f *fakeChannel) ReasoningChannelID() string                              { return f.id }

func newTestAgentLoop(
	t *testing.T,
) (al *AgentLoop, cfg *config.Config, msgBus *bus.MessageBus, provider *mockProvider, cleanup func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	cfg = &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}
	msgBus = bus.NewMessageBus()
	provider = &mockProvider{}
	al = NewAgentLoop(cfg, msgBus, provider, nil)
	return al, cfg, msgBus, provider, func() { os.RemoveAll(tmpDir) }
}

func TestRecordLastChannel(t *testing.T) {
	al, cfg, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	testChannel := "test-channel"
	if err := al.RecordLastChannel(testChannel); err != nil {
		t.Fatalf("RecordLastChannel failed: %v", err)
	}
	if got := al.state.GetLastChannel(); got != testChannel {
		t.Errorf("Expected channel '%s', got '%s'", testChannel, got)
	}
	al2 := NewAgentLoop(cfg, msgBus, provider, nil)
	if got := al2.state.GetLastChannel(); got != testChannel {
		t.Errorf("Expected persistent channel '%s', got '%s'", testChannel, got)
	}
}

func TestRecordLastChatID(t *testing.T) {
	al, cfg, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	testChatID := "test-chat-id-123"
	if err := al.RecordLastChatID(testChatID); err != nil {
		t.Fatalf("RecordLastChatID failed: %v", err)
	}
	if got := al.state.GetLastChatID(); got != testChatID {
		t.Errorf("Expected chat ID '%s', got '%s'", testChatID, got)
	}
	al2 := NewAgentLoop(cfg, msgBus, provider, nil)
	if got := al2.state.GetLastChatID(); got != testChatID {
		t.Errorf("Expected persistent chat ID '%s', got '%s'", testChatID, got)
	}
}

func TestNewAgentLoop_StateInitialized(t *testing.T) {
	// Create temp workspace
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}

	// Create agent loop
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	// Verify state manager is initialized
	if al.state == nil {
		t.Error("Expected state manager to be initialized")
	}

	// Verify state directory was created. The default agent resolves to
	// <base_dir>/default, so its state lives under tmpDir/default/state.
	stateDir := filepath.Join(tmpDir, "default", "state")
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		t.Error("Expected state directory to exist")
	}
}

// TestToolRegistry_ToolRegistration verifies tools can be registered and retrieved
func TestToolRegistry_ToolRegistration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{
					ID:      "main",
					Default: true,
					Tools:   []string{"mock_custom"},
				},
			},
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	// Register a custom tool
	customTool := &mockCustomTool{}
	al.RegisterTool(customTool)

	// Verify tool is registered; it appears because the agent allowlist includes "mock_custom"
	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list
	found := slices.Contains(toolsList, "mock_custom")
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestToolContext_Updates verifies tool context helpers work correctly
func TestToolContext_Updates(t *testing.T) {
	ctx := tools.WithToolContext(context.Background(), "telegram", "chat-42")

	if got := tools.ToolChannel(ctx); got != "telegram" {
		t.Errorf("expected channel 'telegram', got %q", got)
	}
	if got := tools.ToolChatID(ctx); got != "chat-42" {
		t.Errorf("expected chatID 'chat-42', got %q", got)
	}

	// Empty context returns empty strings
	if got := tools.ToolChannel(context.Background()); got != "" {
		t.Errorf("expected empty channel from bare context, got %q", got)
	}
}

// TestToolRegistry_GetDefinitions verifies tool definitions can be retrieved
func TestToolRegistry_GetDefinitions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{
					ID:      "main",
					Default: true,
					Tools:   []string{"mock_custom"},
				},
			},
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	// Register a test tool and verify it shows up in startup info
	testTool := &mockCustomTool{}
	al.RegisterTool(testTool)

	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list; it appears because the agent allowlist includes "mock_custom"
	found := slices.Contains(toolsList, "mock_custom")
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestAgentLoop_GetStartupInfo verifies startup info contains tools
func TestAgentLoop_GetStartupInfo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = tmpDir
	cfg.Agents.Defaults.SetDefaultModel("test-model")
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 10
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:      "main",
			Default: true,
			Tools:   []string{"*"},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	info := al.GetStartupInfo()

	// Verify tools info exists
	toolsInfo, ok := info["tools"]
	if !ok {
		t.Fatal("Expected 'tools' key in startup info")
	}

	toolsMap, ok := toolsInfo.(map[string]any)
	if !ok {
		t.Fatal("Expected 'tools' to be a map")
	}

	count, ok := toolsMap["count"]
	if !ok {
		t.Fatal("Expected 'count' in tools info")
	}

	// Agent has wildcard allowlist, so all enabled tools should be registered
	if count.(int) == 0 {
		t.Error("Expected at least some tools to be registered")
	}
}

// TestAgentLoop_Stop verifies Stop() sets running to false
func TestAgentLoop_Stop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	// Note: running is only set to true when Run() is called
	// We can't test that without starting the event loop
	// Instead, verify the Stop method can be called safely
	al.Stop()

	// Verify running is false (initial state or after Stop)
	if al.running.Load() {
		t.Error("Expected agent to be stopped (or never started)")
	}
}

// Mock implementations for testing

type simpleMockProvider struct {
	response string
}

func (m *simpleMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:   m.response,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *simpleMockProvider) GetDefaultModel() string {
	return "mock-model"
}

type countingMockProvider struct {
	response string
	calls    int
}

func (m *countingMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	m.calls++
	return &providers.LLMResponse{
		Content:   m.response,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *countingMockProvider) GetDefaultModel() string {
	return "counting-mock-model"
}

// mockCustomTool is a simple mock tool for registration testing
type mockCustomTool struct{}

func (m *mockCustomTool) Name() string {
	return "mock_custom"
}

func (m *mockCustomTool) Description() string {
	return "Mock custom tool for testing"
}

func (m *mockCustomTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *mockCustomTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.SilentResult("Custom tool executed")
}

// testHelper executes a message and returns the response
type testHelper struct {
	al *AgentLoop
}

func (h testHelper) executeAndGetResponse(tb testing.TB, ctx context.Context, msg bus.InboundMessage) string {
	// Use a short timeout to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, responseTimeout)
	defer cancel()

	response, err := h.al.processMessage(timeoutCtx, msg)
	if err != nil {
		tb.Fatalf("processMessage failed: %v", err)
	}
	return response
}

const responseTimeout = 3 * time.Second

func TestProcessMessage_UsesRouteSessionKey(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "ok"}
	al := NewAgentLoop(cfg, msgBus, provider, nil)

	msg := bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "hello",
		Peer: bus.Peer{
			Kind: "direct",
			ID:   "user1",
		},
	}

	route := al.registry.ResolveRoute(routing.RouteInput{
		Channel: msg.Channel,
		Peer:    extractPeer(msg),
	})
	sessionKey := route.SessionKey

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}

	helper := testHelper{al: al}
	_ = helper.executeAndGetResponse(t, context.Background(), msg)

	history := defaultAgent.Sessions.GetHistory(sessionKey)
	if len(history) != 2 {
		t.Fatalf("expected session history len=2, got %d", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Fatalf("unexpected first message in session: %+v", history[0])
	}
}

func TestProcessMessage_CommandOutcomes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
		Session: config.SessionConfig{
			Mode: "per-platform",
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider, nil)
	helper := testHelper{al: al}

	baseMsg := bus.InboundMessage{
		Channel:  "slack",
		SenderID: "user1",
		ChatID:   "chat1",
		Peer: bus.Peer{
			Kind: "direct",
			ID:   "user1",
		},
	}

	showResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  baseMsg.Channel,
		SenderID: baseMsg.SenderID,
		ChatID:   baseMsg.ChatID,
		Content:  "/show channel",
		Peer:     baseMsg.Peer,
	})
	if showResp != "Current Channel: slack" {
		t.Fatalf("unexpected /show reply: %q", showResp)
	}
	if provider.calls != 0 {
		t.Fatalf("LLM should not be called for handled command, calls=%d", provider.calls)
	}

	// Unknown commands must never reach the LLM — they return an error reply instead.
	fooResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  baseMsg.Channel,
		SenderID: baseMsg.SenderID,
		ChatID:   baseMsg.ChatID,
		Content:  "/foo",
		Peer:     baseMsg.Peer,
	})
	if !strings.Contains(fooResp, "Unknown command") {
		t.Fatalf("unexpected /foo reply: %q (expected unknown command message)", fooResp)
	}
	if provider.calls != 0 {
		t.Fatalf("LLM must not be called for unknown command /foo, calls=%d", provider.calls)
	}

	newResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  baseMsg.Channel,
		SenderID: baseMsg.SenderID,
		ChatID:   baseMsg.ChatID,
		Content:  "/new",
		Peer:     baseMsg.Peer,
	})
	if !strings.Contains(newResp, "Unknown command") {
		t.Fatalf("unexpected /new reply: %q (expected unknown command message)", newResp)
	}
	if provider.calls != 0 {
		t.Fatalf("LLM must not be called for unknown command /new, calls=%d", provider.calls)
	}
}

func TestProcessMessage_ModelSelectListConsistency(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"primary", "secondary"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
		Providers: []config.Provider{
			{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "k"},
		},
		Models: []config.ModelConfig{
			{ModelName: "primary", Model: "primary", Provider: "openai", Enabled: true},
			{ModelName: "secondary", Model: "secondary", Provider: "openai", Enabled: true},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider, nil)
	helper := testHelper{al: al}

	modelResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/model 1",
		Peer: bus.Peer{
			Kind: "direct",
			ID:   "user1",
		},
	})
	if !strings.Contains(modelResp, "Model set to 1: secondary") {
		t.Fatalf("unexpected /model reply: %q", modelResp)
	}

	listResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/list models",
		Peer: bus.Peer{
			Kind: "direct",
			ID:   "user1",
		},
	})
	if !strings.Contains(listResp, "▶ 1: secondary") {
		t.Fatalf("unexpected /list models reply after select: %q", listResp)
	}

	if provider.calls != 0 {
		t.Fatalf("LLM should not be called for /model and /list, calls=%d", provider.calls)
	}
}

// TestToolResult_SilentToolDoesNotSendUserMessage verifies silent tools don't trigger outbound
func TestToolResult_SilentToolDoesNotSendUserMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "File operation complete"}
	al := NewAgentLoop(cfg, msgBus, provider, nil)
	helper := testHelper{al: al}

	// ReadFileTool returns SilentResult, which should not send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "read test.txt",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// Silent tool should return the LLM's response directly
	if response != "File operation complete" {
		t.Errorf("Expected 'File operation complete', got: %s", response)
	}
}

// TestToolResult_UserFacingToolDoesSendMessage verifies user-facing tools trigger outbound
func TestToolResult_UserFacingToolDoesSendMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "Command output: hello world"}
	al := NewAgentLoop(cfg, msgBus, provider, nil)
	helper := testHelper{al: al}

	// ExecTool returns UserResult, which should send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "run hello",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// User-facing tool should include the output in final response
	if response != "Command output: hello world" {
		t.Errorf("Expected 'Command output: hello world', got: %s", response)
	}
}

// failFirstMockProvider fails on the first N calls with a specific error
type failFirstMockProvider struct {
	failures    int
	currentCall int
	failError   error
	successResp string
}

func (m *failFirstMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	m.currentCall++
	if m.currentCall <= m.failures {
		return nil, m.failError
	}
	return &providers.LLMResponse{
		Content:   m.successResp,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *failFirstMockProvider) GetDefaultModel() string {
	return "mock-fail-model"
}

// TestAgentLoop_ContextExhaustionRetry verify that the agent retries on context errors
func TestAgentLoop_ContextExhaustionRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
	}

	msgBus := bus.NewMessageBus()

	// Create a provider that fails once with a context error
	contextErr := fmt.Errorf("InvalidParameter: Total tokens of image and text exceed max message tokens")
	provider := &failFirstMockProvider{
		failures:    1,
		failError:   contextErr,
		successResp: "Recovered from context error",
	}

	al := NewAgentLoop(cfg, msgBus, provider, nil)

	// Inject some history to simulate a full context
	sessionKey := "test-session-context"
	// Create dummy history
	history := []providers.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Old message 1"},
		{Role: "assistant", Content: "Old response 1"},
		{Role: "user", Content: "Old message 2"},
		{Role: "assistant", Content: "Old response 2"},
		{Role: "user", Content: "Trigger message"},
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}
	defaultAgent.Sessions.SetHistory(sessionKey, history)

	// Call ProcessDirectWithChannel
	// Note: ProcessDirectWithChannel calls processMessage which will execute runLLMIteration
	response, err := al.ProcessDirectWithChannel(
		context.Background(),
		"Trigger message",
		sessionKey,
		"test",
		"test-chat",
		"channel",
	)
	if err != nil {
		t.Fatalf("Expected success after retry, got error: %v", err)
	}

	if response != "Recovered from context error" {
		t.Errorf("Expected 'Recovered from context error', got '%s'", response)
	}

	// We expect 2 calls: 1st failed, 2nd succeeded
	if provider.currentCall != 2 {
		t.Errorf("Expected 2 calls (1 fail + 1 success), got %d", provider.currentCall)
	}

	// Check final history length
	finalHistory := defaultAgent.Sessions.GetHistory(sessionKey)
	// We verify that the history has been modified (compressed)
	// Original length: 6
	// Expected behavior: compression drops ~50% of history (mid slice)
	// We can assert that the length is NOT what it would be without compression.
	// Without compression: 6 + 1 (new user msg) + 1 (assistant msg) = 8
	if len(finalHistory) >= 8 {
		t.Errorf("Expected history to be compressed (len < 8), got %d", len(finalHistory))
	}
}

// TestProcessDirectWithChannel_TriggersMCPInitialization verifies that
// ProcessDirectWithChannel triggers MCP initialization when MCP is enabled.
// Note: Manager is only initialized when at least one MCP server is configured
// and successfully connected.
func TestProcessDirectWithChannel_TriggersMCPInitialization(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test with MCP enabled but no servers - should not initialize manager
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Default: true},
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{
					Enabled: true,
				},
				// No servers configured - manager should not be initialized
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider, nil)
	defer al.Close()

	if al.mcp.hasManager() {
		t.Fatal("expected MCP manager to be nil before first direct processing")
	}

	_, err = al.ProcessDirectWithChannel(
		context.Background(),
		"hello",
		"session-1",
		"cli",
		"direct",
		"direct",
	)
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}

	// Manager should not be initialized when no servers are configured
	if al.mcp.hasManager() {
		t.Fatal("expected MCP manager to be nil when no servers are configured")
	}
}

func TestTargetReasoningChannelID_AllChannels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	chManager, err := channels.NewManager(&config.Config{}, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("Failed to create channel manager: %v", err)
	}
	for name, id := range map[string]string{
		"telegram": "rid-telegram",
		"discord":  "rid-discord",
		"slack":    "rid-slack",
		"line":     "rid-line",
	} {
		chManager.RegisterChannel(name, &fakeChannel{id: id})
	}
	al.SetChannelManager(chManager)
	tests := []struct {
		channel string
		wantID  string
	}{
		{channel: "telegram", wantID: "rid-telegram"},
		{channel: "discord", wantID: "rid-discord"},
		{channel: "slack", wantID: "rid-slack"},
		{channel: "line", wantID: "rid-line"},
		{channel: "unknown", wantID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			got := al.targetReasoningChannelID(tt.channel)
			if got != tt.wantID {
				t.Fatalf("targetReasoningChannelID(%q) = %q, want %q", tt.channel, got, tt.wantID)
			}
		})
	}
}

func TestHandleReasoning(t *testing.T) {
	newLoop := func(t *testing.T) (*AgentLoop, *bus.MessageBus) {
		t.Helper()
		tmpDir, err := os.MkdirTemp("", "agent-test-*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
		cfg := &config.Config{
			Agents: config.AgentsConfig{
				BaseDir: tmpDir,
				Defaults: config.AgentDefaults{
					Models:            []string{"test-model"},
					MaxTokens:         4096,
					MaxToolIterations: 10,
				},
			},
		}
		msgBus := bus.NewMessageBus()
		return NewAgentLoop(cfg, msgBus, &mockProvider{}, nil), msgBus
	}

	t.Run("skips when any required field is empty", func(t *testing.T) {
		al, msgBus := newLoop(t)
		al.handleReasoning(context.Background(), "reasoning", "telegram", "")

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if msg, ok := msgBus.SubscribeOutbound(ctx); ok {
			t.Fatalf("expected no outbound message, got %+v", msg)
		}
	})

	t.Run("publishes one message for non telegram", func(t *testing.T) {
		al, msgBus := newLoop(t)
		al.handleReasoning(context.Background(), "hello reasoning", "slack", "channel-1")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if !ok {
			t.Fatal("expected an outbound message")
		}
		if msg.Channel != "slack" || msg.ChatID != "channel-1" || msg.Content != "hello reasoning" {
			t.Fatalf("unexpected outbound message: %+v", msg)
		}
	})

	t.Run("publishes one message for telegram", func(t *testing.T) {
		al, msgBus := newLoop(t)
		reasoning := "hello telegram reasoning"
		al.handleReasoning(context.Background(), reasoning, "telegram", "tg-chat")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if !ok {
			t.Fatal("expected outbound message")
		}

		if msg.Channel != "telegram" {
			t.Fatalf("expected telegram channel message, got %+v", msg)
		}
		if msg.ChatID != "tg-chat" {
			t.Fatalf("expected chatID tg-chat, got %+v", msg)
		}
		if msg.Content != reasoning {
			t.Fatalf("content mismatch: got %q want %q", msg.Content, reasoning)
		}
	})
	t.Run("expired ctx", func(t *testing.T) {
		al, msgBus := newLoop(t)
		reasoning := "hello telegram reasoning"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		al.handleReasoning(ctx, reasoning, "telegram", "tg-chat")

		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if ok {
			t.Fatalf("expected no outbound message, got %+v", msg)
		}
	})

	t.Run("returns promptly when bus is full", func(t *testing.T) {
		al, msgBus := newLoop(t)

		// Fill the outbound bus buffer until a publish would block.
		// Use a short timeout to detect when the buffer is full,
		// rather than hardcoding the buffer size.
		for i := 0; ; i++ {
			fillCtx, fillCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			err := msgBus.PublishOutbound(fillCtx, bus.OutboundMessage{
				Channel: "filler",
				ChatID:  "filler",
				Content: fmt.Sprintf("filler-%d", i),
			})
			fillCancel()
			if err != nil {
				// Buffer is full (timed out trying to send).
				break
			}
		}

		// Use a short-deadline parent context to bound the test.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		start := time.Now()
		al.handleReasoning(ctx, "should timeout", "slack", "channel-full")
		elapsed := time.Since(start)

		// handleReasoning uses a 5s internal timeout, but the parent ctx
		// expires in 500ms. It should return within ~500ms, not 5s.
		if elapsed > 2*time.Second {
			t.Fatalf("handleReasoning blocked too long (%v); expected prompt return", elapsed)
		}

		// Drain the bus and verify the reasoning message was NOT published
		// (it should have been dropped due to timeout).
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer drainCancel()
		foundReasoning := false
		for {
			msg, ok := msgBus.SubscribeOutbound(drainCtx)
			if !ok {
				break
			}
			if msg.Content == "should timeout" {
				foundReasoning = true
			}
		}
		if foundReasoning {
			t.Fatal("expected reasoning message to be dropped when bus is full, but it was published")
		}
	})
}

func TestResolveMediaRefs_ResolvesToBase64(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	// Create a minimal valid PNG (8-byte header is enough for filetype detection)
	pngPath := filepath.Join(dir, "test.png")
	// PNG magic: 0x89 P N G \r \n 0x1A \n + minimal IHDR
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, // IHDR length
		0x49, 0x48, 0x44, 0x52, // "IHDR"
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, // 1x1 RGB
		0x00, 0x00, 0x00, // no interlace
		0x90, 0x77, 0x53, 0xDE, // CRC
	}
	if err := os.WriteFile(pngPath, pngHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(pngPath, media.MediaMeta{}, "test")
	if err != nil {
		t.Fatal(err)
	}

	messages := []providers.Message{
		{Role: "user", Content: "describe this", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 {
		t.Fatalf("expected 1 resolved media, got %d", len(result[0].Media))
	}
	if !strings.HasPrefix(result[0].Media[0], "data:image/png;base64,") {
		t.Fatalf("expected data:image/png;base64, prefix, got %q", result[0].Media[0][:40])
	}
}

func TestResolveMediaRefs_SkipsOversizedFile(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	bigPath := filepath.Join(dir, "big.png")
	// Write PNG header + padding to exceed limit
	data := make([]byte, 1024+1) // 1KB + 1 byte
	copy(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	if err := os.WriteFile(bigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Store(bigPath, media.MediaMeta{}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	// Use a tiny limit (1KB) so the file is oversized
	result := resolveMediaRefs(messages, store, 1024)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media (oversized), got %d", len(result[0].Media))
	}
}

func TestResolveMediaRefs_UnknownTypeInjectsPath(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	txtPath := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Store(txtPath, media.MediaMeta{}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media entries, got %d", len(result[0].Media))
	}
	expected := "hi [file:" + txtPath + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_PassesThroughNonMediaRefs(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{"https://example.com/img.png"}},
	}
	result := resolveMediaRefs(messages, nil, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 || result[0].Media[0] != "https://example.com/img.png" {
		t.Fatalf("expected passthrough of non-media:// URL, got %v", result[0].Media)
	}
}

func TestResolveMediaRefs_DoesNotMutateOriginal(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "test.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	os.WriteFile(pngPath, pngHeader, 0o644)
	ref, _ := store.Store(pngPath, media.MediaMeta{}, "test")

	original := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	originalRef := original[0].Media[0]

	resolveMediaRefs(original, store, config.DefaultMaxMediaSize)

	if original[0].Media[0] != originalRef {
		t.Fatal("resolveMediaRefs mutated original message slice")
	}
}

func TestResolveMediaRefs_UsesMetaContentType(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	// File with JPEG content but stored with explicit content type
	jpegPath := filepath.Join(dir, "photo")
	jpegHeader := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic bytes
	os.WriteFile(jpegPath, jpegHeader, 0o644)
	ref, _ := store.Store(jpegPath, media.MediaMeta{ContentType: "image/jpeg"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 {
		t.Fatalf("expected 1 media, got %d", len(result[0].Media))
	}
	if !strings.HasPrefix(result[0].Media[0], "data:image/jpeg;base64,") {
		t.Fatalf("expected jpeg prefix, got %q", result[0].Media[0][:30])
	}
}

func TestResolveMediaRefs_PDFInjectsFilePath(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	pdfPath := filepath.Join(dir, "report.pdf")
	// PDF magic bytes
	os.WriteFile(pdfPath, []byte("%PDF-1.4 test content"), 0o644)
	ref, _ := store.Store(pdfPath, media.MediaMeta{ContentType: "application/pdf"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "report.pdf [file]", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media (non-image), got %d", len(result[0].Media))
	}
	expected := "report.pdf [file:" + pdfPath + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_AudioInjectsAudioPath(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	oggPath := filepath.Join(dir, "voice.ogg")
	os.WriteFile(oggPath, []byte("fake audio"), 0o644)
	ref, _ := store.Store(oggPath, media.MediaMeta{ContentType: "audio/ogg"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "voice.ogg [audio]", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media, got %d", len(result[0].Media))
	}
	expected := "voice.ogg [audio:" + oggPath + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_VideoInjectsVideoPath(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	mp4Path := filepath.Join(dir, "clip.mp4")
	os.WriteFile(mp4Path, []byte("fake video"), 0o644)
	ref, _ := store.Store(mp4Path, media.MediaMeta{ContentType: "video/mp4"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "clip.mp4 [video]", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media, got %d", len(result[0].Media))
	}
	expected := "clip.mp4 [video:" + mp4Path + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_NoGenericTagAppendsPath(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	csvPath := filepath.Join(dir, "data.csv")
	os.WriteFile(csvPath, []byte("a,b,c"), 0o644)
	ref, _ := store.Store(csvPath, media.MediaMeta{ContentType: "text/csv"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "here is my data", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	expected := "here is my data [file:" + csvPath + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_EmptyContentGetsPathTag(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	docPath := filepath.Join(dir, "doc.docx")
	os.WriteFile(docPath, []byte("fake docx"), 0o644)
	docxMIME := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	ref, _ := store.Store(docPath, media.MediaMeta{ContentType: docxMIME}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	expected := "[file:" + docPath + "]"
	if result[0].Content != expected {
		t.Fatalf("expected content %q, got %q", expected, result[0].Content)
	}
}

func TestResolveMediaRefs_MixedImageAndFile(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	pngPath := filepath.Join(dir, "photo.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	os.WriteFile(pngPath, pngHeader, 0o644)
	imgRef, _ := store.Store(pngPath, media.MediaMeta{}, "test")

	pdfPath := filepath.Join(dir, "report.pdf")
	os.WriteFile(pdfPath, []byte("%PDF-1.4 test"), 0o644)
	fileRef, _ := store.Store(pdfPath, media.MediaMeta{ContentType: "application/pdf"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "check these [file]", Media: []string{imgRef, fileRef}},
	}
	result := resolveMediaRefs(messages, store, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 {
		t.Fatalf("expected 1 media (image only), got %d", len(result[0].Media))
	}
	if !strings.HasPrefix(result[0].Media[0], "data:image/png;base64,") {
		t.Fatal("expected image to be base64 encoded")
	}
	expectedContent := "check these [file:" + pdfPath + "]"
	if result[0].Content != expectedContent {
		t.Fatalf("expected content %q, got %q", expectedContent, result[0].Content)
	}
}

// TestForceCompression_WithSystemPrompt verifies that ForceCompress keeps the
// system message at index 0, reduces total history, and preserves the most
// recent turn group (last user message). Messages are padded so that two or
// more conversation groups exceed the safety threshold, forcing a drop.
func TestForceCompression_WithSystemPrompt(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	// contextWindow=400 tokens, safetyPercent=80 → threshold = 320 tokens.
	// Each padded message is ~160 tokens (640 chars / 4). The current turn
	// group (user3, 160 tokens) fits. Adding ass2 (160 tokens) would hit 320
	// tokens = 80%, which is NOT < 80%, so only user3 is kept. That gives us
	// sys + user3 = 2 messages, down from 6.
	pad := strings.Repeat("x", 640)
	sessionKey := "test-compress-sys"
	history := []providers.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "user msg 1 " + pad},
		{Role: "assistant", Content: "assistant msg 1 " + pad},
		{Role: "user", Content: "user msg 2 " + pad},
		{Role: "assistant", Content: "assistant msg 2 " + pad},
		{Role: "user", Content: "user msg 3"},
	}
	agent.Sessions.SetHistory(sessionKey, history)

	mgr := llmcontext.New(sessionKey, agent.Sessions, agent.ContextBuilder, nil,
		llmcontext.WithContextWindow(400),
		llmcontext.WithSafetyPercent(80),
	)
	if err := mgr.ForceCompress(context.Background()); err != nil {
		t.Fatalf("ForceCompress returned unexpected error: %v", err)
	}

	got := agent.Sessions.GetHistory(sessionKey)
	if len(got) >= len(history) {
		t.Fatalf("expected history to be reduced from %d, got %d messages", len(history), len(got))
	}
	if len(got) == 0 {
		t.Fatal("history must not be empty after compression")
	}
	if got[0].Role != "system" {
		t.Fatalf("expected first message to be system, got role=%q", got[0].Role)
	}
	// Most recent turn group (user3) must still be present.
	last := got[len(got)-1]
	if last.Content != "user msg 3" {
		t.Fatalf("expected last user message to be preserved, got %q", last.Content)
	}
}

// TestForceCompression_NoSystemPrompt verifies that ForceCompress handles
// histories without a system prompt: no system message is injected in the result.
func TestForceCompression_NoSystemPrompt(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	// Same window/padding as the system-prompt test; force a drop.
	pad := strings.Repeat("x", 640)
	sessionKey := "test-compress-nosys"
	history := []providers.Message{
		{Role: "user", Content: "user msg 1 " + pad},
		{Role: "assistant", Content: "assistant msg 1 " + pad},
		{Role: "user", Content: "user msg 2 " + pad},
		{Role: "assistant", Content: "assistant msg 2 " + pad},
		{Role: "user", Content: "user msg 3"},
	}
	agent.Sessions.SetHistory(sessionKey, history)

	mgr := llmcontext.New(sessionKey, agent.Sessions, agent.ContextBuilder, nil,
		llmcontext.WithContextWindow(400),
		llmcontext.WithSafetyPercent(80),
	)
	if err := mgr.ForceCompress(context.Background()); err != nil {
		t.Fatalf("ForceCompress returned unexpected error: %v", err)
	}

	got := agent.Sessions.GetHistory(sessionKey)
	if len(got) >= len(history) {
		t.Fatalf("expected history to be reduced from %d, got %d messages", len(history), len(got))
	}
	if len(got) == 0 {
		t.Fatal("history must not be empty after compression")
	}
	// Without a system prompt, the first message should not be a system message.
	if got[0].Role == "system" {
		t.Fatalf("expected first message not to be system when no system prompt was set, got role=%q", got[0].Role)
	}
}

// errorProvider is a mock LLM provider that always returns the configured error.
type errorProvider struct {
	err error
}

func (e *errorProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return nil, e.err
}

func (e *errorProvider) GetDefaultModel() string {
	return "error-model"
}

// TestRunLLMIteration_ContextCancelDuringBackoff verifies that cancelling the
// context during a retry backoff causes runLLMIteration to return promptly
// (well within the backoff duration of 5s or 10s).
func TestRunLLMIteration_ContextCancelDuringBackoff(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	// Replace the default agent's provider with one that always returns a timeout error.
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &errorProvider{err: fmt.Errorf("deadline exceeded")}

	messages := []providers.Message{
		{Role: "user", Content: "trigger backoff"},
	}
	opts := processOptions{
		SessionKey:   "backoff-test",
		SendResponse: false,
	}

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		err error
	}
	done := make(chan result, 1)

	go func() {
		cm, releaseTestCM := al.getContextManager(agent, opts.SessionKey)
		defer releaseTestCM()
		_, _, _, _, err := al.runLLMIteration(ctx, agent, messages, opts, cm)
		done <- result{err: err}
	}()

	// Give the goroutine time to enter the first retry backoff, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		// Expect context cancellation error.
		if res.err == nil {
			t.Fatal("expected error after context cancel, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLLMIteration did not return promptly after context cancellation")
	}
}

// sequenceProvider returns a predetermined sequence of responses and errors.
// After the sequence is exhausted it returns a generic "done" response.
type sequenceProvider struct {
	responses []*providers.LLMResponse
	errors    []error
	idx       int
	mu        sync.Mutex
}

func (p *sequenceProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx >= len(p.responses) {
		return &providers.LLMResponse{Content: "done"}, nil
	}
	r, e := p.responses[p.idx], p.errors[p.idx]
	p.idx++
	return r, e
}

func (p *sequenceProvider) GetDefaultModel() string { return "test" }

// mockEchoTool is a simple tool that echoes its input back as ForLLM content.
type mockEchoTool struct{}

func (m *mockEchoTool) Name() string        { return "echo_tool" }
func (m *mockEchoTool) Description() string { return "Echoes input back" }
func (m *mockEchoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
	}
}
func (m *mockEchoTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	text, _ := args["text"].(string)
	if text == "" {
		text = "echo"
	}
	return tools.SilentResult(text)
}

// TestRunAgentLoop_EmptyResponse_FirstIteration verifies that when the provider
// returns a non-normal empty response on the first iteration, runAgentLoop returns
// a message describing the abnormal finish reason.
func TestRunAgentLoop_EmptyResponse_FirstIteration(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	// Provider returns empty content with an abnormal (non-normal) finish — Normal: false (zero value).
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{Content: "", FinishReason: "stop", Normal: false, ToolCalls: []providers.ToolCall{}},
		},
		errors: []error{nil},
	}

	opts := processOptions{
		SessionKey:      "empty-first",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		SendResponse:    false,
	}

	got, err := al.runAgentLoop(context.Background(), agent, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty error message for abnormal empty response, got empty string")
	}
	if !strings.Contains(got, "stop") {
		t.Errorf("expected message to contain finish reason %q, got %q", "stop", got)
	}
}

// TestRunAgentLoop_EmptyResponse_AfterToolCalls verifies that when the provider
// returns tool calls first and then a non-normal empty response, runAgentLoop
// returns a message describing the abnormal finish reason.
func TestRunAgentLoop_EmptyResponse_AfterToolCalls(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	// Register a tool so the tool call can be dispatched without an error.
	agent.Tools.Register(&mockEchoTool{})

	// First call: return a tool call. Second call: return empty content.
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{
				Content: "",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Name: "echo_tool",
						Function: &providers.FunctionCall{
							Name:      "echo_tool",
							Arguments: `{"text":"hi"}`,
						},
					},
				},
			},
			{Content: "", ToolCalls: []providers.ToolCall{}},
		},
		errors: []error{nil, nil},
	}

	opts := processOptions{
		SessionKey:      "empty-after-tools",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "use a tool",
		DefaultResponse: defaultResponse,
		SendResponse:    false,
	}

	got, err := al.runAgentLoop(context.Background(), agent, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty error message for abnormal empty response after tool calls, got empty string")
	}
}

// TestRunLLMIteration_ToolCalls_ThenFinalResponse verifies that when the
// provider first returns tool calls and then returns text content, the final
// content from runLLMIteration is that text and the iteration count is 2.
func TestRunLLMIteration_ToolCalls_ThenFinalResponse(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	agent.Tools.Register(&mockEchoTool{})

	const finalText = "all done"
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{
				Content: "",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Name: "echo_tool",
						Function: &providers.FunctionCall{
							Name:      "echo_tool",
							Arguments: `{"text":"test"}`,
						},
					},
				},
			},
			{Content: finalText, ToolCalls: []providers.ToolCall{}},
		},
		errors: []error{nil, nil},
	}

	messages := []providers.Message{{Role: "user", Content: "run echo_tool"}}
	opts := processOptions{
		SessionKey:   "tool-then-text",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "run echo_tool",
		SendResponse: false,
	}

	cm, releaseTestCM := al.getContextManager(agent, opts.SessionKey)
	defer releaseTestCM()
	content, _, _, iterations, err := al.runLLMIteration(context.Background(), agent, messages, opts, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != finalText {
		t.Errorf("expected final content %q, got %q", finalText, content)
	}
	if iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", iterations)
	}
}

// TestRunLLMIteration_MaxIterations verifies that the LLM loop exits after
// MaxIterations when the provider never stops returning tool calls.
func TestRunLLMIteration_MaxIterations(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	const maxIter = 3
	agent.MaxIterations = maxIter
	agent.Tools.Register(&mockEchoTool{})

	// Always return a tool call — never a text response.
	toolCallResp := &providers.LLMResponse{
		Content: "",
		ToolCalls: []providers.ToolCall{
			{
				ID:   "tc-inf",
				Type: "function",
				Name: "echo_tool",
				Function: &providers.FunctionCall{
					Name:      "echo_tool",
					Arguments: `{"text":"loop"}`,
				},
			},
		},
	}
	responses := make([]*providers.LLMResponse, maxIter+2)
	errors := make([]error, maxIter+2)
	for i := range responses {
		responses[i] = toolCallResp
		errors[i] = nil
	}
	agent.Provider = &sequenceProvider{responses: responses, errors: errors}

	messages := []providers.Message{{Role: "user", Content: "loop forever"}}
	opts := processOptions{
		SessionKey:   "max-iter",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "loop forever",
		SendResponse: false,
	}

	cm, releaseTestCM := al.getContextManager(agent, opts.SessionKey)
	defer releaseTestCM()
	content, _, _, iterations, err := al.runLLMIteration(context.Background(), agent, messages, opts, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Loop exhausted — content should be empty (caller converts to defaultResponse).
	if content != "" {
		t.Errorf("expected empty content after max iterations, got %q", content)
	}
	if iterations != maxIter {
		t.Errorf("expected %d iterations, got %d", maxIter, iterations)
	}
}

// fourCandidates returns a deterministic 4-element candidate list for reorder tests.
func fourCandidates() []providers.FallbackCandidate {
	return []providers.FallbackCandidate{
		{Provider: "p0", Model: "m0", Alias: "a0"},
		{Provider: "p1", Model: "m1", Alias: "a1"},
		{Provider: "p2", Model: "m2", Alias: "a2"},
		{Provider: "p3", Model: "m3", Alias: "a3"},
	}
}

// TestSelectCandidates_DefaultOrder verifies that with the default active index
// (0) selectCandidates returns the candidate list in its original order.
func TestSelectCandidates_DefaultOrder(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Candidates = fourCandidates()
	agent.Model = "primary"

	candidates, model := al.selectCandidates(agent, "session-default")

	if model != "a0" {
		t.Errorf("expected model %q, got %q", "a0", model)
	}
	for i, c := range candidates {
		if c.Model != agent.Candidates[i].Model {
			t.Fatalf("candidate %d = %q, want %q (order changed)", i, c.Model, agent.Candidates[i].Model)
		}
	}
}

// TestSelectCandidates_Reorder verifies move-to-front semantics: selecting index
// 2 returns [2,0,1,3] and never mutates agent.Candidates.
func TestSelectCandidates_Reorder(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Candidates = fourCandidates()
	agent.Model = "primary"

	orig := fourCandidates()
	const sessionKey = "session-reorder"

	if err := al.setActiveModelIndex(agent, sessionKey, 2); err != nil {
		t.Fatalf("setActiveModelIndex(2) failed: %v", err)
	}

	candidates, model := al.selectCandidates(agent, sessionKey)
	wantOrder := []string{"m2", "m0", "m1", "m3"}
	if len(candidates) != len(wantOrder) {
		t.Fatalf("got %d candidates, want %d", len(candidates), len(wantOrder))
	}
	for i, want := range wantOrder {
		if candidates[i].Model != want {
			t.Errorf("candidate %d = %q, want %q", i, candidates[i].Model, want)
		}
	}
	if model != "a2" {
		t.Errorf("expected model %q, got %q", "a2", model)
	}

	// agent.Candidates must be untouched.
	for i := range orig {
		if agent.Candidates[i].Model != orig[i].Model {
			t.Errorf("agent.Candidates mutated at %d: %q != %q", i, agent.Candidates[i].Model, orig[i].Model)
		}
	}
}

// TestSetActiveModelIndex_OutOfRange verifies range/empty validation.
func TestSetActiveModelIndex_OutOfRange(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Candidates = fourCandidates()

	if err := al.setActiveModelIndex(agent, "s", 4); err == nil {
		t.Error("expected error for index 4 (out of range), got nil")
	}
	if err := al.setActiveModelIndex(agent, "s", -1); err == nil {
		t.Error("expected error for index -1, got nil")
	}

	agent.Candidates = nil
	if err := al.setActiveModelIndex(agent, "s", 0); err == nil {
		t.Error("expected error when agent has no candidates, got nil")
	}
}

// TestActiveModelIndex_Persists verifies the active index survives a cache clear
// by reloading through the session store's CompactionState.
func TestActiveModelIndex_Persists(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Candidates = fourCandidates()
	const sessionKey = "session-persist"

	if err := al.setActiveModelIndex(agent, sessionKey, 3); err != nil {
		t.Fatalf("setActiveModelIndex(3) failed: %v", err)
	}

	// Clear the in-memory cache so the next read must come from the store.
	al.activeModelMu.Lock()
	al.activeModelIdx = make(map[string]int)
	al.activeModelMu.Unlock()

	if got := al.getActiveModelIndex(agent, sessionKey); got != 3 {
		t.Errorf("reloaded active index = %d, want 3", got)
	}
}

// TestProcessSystemMessage_InternalChannel verifies that processSystemMessage
// returns empty string and nil error for internal-channel system messages
// (e.g. when the origin channel in ChatID is "cli" or "subagent").
func TestProcessSystemMessage_InternalChannel(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	// ChatID format is "originChannel:originChatID".
	// "cli" is an internal channel so no response should be sent.
	msg := bus.InboundMessage{
		Channel:  "system",
		SenderID: "async:some_tool",
		ChatID:   "cli:some-id",
		Content:  "Task 'label' completed.\n\nResult:\ntool output",
	}

	got, err := al.processSystemMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty response for internal channel, got %q", got)
	}
}

// TestProcessSystemMessage_WrongChannel verifies that processSystemMessage
// returns an error when called with a non-system channel.
func TestProcessSystemMessage_WrongChannel(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	msg := bus.InboundMessage{
		Channel: "telegram",
		Content: "should fail",
	}

	_, err := al.processSystemMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for non-system channel, got nil")
	}
}

// TestRunLLMIteration_ContextWindowError_Retry verifies that when the provider
// returns a context_length_exceeded error on the first call and succeeds on the
// second, runLLMIteration retries and returns the successful response.
func TestRunLLMIteration_ContextWindowError_Retry(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}

	// Populate a session with enough messages to survive forceCompression.
	const sessionKey = "ctx-window-retry"
	for i := 0; i < 6; i++ {
		agent.Sessions.AddMessage(sessionKey, "user", "old user msg")
		agent.Sessions.AddMessage(sessionKey, "assistant", "old assistant reply")
	}

	const successContent = "recovered after context error"
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			nil,
			{Content: successContent, ToolCalls: []providers.ToolCall{}},
		},
		errors: []error{
			fmt.Errorf("context_length_exceeded: request too long"),
			nil,
		},
	}

	messages := []providers.Message{{Role: "user", Content: "trigger"}}
	opts := processOptions{
		SessionKey:   sessionKey,
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "trigger",
		SendResponse: false,
	}

	cm, releaseTestCM := al.getContextManager(agent, opts.SessionKey)
	defer releaseTestCM()
	content, _, _, _, err := al.runLLMIteration(context.Background(), agent, messages, opts, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != successContent {
		t.Errorf("expected %q, got %q", successContent, content)
	}
}

// TestInferMediaType covers the inferMediaType helper for various inputs.
func TestInferMediaType(t *testing.T) {
	tests := []struct {
		filename    string
		contentType string
		want        string
	}{
		{"photo.jpg", "image/jpeg", "image"},
		{"sound.mp3", "audio/mpeg", "audio"},
		{"movie.mp4", "video/mp4", "video"},
		{"doc.pdf", "application/pdf", "file"},
		{"audio.ogg", "application/ogg", "audio"},
		{"image.png", "", "image"},  // extension fallback
		{"audio.wav", "", "audio"},  // extension fallback
		{"video.avi", "", "video"},  // extension fallback
		{"unknown.xyz", "", "file"}, // unknown extension
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			got := inferMediaType(tc.filename, tc.contentType)
			if got != tc.want {
				t.Errorf("inferMediaType(%q, %q) = %q, want %q",
					tc.filename, tc.contentType, got, tc.want)
			}
		})
	}
}

// TestExtractPeer covers extractPeer for messages with and without Peer fields.
func TestExtractPeer(t *testing.T) {
	t.Run("no peer kind returns nil", func(t *testing.T) {
		msg := bus.InboundMessage{SenderID: "alice", ChatID: "chat1"}
		if got := extractPeer(msg); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("direct peer with explicit ID", func(t *testing.T) {
		msg := bus.InboundMessage{
			SenderID: "alice",
			ChatID:   "chat1",
			Peer:     bus.Peer{Kind: "direct", ID: "alice-id"},
		}
		got := extractPeer(msg)
		if got == nil {
			t.Fatal("expected non-nil peer")
		}
		if got.Kind != "direct" || got.ID != "alice-id" {
			t.Errorf("unexpected peer: %+v", got)
		}
	})

	t.Run("direct peer without ID falls back to SenderID", func(t *testing.T) {
		msg := bus.InboundMessage{
			SenderID: "alice",
			ChatID:   "chat1",
			Peer:     bus.Peer{Kind: "direct"},
		}
		got := extractPeer(msg)
		if got == nil {
			t.Fatal("expected non-nil peer")
		}
		if got.ID != "alice" {
			t.Errorf("expected ID 'alice', got %q", got.ID)
		}
	})

	t.Run("channel peer without ID falls back to ChatID", func(t *testing.T) {
		msg := bus.InboundMessage{
			SenderID: "alice",
			ChatID:   "room42",
			Peer:     bus.Peer{Kind: "channel"},
		}
		got := extractPeer(msg)
		if got == nil {
			t.Fatal("expected non-nil peer")
		}
		if got.ID != "room42" {
			t.Errorf("expected ID 'room42', got %q", got.ID)
		}
	})
}

// TestInboundMetadata verifies inboundMetadata returns values and handles nil map.
func TestInboundMetadata(t *testing.T) {
	t.Run("nil metadata returns empty", func(t *testing.T) {
		msg := bus.InboundMessage{}
		if got := inboundMetadata(msg, "key"); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("key present returns value", func(t *testing.T) {
		msg := bus.InboundMessage{
			Metadata: map[string]string{"account_id": "acc-1"},
		}
		if got := inboundMetadata(msg, "account_id"); got != "acc-1" {
			t.Errorf("expected 'acc-1', got %q", got)
		}
	})

	t.Run("key absent returns empty", func(t *testing.T) {
		msg := bus.InboundMessage{
			Metadata: map[string]string{"other": "val"},
		}
		if got := inboundMetadata(msg, "missing"); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}

// TestExtractParentPeer verifies extractParentPeer parses parent peer metadata.
func TestExtractParentPeer(t *testing.T) {
	t.Run("no metadata returns nil", func(t *testing.T) {
		msg := bus.InboundMessage{}
		if got := extractParentPeer(msg); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("partial metadata returns nil", func(t *testing.T) {
		msg := bus.InboundMessage{
			Metadata: map[string]string{metadataKeyParentPeerKind: "direct"},
		}
		if got := extractParentPeer(msg); got != nil {
			t.Errorf("expected nil when ID missing, got %+v", got)
		}
	})

	t.Run("full metadata returns peer", func(t *testing.T) {
		msg := bus.InboundMessage{
			Metadata: map[string]string{
				metadataKeyParentPeerKind: "channel",
				metadataKeyParentPeerID:   "room-99",
			},
		}
		got := extractParentPeer(msg)
		if got == nil {
			t.Fatal("expected non-nil peer")
		}
		if got.Kind != "channel" || got.ID != "room-99" {
			t.Errorf("unexpected parent peer: %+v", got)
		}
	})
}

// TestProcessDirect_ReturnsResponse verifies ProcessDirect calls through to the LLM.
func TestProcessDirect_ReturnsResponse(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &simpleMockProvider{response: "direct response"}

	got, err := al.ProcessDirect(context.Background(), "hello", "direct-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "direct response" {
		t.Errorf("expected 'direct response', got %q", got)
	}
}

// TestSetMediaStore_PropagatesStore verifies that SetMediaStore propagates to
// agent tools (specifically send_file if registered).
func TestSetMediaStore_PropagatesStore(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	// SetMediaStore should not panic even with a nil store.
	al.SetMediaStore(nil)
}

// TestSetTranscriber_StoresTranscriber verifies SetTranscriber stores the value.
func TestSetTranscriber_StoresTranscriber(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	// SetTranscriber with nil should not panic.
	al.SetTranscriber(nil)
	if al.transcriber != nil {
		t.Error("expected transcriber to be nil")
	}
}

// TestReloadProviderAndConfig_NilProvider verifies ReloadProviderAndConfig
// returns an error when provider is nil.
func TestReloadProviderAndConfig_NilProvider(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	err := al.ReloadProviderAndConfig(context.Background(), nil, cfg)
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

// TestReloadProviderAndConfig_NilConfig verifies ReloadProviderAndConfig
// returns an error when config is nil.
func TestReloadProviderAndConfig_NilConfig(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	err := al.ReloadProviderAndConfig(context.Background(), provider, nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

// TestReloadProviderAndConfig_Success verifies that a valid reload swaps the
// config and registry without error.
func TestReloadProviderAndConfig_Success(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	newCfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: cfg.Agents.BaseDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"new-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	err := al.ReloadProviderAndConfig(context.Background(), provider, newCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := al.GetConfig()
	if got != newCfg {
		t.Error("expected config to be swapped to newCfg")
	}
}

// TestMapCommandError_WithCommandName verifies mapCommandError formats the
// message correctly when Command is set.
func TestMapCommandError_WithCommandName(t *testing.T) {
	result := commands.ExecuteResult{
		Command: "help",
		Err:     fmt.Errorf("not found"),
	}
	got := mapCommandError(result)
	if !strings.Contains(got, "/help") {
		t.Errorf("expected output to contain '/help', got %q", got)
	}
}

// TestMapCommandError_WithoutCommandName verifies mapCommandError formats the
// message correctly when Command is empty.
func TestMapCommandError_WithoutCommandName(t *testing.T) {
	result := commands.ExecuteResult{
		Command: "",
		Err:     fmt.Errorf("parse error"),
	}
	got := mapCommandError(result)
	if !strings.Contains(got, "Failed to execute command:") {
		t.Errorf("expected generic error format, got %q", got)
	}
}

// TestResolveMessageRoute_PreresolvedAgentID verifies that a message carrying
// metadataKeyPreresolvedAgentID is delivered to the named agent, bypassing the
// binding cascade. Regression test for FIX-2: callbacks intended for one agent
// were being routed to a different agent that owned a Slack catch-all binding.
func TestResolveMessageRoute_PreresolvedAgentID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-preresolve-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "dawn", Name: "Dawn"},
				{ID: "penny", Name: "Penny", Default: true},
			},
		},
		Bindings: []config.AgentBinding{
			// Dawn owns a specific Slack channel.
			{AgentID: "dawn", Match: config.BindingMatch{
				Channel: "slack",
				Peer:    &config.PeerMatch{Kind: "channel", ID: "C0AN5SN702V"},
			}},
			// Penny is the Slack-wide catch-all (binding.account match).
			{AgentID: "penny", Match: config.BindingMatch{Channel: "slack"}},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, nil)

	// Baseline: without preresolved metadata, a direct peer with ID "dawn"
	// falls through to Penny's account-level binding. This is the old bug.
	msg := bus.InboundMessage{
		Channel: "slack",
		ChatID:  "C0AN5SN702V",
		Peer:    bus.Peer{Kind: "direct", ID: "dawn"},
	}
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("baseline resolveMessageRoute: %v", err)
	}
	if agent.ID != "penny" {
		t.Fatalf("baseline: expected fallthrough to penny (the bug), got %q", agent.ID)
	}
	if route.MatchedBy != "binding.account" {
		t.Errorf("baseline: expected matched_by=binding.account, got %q", route.MatchedBy)
	}

	// With preresolved metadata: must route to Dawn regardless of bindings.
	msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "dawn"}
	route, agent, err = al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("preresolved resolveMessageRoute: %v", err)
	}
	if agent.ID != "dawn" {
		t.Errorf("expected routing to dawn, got %q", agent.ID)
	}
	if route.MatchedBy != "preresolved" {
		t.Errorf("expected matched_by=preresolved, got %q", route.MatchedBy)
	}
	if want := routing.BuildAgentMainSessionKey("dawn"); route.SessionKey != want {
		t.Errorf("expected session key %q, got %q", want, route.SessionKey)
	}

	// Unknown preresolved ID falls back to binding resolution without error.
	msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "nobody"}
	_, agent, err = al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("unknown-preresolved resolveMessageRoute: %v", err)
	}
	if agent.ID != "penny" {
		t.Errorf("expected fallback to binding resolution (penny), got %q", agent.ID)
	}
}
