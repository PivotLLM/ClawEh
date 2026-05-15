// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"fmt"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/session"
)

// Manager implements ContextManager using a SessionStore for persistence and a
// MessageBuilder for system-prompt assembly.
type Manager struct {
	sessionKey string
	store      session.SessionStore
	builder    MessageBuilder
	llm        LLMClient
	cfg        managerConfig

	// channel/chatID updated per-call via SetCallContext for use in Build()
	channel string
	chatID  string

	// trigger state — in-memory
	msgCount          int  // total messages added since last compression
	compressedAtCount int  // msgCount at last compression (count trigger reset window)
	cooling           bool // true while in the post-compression cooldown window
	coolingSinceCount int  // msgCount when cooling started

	// compression clients resolved at construction time
	compressClients []LLMClient

	// compression outcome tracking
	lastCompressedAt   time.Time
	lastCompressionGain float64

	// compressHook is called by compress() when non-nil. Only for testing.
	compressHook func(safetyNet bool)
}

// New constructs a ContextManager. Options are applied over package defaults.
// Validation order: (a) zero percent → default; (b) safety ≤ normal → WARN;
// (c) min ≥ normal → WARN, set min = normalPercent/2.
func New(
	sessionKey string,
	store session.SessionStore,
	builder MessageBuilder,
	llm LLMClient,
	opts ...Option,
) ContextManager {
	cfg := defaultManagerConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// (a) Zero percent thresholds → replace with defaults.
	if cfg.minPercent == 0 {
		cfg.minPercent = defaultMinPercent
	}
	if cfg.normalPercent == 0 {
		cfg.normalPercent = defaultNormalPercent
	}
	if cfg.safetyPercent == 0 {
		cfg.safetyPercent = defaultSafetyPercent
	}

	// (b) safety ≤ normal → WARN only; safety net will never fire.
	if cfg.safetyPercent <= cfg.normalPercent {
		logger.WarnCF("llmcontext", "compress_safety_percent <= compress_normal_percent; safety net will never fire",
			map[string]any{"safety": cfg.safetyPercent, "normal": cfg.normalPercent})
	}

	// (c) min ≥ normal → WARN + clamp to normal/2.
	if cfg.minPercent >= cfg.normalPercent {
		logger.WarnCF("llmcontext", "compress_min_percent >= compress_normal_percent; clamping min to normal/2",
			map[string]any{"min": cfg.minPercent, "normal": cfg.normalPercent})
		cfg.minPercent = cfg.normalPercent / 2
	}

	// Resolve compression clients: prefer explicit list, fall back to primary llm.
	clients := cfg.compressClients
	if len(clients) == 0 && llm != nil {
		clients = []LLMClient{llm}
	}

	return &Manager{
		sessionKey:      sessionKey,
		store:           store,
		builder:         builder,
		llm:             llm,
		cfg:             cfg,
		compressClients: clients,
	}
}

// SetCallContext records the channel and chatID for the current call. The agent
// loop calls this before Build() so the system prompt receives the correct
// session context.
func (m *Manager) SetCallContext(channel, chatID string) {
	m.channel = channel
	m.chatID = chatID
}

func (m *Manager) AddUserMessage(ctx context.Context, msg providers.Message) error {
	m.store.AddFullMessage(m.sessionKey, msg)
	m.msgCount++
	return m.triggerCheck(ctx)
}

func (m *Manager) AddAssistantMessage(ctx context.Context, msg providers.Message) error {
	m.store.AddFullMessage(m.sessionKey, msg)
	m.msgCount++
	return m.triggerCheck(ctx)
}

// estimateTokens estimates token count using ~4 chars/token.
func estimateTokens(msgs []providers.Message) int {
	total := 0
	for _, m := range msgs {
		total += len([]rune(m.Content))
	}
	return total / 4
}

// archiveWindow returns the effective [minSeq, maxSeq] range of retrievable
// archive messages for this session, applying the archiveMessageCount cap.
// Returns (0, 0) if no archive exists.
func (m *Manager) archiveWindow() (minSeq, maxSeq int) {
	minSeq, maxSeq = m.store.GetArchiveBounds(m.sessionKey)
	if maxSeq == 0 {
		return 0, 0
	}
	if m.cfg.archiveMessageCount > 0 {
		floor := maxSeq - m.cfg.archiveMessageCount + 1
		if floor > minSeq {
			minSeq = floor
		}
	}
	return
}

// compress logs the compression trigger and dispatches to the LLM-based
// compressor. If compressHook is set (tests only), the hook short-circuits
// before doCompress so trigger tests remain independent of LLM behavior.
func (m *Manager) compress(ctx context.Context, safetyNet bool) error {
	history := m.store.GetHistory(m.sessionKey)
	tokens := estimateTokens(history)
	contextPct := 0.0
	if m.cfg.contextWindow > 0 {
		contextPct = float64(tokens) * 100.0 / float64(m.cfg.contextWindow)
	}
	logger.InfoCF("llmcontext", "compression triggered", map[string]any{
		"session_key": m.sessionKey,
		"safety_net":  safetyNet,
		"context_pct": contextPct,
		"msg_count":   m.msgCount,
	})
	if m.compressHook != nil {
		m.compressedAtCount = m.msgCount
		m.compressHook(safetyNet)
		return nil
	}
	return m.doCompress(ctx, safetyNet)
}

// triggerCheck runs the unified compression trigger. Called at the end of
// AddUserMessage and AddAssistantMessage.
func (m *Manager) triggerCheck(ctx context.Context) error {
	history := m.store.GetHistory(m.sessionKey)
	tokens := estimateTokens(history)
	if m.cfg.contextWindow <= 0 {
		return nil
	}
	contextPct := float64(tokens) * 100.0 / float64(m.cfg.contextWindow)

	// Floor: no compression regardless of other triggers.
	if contextPct < float64(m.cfg.minPercent) {
		return nil
	}

	// Safety net overrides everything.
	if contextPct >= float64(m.cfg.safetyPercent) {
		return m.compress(ctx, true)
	}

	// Cooldown suppresses normal and count triggers.
	if m.cooling {
		coolAge := m.msgCount - m.coolingSinceCount
		if coolAge >= defaultCooldownMessages {
			m.cooling = false
		}
	}

	countTriggered := m.cfg.messageThreshold > 0 &&
		(m.msgCount-m.compressedAtCount) >= m.cfg.messageThreshold

	if (contextPct >= float64(m.cfg.normalPercent) || countTriggered) && !m.cooling {
		return m.compress(ctx, false)
	}

	return nil
}

// SetTestCompressHook sets a hook function that is called whenever compress()
// fires. Only for use in tests.
func (m *Manager) SetTestCompressHook(fn func(safetyNet bool)) {
	m.compressHook = fn
}

func (m *Manager) SetSystemPrompt(_ string) {
	// Phase 0: no-op; ContextBuilder owns the system prompt.
}

func (m *Manager) Build(_ context.Context) ([]providers.Message, error) {
	if m.builder == nil {
		return []providers.Message{}, nil
	}
	history := m.store.GetHistory(m.sessionKey)
	rawSummary := m.store.GetSummary(m.sessionKey)
	archiveMin, archiveMax := m.archiveWindow()
	rendered := renderSummaryFromRaw(rawSummary, archiveMin, archiveMax)
	msgs := m.builder.BuildMessages(history, rendered, "", nil, m.channel, m.chatID)
	return msgs, nil
}

// Compact triggers a normal LLM-based compression pass, identical to what
// runs when the regular compression threshold is crossed.
func (m *Manager) Compact(ctx context.Context) error {
	return m.doCompress(ctx, false)
}

// ForceCompress aggressively reduces context by dropping the oldest half of the
// conversation. This is the last-resort handler for hard context-limit errors.
// Migrated from pkg/agent/loop.go forceCompression.
func (m *Manager) ForceCompress(_ context.Context) error {
	history := m.store.GetHistory(m.sessionKey)
	if len(history) <= 4 {
		return nil
	}

	// Keep system prompt (if present at [0]) and the very last message (user's trigger).
	// Drop the oldest half of the conversation.
	hasSystemPrompt := history[0].Role == "system"
	conversationStart := 0
	if hasSystemPrompt {
		conversationStart = 1
	}
	conversation := history[conversationStart : len(history)-1]
	if len(conversation) == 0 {
		return nil
	}

	mid := len(conversation) / 2
	droppedCount := mid
	keptConversation := conversation[mid:]

	capacity := len(keptConversation) + 1
	if hasSystemPrompt {
		capacity++
	}
	newHistory := make([]providers.Message, 0, capacity)

	if hasSystemPrompt {
		// Append compression note to the original system prompt instead of adding a
		// new system message — avoids consecutive system messages that some APIs reject.
		compressionNote := fmt.Sprintf(
			"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
			droppedCount,
		)
		enhanced := history[0]
		enhanced.Content += compressionNote
		newHistory = append(newHistory, enhanced)
	}
	newHistory = append(newHistory, keptConversation...)
	newHistory = append(newHistory, history[len(history)-1])

	m.store.SetHistory(m.sessionKey, newHistory)
	if err := m.store.Save(m.sessionKey); err != nil {
		return fmt.Errorf("llmcontext: force compress save: %w", err)
	}

	logger.WarnCF("llmcontext", "force compression executed", map[string]any{
		"session_key":  m.sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
	})
	return nil
}

func (m *Manager) Stats() ContextStats {
	history := m.store.GetHistory(m.sessionKey)
	tokens := estimateTokens(history)
	pct := 0.0
	if m.cfg.contextWindow > 0 {
		pct = float64(tokens) * 100.0 / float64(m.cfg.contextWindow)
	}
	return ContextStats{
		TotalMessages:       len(history),
		EstimatedTokens:     tokens,
		ContextWindowPct:    pct,
		LastCompressedAt:    m.lastCompressedAt,
		LastCompressionGain: m.lastCompressionGain,
		CompressionCooling:  m.cooling,
		CoolingSinceCount:   m.coolingSinceCount,
	}
}

func (m *Manager) Reset() error {
	m.store.TruncateHistory(m.sessionKey, 0)
	m.store.SetSummary(m.sessionKey, "")
	return m.store.Save(m.sessionKey)
}
