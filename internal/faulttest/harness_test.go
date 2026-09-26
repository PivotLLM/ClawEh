// ClawEh
// License: MIT

package faulttest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/test/stubprovider"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/state"
	"github.com/PivotLLM/ClawEh/tools"
)

const (
	// testChannel is a real (non-internal) channel name so the loop records
	// the turn source for recovery and posts fallback notices.
	testChannel = "webui"
	testChatID  = "chat-1"

	primaryAlias    = "primary"
	fallbackAlias   = "fallback"
	primaryProvider = "stub-a"
	primaryModel    = "stub-primary"

	// compressionNotice is what the loop posts before ForceCompress
	// (agent/loop_turn.go).
	compressionNotice = "Context window exceeded. Compressing history and retrying..."

	turnWait = 5 * time.Second
	quiet    = 300 * time.Millisecond
)

func TestMain(m *testing.M) {
	// The loop logs every dispatch at INFO; keep the test output readable.
	logger.SetLevel(logger.WARN)
	os.Exit(m.Run())
}

// newConfig builds a two-model config: alias "primary" on stub provider
// "stub-a" and alias "fallback" on "stub-b", one default agent "main" under a
// fresh base dir. fallback may be nil for a single-model chain.
func newConfig(t *testing.T, primary, fallback *stubprovider.Server) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: primaryProvider, Protocol: "openai-chat", BaseURL: primary.URL(), APIKey: "test-key"},
		},
		Models: []config.ModelConfig{
			{ModelName: primaryAlias, Model: primaryModel, Provider: primaryProvider, Enabled: true},
		},
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            []string{primaryAlias},
				MaxTokens:         512,
				MaxToolIterations: 5,
				// Bound every HTTP call so a stub that hangs cannot outlive the test.
				RequestTimeout: 5,
				// No progress heartbeats on the outbound bus.
				ProgressInterval: -1,
			},
			List: []config.AgentConfig{{ID: "main", Name: "Main", Default: true}},
		},
	}
	if fallback != nil {
		cfg.Providers = append(cfg.Providers,
			config.Provider{Name: "stub-b", Protocol: "openai-chat", BaseURL: fallback.URL(), APIKey: "test-key"})
		cfg.Models = append(cfg.Models,
			config.ModelConfig{ModelName: fallbackAlias, Model: "stub-fallback", Provider: "stub-b", Enabled: true})
		cfg.Agents.Defaults.Models = append(cfg.Agents.Defaults.Models, fallbackAlias)
	}
	return cfg
}

// inertChannel is registered so the channel manager reports testChannel
// configured; it never delivers anything (tests read the bus directly).
type inertChannel struct{ *channels.BaseChannel }

func (inertChannel) Start(context.Context) error                     { return nil }
func (inertChannel) Stop(context.Context) error                      { return nil }
func (inertChannel) Send(context.Context, bus.OutboundMessage) error { return nil }

// loop is one running AgentLoop with its bus.
type loop struct {
	t   *testing.T
	cfg *config.Config
	bus *bus.MessageBus
	al  *agent.AgentLoop

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// startLoop builds the loop the gateway would: dispatcher from the config, the
// primary model's provider as the agent default, a channel manager that knows
// testChannel, and Run in the background. extraTools are registered before
// Run. The loop is stopped and closed when the test ends.
func startLoop(t *testing.T, cfg *config.Config, msgBus *bus.MessageBus, extraTools ...tools.Tool) *loop {
	t.Helper()
	provider, _, err := providers.CreateProviderFromConfig(&cfg.Models[0], &cfg.Providers[0])
	if err != nil {
		t.Fatalf("CreateProviderFromConfig: %v", err)
	}
	dispatcher := providers.NewProviderDispatcher(cfg)
	al, err := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	cm, err := channels.NewManager(&config.Config{}, msgBus, nil)
	if err != nil {
		t.Fatalf("channels.NewManager: %v", err)
	}
	cm.RegisterChannel(testChannel, inertChannel{channels.NewBaseChannel(testChannel, nil, msgBus, nil)})
	al.SetChannelManager(cm)
	for _, tool := range extraTools {
		al.RegisterTool(tool)
	}

	ctx, cancel := context.WithCancel(context.Background())
	l := &loop{t: t, cfg: cfg, bus: msgBus, al: al, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		if err := al.Run(ctx); err != nil {
			t.Errorf("AgentLoop.Run: %v", err)
		}
	}()
	t.Cleanup(l.stop)
	return l
}

// stop ends Run (like a shutdown) and releases the loop's resources.
// Idempotent.
func (l *loop) stop() {
	l.once.Do(func() {
		l.al.Stop()
		l.cancel()
		select {
		case <-l.done:
		case <-time.After(turnWait):
			l.t.Error("AgentLoop.Run did not return after cancel")
		}
		l.al.Close()
	})
}

// crash ends Run without Close, the way a process kill leaves the data dir:
// whatever is mid-turn stays flagged pending.
func (l *loop) crash() {
	l.once.Do(func() {
		l.al.Stop()
		l.cancel()
		select {
		case <-l.done:
		case <-time.After(turnWait):
			l.t.Error("AgentLoop.Run did not return after cancel")
		}
	})
}

// send publishes a user message on testChannel.
func (l *loop) send(content string) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := l.bus.PublishInbound(ctx, bus.InboundMessage{
		Channel:  testChannel,
		ChatID:   testChatID,
		SenderID: "user1",
		Content:  content,
		Peer:     bus.Peer{Kind: "direct", ID: "user1"},
	})
	if err != nil {
		l.t.Fatalf("PublishInbound: %v", err)
	}
}

// isNotice reports whether an outbound message is a mid-turn heads-up rather
// than the turn's reply: fallback/skip notices, the compression notice and
// the restart notices.
func isNotice(m bus.OutboundMessage) bool {
	return strings.HasPrefix(m.Content, "⚠️") ||
		m.Content == compressionNotice ||
		strings.HasPrefix(m.Content, "I was restarted")
}

// collectUntil reads outbound messages until done(all) is true or timeout
// elapses, then keeps draining for quiet so anything published just after
// the condition is caught too.
func collectUntil(t *testing.T, msgBus *bus.MessageBus, timeout time.Duration, done func([]bus.OutboundMessage) bool) []bus.OutboundMessage {
	t.Helper()
	var all []bus.OutboundMessage
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && !done(all) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		m, ok := msgBus.SubscribeOutbound(ctx)
		cancel()
		if !ok {
			break
		}
		all = append(all, m)
	}
	all = append(all, drain(msgBus, quiet)...)
	return all
}

// drain returns everything published within d of the last message.
func drain(msgBus *bus.MessageBus, d time.Duration) []bus.OutboundMessage {
	var out []bus.OutboundMessage
	for {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		m, ok := msgBus.SubscribeOutbound(ctx)
		cancel()
		if !ok {
			return out
		}
		out = append(out, m)
	}
}

// turn sends content and returns the turn's reply and the notices published
// before it. It fails when no reply arrives, when more than one non-notice
// message is published, or when a message targets another channel/chat.
func (l *loop) turn(content string) (reply string, notices []string) {
	l.t.Helper()
	l.send(content)
	all := collectUntil(l.t, l.bus, turnWait, func(ms []bus.OutboundMessage) bool {
		for _, m := range ms {
			if !isNotice(m) {
				return true
			}
		}
		return false
	})
	var replies []string
	for _, m := range all {
		if m.Channel != testChannel || m.ChatID != testChatID {
			l.t.Errorf("outbound on %s/%s, want %s/%s: %q", m.Channel, m.ChatID, testChannel, testChatID, m.Content)
		}
		if isNotice(m) {
			notices = append(notices, m.Content)
		} else {
			replies = append(replies, m.Content)
		}
	}
	if len(replies) != 1 {
		l.t.Fatalf("got %d replies, want exactly 1: %q (notices %q)", len(replies), replies, notices)
	}
	return replies[0], notices
}

// cooldowns runs /cooldowns and returns its rendering of the process-wide
// cooldown tracker.
func (l *loop) cooldowns() string {
	l.t.Helper()
	reply, notices := l.turn("/cooldowns list")
	if len(notices) != 0 {
		l.t.Errorf("/cooldowns produced notices: %q", notices)
	}
	return reply
}

// stateFile is the default agent's persisted state (pending_turns lives here).
func stateFile(cfg *config.Config) string {
	return filepath.Join(cfg.Agents.BaseDir, "default", "state", "state.json")
}

// pendingTurns reads pending_turns from the default agent's state.json; an
// absent file is an empty map.
func pendingTurns(t *testing.T, cfg *config.Config) map[string]state.PendingTurn {
	t.Helper()
	raw, err := os.ReadFile(stateFile(cfg))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st state.State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("parse state.json: %v", err)
	}
	return st.PendingTurns
}

// waitFor polls cond until it holds or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// blockingTool is a tool call that never finishes on its own: Execute
// signals started and then waits for release, ignoring its context (a
// process kill mid-tool is what recovery exists for).
type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingTool() *blockingTool {
	return &blockingTool{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blockingTool) Name() string        { return "block" }
func (b *blockingTool) Description() string { return "Blocks until the test releases it" }
func (b *blockingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (b *blockingTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	b.started <- struct{}{}
	<-b.release
	return tools.SilentResult("released")
}

// waitStarted fails unless a tool call begins within timeout.
func (b *blockingTool) waitStarted(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(timeout):
		t.Fatal("tool call did not start")
	}
}
