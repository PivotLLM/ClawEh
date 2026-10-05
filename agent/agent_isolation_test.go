package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// TestAgentIsolation_ResolveScopeKeyRejectsMismatchedAgentID tests that resolveScopeKey
// strictly rejects session keys belonging to another agent (e.g. Alice's session key
// passed into Bob's route).
func TestAgentIsolation_ResolveScopeKeyRejectsMismatchedAgentID(t *testing.T) {
	bobRoute := routing.ResolvedRoute{
		AgentID:    "bob",
		SessionKey: "agent:bob:main",
	}

	// 1. Foreign session key (Alice) must be rejected and fall back to Bob's route session key
	got := resolveScopeKey(bobRoute, "agent:alice:main")
	if got != "agent:bob:main" {
		t.Fatalf("resolveScopeKey allowed foreign agent key: got %q, want %q", got, "agent:bob:main")
	}

	// 2. Foreign subagent/custom session key must also be rejected
	got = resolveScopeKey(bobRoute, "agent:alice:subagent:123")
	if got != "agent:bob:main" {
		t.Fatalf("resolveScopeKey allowed foreign agent subagent key: got %q, want %q", got, "agent:bob:main")
	}

	// 3. Any other own key collapses to the main session: an agent has one
	// persistent conversation.
	for _, key := range []string{"agent:bob:topic-456", "agent:bob:telegram:direct:1", "agent:bob:device:dev1", "agent:bob:service"} {
		if got = resolveScopeKey(bobRoute, key); got != "agent:bob:main" {
			t.Fatalf("resolveScopeKey(%q) = %q, want agent:bob:main", key, got)
		}
	}

	// 3a. The agent's own sub-agent session is preserved.
	got = resolveScopeKey(bobRoute, "agent:bob:subagent:123")
	if got != "agent:bob:subagent:123" {
		t.Fatalf("resolveScopeKey dropped own sub-agent key: got %q", got)
	}

	// 4. Empty session key falls back to route
	got = resolveScopeKey(bobRoute, "")
	if got != "agent:bob:main" {
		t.Fatalf("resolveScopeKey failed on empty key: got %q, want %q", got, "agent:bob:main")
	}
}

// TestAgentIsolation_ResolveSystemMessageTargetValidation tests that resolveSystemMessageTarget
// validates that the session key belongs to the target agent, falling back to agent's main
// session key if a mismatch occurs.
func TestAgentIsolation_ResolveSystemMessageTargetValidation(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "alice", Name: "Alice", Default: true},
				{ID: "bob", Name: "Bob"},
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)

	// Message targeted to Bob via preresolved_agent_id, but with Alice's session key
	msg := bus.InboundMessage{
		Channel:    "system",
		SenderID:   "async:test",
		ChatID:     "cli:direct",
		Content:    "test content",
		SessionKey: "agent:alice:main",
		Metadata: map[string]string{
			metadataKeyPreresolvedAgentID: "bob",
		},
	}

	agent, sessionKey := al.resolveSystemMessageTarget(msg)
	if agent == nil || agent.ID != "bob" {
		t.Fatalf("expected agent Bob, got %v", agent)
	}
	if sessionKey != "agent:bob:main" {
		t.Fatalf("expected sessionKey agent:bob:main, got %q (cross-agent leak!)", sessionKey)
	}

	// Bob's own non-main key collapses to his main session; his own sub-agent
	// session key is kept as given.
	for key, want := range map[string]string{
		"agent:bob:telegram:direct:1": "agent:bob:main",
		"agent:bob:subagent:u1":       "agent:bob:subagent:u1",
	} {
		msg.SessionKey = key
		if _, got := al.resolveSystemMessageTarget(msg); got != want {
			t.Fatalf("resolveSystemMessageTarget(%q) session = %q, want %q", key, got, want)
		}
	}
}

// TestAgentIsolation_TaskPointerCallbackCarriesOwnerAgent tests that taskPointerCallback
// properly stamps the originating owner agent ID and session key when publishing completion.
func TestAgentIsolation_TaskPointerCallbackCarriesOwnerAgent(t *testing.T) {
	msgBus := bus.NewMessageBus()
	al := &AgentLoop{
		bus: msgBus,
	}

	cb := al.taskPointerCallback("slack", "C123", "bob")
	cb(context.Background(), &tools.ToolResult{
		ForLLM: "task finished",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("failed to consume inbound message from taskPointerCallback")
	}

	if msg.Metadata[metadataKeyPreresolvedAgentID] != "bob" {
		t.Fatalf("expected preresolved_agent_id bob, got %q", msg.Metadata[metadataKeyPreresolvedAgentID])
	}
	if msg.SessionKey != "agent:bob:main" {
		t.Fatalf("expected sessionKey agent:bob:main, got %q", msg.SessionKey)
	}
	if msg.ChatID != "slack:C123" {
		t.Fatalf("expected chatID slack:C123, got %q", msg.ChatID)
	}
}

// TestAgentIsolation_MentionRoutingSessionScoping tests that mentioning @bob in a message
// on Alice's channel routes to Bob and correctly scopes to Bob's session key.
func TestAgentIsolation_MentionRoutingSessionScoping(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AgentMentions: config.AgentMentionConfig{
			Triggers: []string{"@"},
		},
		Bindings: []config.AgentBinding{
			{
				AgentID:       "alice",
				Match:         config.BindingMatch{Channel: "slack"},
				AgentMentions: []string{"*"},
			},
		},
		Agents: config.AgentsConfig{
			BaseDir: tmpDir,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "alice", Name: "Alice", Default: true},
				{ID: "bob", Name: "Bob"},
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)

	msg := bus.InboundMessage{
		Channel: "slack",
		ChatID:  "C123",
		Content: "@bob what is your status?",
	}

	// Extract mentions and check route & scope
	al.extractMention(&msg)
	if msg.Metadata["mentioned_agent"] != "bob" {
		t.Fatalf("expected mentioned_agent bob, got %q", msg.Metadata["mentioned_agent"])
	}
	if msg.Content != "what is your status?" {
		t.Fatalf("expected stripped content, got %q", msg.Content)
	}

	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("resolveMessageRoute failed: %v", err)
	}
	if agent.ID != "bob" {
		t.Fatalf("expected agent Bob, got %q", agent.ID)
	}

	scopeKey := resolveScopeKey(route, msg.SessionKey)
	if scopeKey != "agent:bob:main" {
		t.Fatalf("expected scopeKey agent:bob:main, got %q (leak to %s!)", scopeKey, scopeKey)
	}
}

// TestAgentIsolation_ExtractMentionIsIdempotent guards the two-stage dispatch:
// processSessionMessage extracts the mention to pick the dispatch mutex, then
// processMessage extracts again on the same message. A second pass must not
// re-route a message whose stripped content starts with another mention.
func TestAgentIsolation_ExtractMentionIsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		AgentMentions: config.AgentMentionConfig{Triggers: []string{"@"}},
		Bindings: []config.AgentBinding{
			{AgentID: "alice", Match: config.BindingMatch{Channel: "slack"}, AgentMentions: []string{"*"}},
		},
		Agents: config.AgentsConfig{
			BaseDir:  tmpDir,
			Defaults: config.AgentDefaults{Models: []string{"test-model"}, MaxTokens: 4096, MaxToolIterations: 10},
			List: []config.AgentConfig{
				{ID: "alice", Name: "Alice", Default: true},
				{ID: "bob", Name: "Bob"},
			},
		},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)

	msg := bus.InboundMessage{Channel: "slack", ChatID: "C123", Content: "@alice @bob compare notes"}
	al.extractMention(&msg)
	al.extractMention(&msg)
	if got := msg.Metadata["mentioned_agent"]; got != "alice" {
		t.Fatalf("mentioned_agent = %q after two extractions, want alice", got)
	}
	if msg.Content != "@bob compare notes" {
		t.Fatalf("content = %q, want the first mention stripped only once", msg.Content)
	}
}
