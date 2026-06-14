// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/memory"
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

	// failure circuit breaker — in-memory, resets on restart. Counts consecutive
	// failed automatic compactions; once it reaches
	// defaultMaxConsecutiveCompactFailures the automatic path is suppressed until
	// msgCount advances past breakerTrippedUntilCount. A manual /compact (Compact)
	// and the 413-recovery path (ForceCompress) bypass the breaker.
	consecutiveCompactFailures int
	breakerTrippedUntilCount   int // 0 = not tripped

	// compression clients resolved at construction time
	compressClients []LLMClient

	// refusedModels tracks summarization models that refused this session's
	// content on content-policy grounds. Such models are skipped on subsequent
	// compactions for this session so we stop sending material to a model that
	// will not handle it. In-memory and per-session: cleared when the
	// ContextManager is rebuilt (config reload / restart). Guarded by refusedMu.
	refusedModels map[string]bool
	refusedMu     sync.Mutex

	// compression outcome tracking
	lastCompressedAt    time.Time
	lastCompressionGain float64
	lastReport          *CompactionReport // report from the most recent doCompress

	// archive is opened lazily on first write; nil until then.
	// archiveMu guards lazy initialisation; read operations do not need it.
	archive   *memory.ArchiveStore
	archiveMu sync.Mutex

	// sessionToken is the SST-prefixed per-session MCP token injected into the
	// system prompt by Build() so the LLM can call session-scoped tools. Set
	// via SetSessionToken; empty means no injection.
	sessionToken string

	// compressHook is called by compress() when non-nil. Only for testing.
	compressHook func(safetyNet bool)

	// archiveAppendHook, when non-nil, is invoked at the end of archiveAppend
	// with the seq and message just archived. The agent loop sets a closure
	// (cognitive agents only) that notifies the cogmem consolidation manager.
	// Nil for every non-cognitive agent → identical behavior to before.
	archiveAppendHook func(seq int64, msg providers.Message)

	// memoryBlocks, when non-nil, returns the cognitive-memory STABLE and ROUTED
	// prompt blocks for the session. Set by the agent loop for cognitive agents
	// only; Build injects the blocks into the system message when non-empty.
	memoryBlocks func(sessionKey string) (stable, routed string)
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

	m := &Manager{
		sessionKey:      sessionKey,
		store:           store,
		builder:         builder,
		llm:             llm,
		cfg:             cfg,
		compressClients: clients,
	}

	// 9c. Load durable compaction state if the store supports it.
	if cs, ok := store.(CompactionStateStore); ok {
		state, err := cs.GetCompactionState(sessionKey)
		if err == nil {
			m.msgCount = state.MeaningfulCount
			m.compressedAtCount = state.CompressedAtMeaningfulCount
			m.cooling = state.Cooling
			m.coolingSinceCount = state.CoolingSinceCount
			m.lastCompressedAt = state.SummaryGeneratedAt
		}
	}

	return m
}

// SetCallContext records the channel and chatID for the current call. The agent
// loop calls this before Build() so the system prompt receives the correct
// session context.
func (m *Manager) SetCallContext(channel, chatID string) {
	m.channel = channel
	m.chatID = chatID
}

func (m *Manager) AddUserMessage(ctx context.Context, msg providers.Message) error {
	seq := m.store.AddFullMessage(m.sessionKey, msg)
	m.archiveAppend(seq, msg)
	m.msgCount++
	if err := m.triggerCheck(ctx); err != nil {
		// Automatic triggers log and continue — do not block the LLM call.
		logger.WarnCF("llmcontext", "compression error on AddUserMessage (continuing)", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
	}
	return nil
}

func (m *Manager) AddAssistantMessage(ctx context.Context, msg providers.Message) error {
	seq := m.store.AddFullMessage(m.sessionKey, msg)
	m.archiveAppend(seq, msg)
	m.msgCount++
	if err := m.triggerCheck(ctx); err != nil {
		// Automatic triggers log and continue — do not block the LLM call.
		logger.WarnCF("llmcontext", "compression error on AddAssistantMessage (continuing)", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
	}
	return nil
}

// AddToolCallMessage records the assistant turn containing tool calls.
// Writes to session store and archive. Increments msgCount.
// Does NOT trigger a compression check — compression is deferred to PreDispatchCheck
// so that the check runs once per dispatch rather than after every tool-call message.
func (m *Manager) AddToolCallMessage(_ context.Context, msg providers.Message) error {
	seq := m.store.AddFullMessage(m.sessionKey, msg)
	m.archiveAppend(seq, msg)
	m.msgCount++
	return nil
}

// AddToolResult records a tool result message.
// Writes to session store and archive. Increments msgCount.
// Does NOT trigger a compression check — compression is deferred to PreDispatchCheck.
func (m *Manager) AddToolResult(_ context.Context, msg providers.Message) error {
	seq := m.store.AddFullMessage(m.sessionKey, msg)
	m.archiveAppend(seq, msg)
	m.msgCount++
	return nil
}

// PreDispatchCheck runs the mid-turn compression trigger and, if compression
// fires and succeeds, rebuilds the message slice via Build() and returns the
// fresh slice. If no compression is needed, returns current unchanged. If
// compression fails, logs the error and returns (current, ErrCompressionFailed)
// so the caller can proceed with the stale slice (best-effort).
//
// This runs at the top of every tool-call iteration, so it is restricted to the
// emergency safety-net trigger (contextPct >= safetyPercent): only an imminent
// context-window overflow may compact between iterations. First-stage compaction
// (normal-percent and message-count) is deferred to the turn boundary, where
// triggerCheck handles it on AddUserMessage / AddAssistantMessage — so it never
// fires mid-turn and posts a compaction notice in the middle of a tool-using turn.
//
// Build() is idempotent and has no write side-effects: it reads from the store and
// assembles the message slice without modifying any persistent state. It is safe to
// call multiple times within a single turn.
func (m *Manager) PreDispatchCheck(ctx context.Context, current []providers.Message) ([]providers.Message, error) {
	if m.cfg.contextWindow <= 0 {
		return current, nil
	}
	history := m.store.GetHistory(m.sessionKey)
	tokens := m.estTokens(history)
	contextPct := float64(tokens) * 100.0 / float64(m.cfg.contextWindow)

	// Emergency only: defer first-stage compaction to the turn boundary.
	if contextPct < float64(m.cfg.safetyPercent) {
		return current, nil
	}

	if err := m.compress(ctx, true); err != nil && !errors.Is(err, ErrNothingToCompress) {
		logger.WarnCF("llmcontext", "PreDispatchCheck: compression failed (continuing with stale slice)", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
		return current, ErrCompressionFailed
	}

	// Compression succeeded — rebuild to get a fresh slice that reflects the new
	// history state. Build() is read-only with no write side-effects.
	built, err := m.Build(ctx)
	if err != nil {
		logger.WarnCF("llmcontext", "PreDispatchCheck: Build after compression failed", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
		return current, err
	}
	return built, nil
}

// CheckAndCompress estimates the post-Build token count (Message.Content plus
// serialized ToolCalls arguments) and adds the configured overhead to account
// for system prompt, rendered summary, tool definitions, and completion budget.
// It runs once, after Build and before the first dispatch of a turn.
//
// It is restricted to the emergency safety-net trigger (adjusted total >=
// safetyPercent): its purpose is to avoid sending an over-window request on the
// first dispatch when stored history is within threshold but the rendered request
// (system prompt + tool defs + completion budget) pushes the total over. First-stage
// compaction (normal-percent and message-count) is handled at the turn boundary by
// triggerCheck, not here, so it never fires while a request is being assembled.
//
// If compression fires and succeeds, a fresh message slice is produced via Build()
// and returned. If no compression is needed the input slice is returned unchanged.
func (m *Manager) CheckAndCompress(ctx context.Context, built []providers.Message) ([]providers.Message, error) {
	if m.cfg.contextWindow <= 0 {
		return built, nil
	}

	tokens := m.estTokens(built) + m.cfg.overheadTokens
	contextPct := float64(tokens) * 100.0 / float64(m.cfg.contextWindow)

	// Emergency only: defer first-stage compaction to the turn boundary.
	if contextPct < float64(m.cfg.safetyPercent) {
		return built, nil
	}

	if err := m.compress(ctx, true); err != nil && !errors.Is(err, ErrNothingToCompress) {
		logger.WarnCF("llmcontext", "CheckAndCompress: compression failed (continuing with input slice)", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
		return built, ErrCompressionFailed
	}

	// Compression succeeded — rebuild to get a fresh slice reflecting the new
	// history state.
	fresh, err := m.Build(ctx)
	if err != nil {
		logger.WarnCF("llmcontext", "CheckAndCompress: Build after compression failed", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
		return built, err
	}
	return fresh, nil
}

// estimateTokens estimates token count using the package defaults
// (~4 chars/token, no safety margin). It is retained for callers and tests that
// have no Manager config in scope; Manager methods should use estTokens so the
// configured divisor and safety margin apply.
func estimateTokens(msgs []providers.Message) int {
	return estimateTokensWith(msgs, defaultCharsPerToken, defaultTokenSafetyMargin)
}

// estimateTokensWith estimates token count by dividing the total rune count by
// charsPerToken and inflating by safetyMargin. It counts both message Content
// and the JSON-serialized arguments of any ToolCalls, so tool-call turns are
// not under-counted. Non-positive tuning values fall back to the defaults.
func estimateTokensWith(msgs []providers.Message, charsPerToken, safetyMargin float64) int {
	if charsPerToken <= 0 {
		charsPerToken = defaultCharsPerToken
	}
	if safetyMargin <= 0 {
		safetyMargin = defaultTokenSafetyMargin
	}
	total := 0
	for _, m := range msgs {
		total += len([]rune(m.Content))
		if len(m.ToolCalls) > 0 {
			if data, err := json.Marshal(m.ToolCalls); err == nil {
				total += len([]rune(string(data)))
			}
		}
	}
	return int(float64(total) / charsPerToken * safetyMargin)
}

// estTokens estimates token count for msgs using this Manager's configured
// chars-per-token divisor and safety margin.
func (m *Manager) estTokens(msgs []providers.Message) int {
	return estimateTokensWith(msgs, m.cfg.charsPerToken, m.cfg.tokenSafetyMargin)
}

// getOrOpenArchive returns the ArchiveStore for this session, opening it lazily
// on first call. Returns nil when archiveDir is empty or the open fails.
func (m *Manager) getOrOpenArchive() *memory.ArchiveStore {
	if m.cfg.archiveDir == "" {
		return nil
	}
	m.archiveMu.Lock()
	defer m.archiveMu.Unlock()
	if m.archive != nil {
		return m.archive
	}
	sanitized := sanitizeSessionKey(m.sessionKey)
	path := filepath.Join(m.cfg.archiveDir, sanitized+".archive.db")
	store, err := memory.Open(path)
	if err != nil && !errors.Is(err, memory.ErrArchiveUnavailable) {
		logger.WarnCF("llmcontext", "archive open failed", map[string]any{
			"session_key": m.sessionKey,
			"path":        path,
			"error":       err.Error(),
		})
	}
	m.archive = store
	// Apply retention once per manager lifecycle, on the path that actually opens
	// a fresh archive (not on subsequent cached returns), so long-lived growth is
	// trimmed at startup.
	m.pruneArchive(store)
	return m.archive
}

// pruneArchive applies the four retention caps (message count/age and summary
// count/age) to the given archive using the manager's configured limits. Each
// cap is skipped when its configured value is <= 0. Best-effort: errors are
// logged and otherwise ignored so pruning never fails compaction or open.
func (m *Manager) pruneArchive(a *memory.ArchiveStore) {
	if a == nil {
		return
	}

	if n := m.cfg.archiveMessageCount; n > 0 {
		if err := a.PruneMessagesToCount(n); err != nil {
			logger.WarnCF("llmcontext", "archive prune messages to count failed", map[string]any{
				"session_key": m.sessionKey,
				"count":       n,
				"error":       err.Error(),
			})
		}
	}
	if d := m.cfg.archiveDays; d > 0 {
		cutoff := time.Now().AddDate(0, 0, -d)
		if err := a.PruneMessagesBefore(cutoff); err != nil {
			logger.WarnCF("llmcontext", "archive prune messages before failed", map[string]any{
				"session_key": m.sessionKey,
				"days":        d,
				"error":       err.Error(),
			})
		}
	}
	if n := m.cfg.summaryMaxCount; n > 0 {
		if err := a.PruneSummariesToCount(n); err != nil {
			logger.WarnCF("llmcontext", "archive prune summaries to count failed", map[string]any{
				"session_key": m.sessionKey,
				"count":       n,
				"error":       err.Error(),
			})
		}
	}
	if d := m.cfg.summaryRetentionDays; d > 0 {
		cutoff := time.Now().AddDate(0, 0, -d)
		if err := a.PruneSummariesBefore(cutoff); err != nil {
			logger.WarnCF("llmcontext", "archive prune summaries before failed", map[string]any{
				"session_key": m.sessionKey,
				"days":        d,
				"error":       err.Error(),
			})
		}
	}
}

// archiveAppend writes msg to the archive (if one is configured) keyed by the
// MEMORY seq assigned to the message by the session store. Using the memory seq
// (not a separate archive-private counter) keeps a single sequence space across
// the memory store, the archive, and the summaries that cite them: the seq the
// summarizer cites == the seq the strip validates == the seq stored in (and
// retrievable from) the archive.
//
// Cutover note: archives written before this change stored rows under an OLD,
// separate archive-space counter (1, 2, 3, ...). New messages now use the memory
// seq, which is typically far higher on a long-lived session. As a result,
// Bounds() returns a mixed min(legacy_low)..max(memory) range until those old
// rows age out of the retrieval window. This is harmless: the previous bug was
// the archive upper bound being too LOW (so fresh summary refs were stripped);
// now max tracks the memory seq, so fresh refs survive. The legacy low-seq rows
// are inert. No data migration is required.
//
// Best-effort: errors are logged but not returned to callers. A non-positive seq
// (e.g. a store that failed to assign one) is skipped to avoid corrupting the
// archive with seq 0.
func (m *Manager) archiveAppend(seq int64, msg providers.Message) {
	a := m.getOrOpenArchive()
	if a == nil {
		return
	}
	if seq <= 0 {
		logger.WarnCF("llmcontext", "archive append skipped: non-positive seq", map[string]any{
			"session_key": m.sessionKey,
			"seq":         seq,
		})
		return
	}
	msg = archiveTruncateContent(msg, m.archiveContentLimit())
	if err := a.Append(seq, msg, time.Now()); err != nil {
		logger.WarnCF("llmcontext", "archive append failed", map[string]any{
			"session_key": m.sessionKey,
			"seq":         seq,
			"error":       err.Error(),
		})
	}
	// Notify the cognitive-memory consolidation manager (cognitive agents only;
	// nil for everyone else). Best-effort and non-blocking by contract.
	if m.archiveAppendHook != nil {
		m.archiveAppendHook(seq, msg)
	}
}

// archiveContentMaxBytes is the default maximum number of content bytes stored
// per message in the archive. Messages whose Content exceeds this limit are
// truncated before writing; the LLM already saw the full content in the
// active context window, so only a compact summary is needed for history.
// Tool results that contain large file payloads are the primary use-case.
// Override per-agent via WithArchiveContentMaxBytes.
const archiveContentMaxBytes = 4096

// archiveContentLimit returns the effective per-message archive content cap,
// falling back to archiveContentMaxBytes when unconfigured.
func (m *Manager) archiveContentLimit() int {
	if m.cfg.archiveContentMaxBytes > 0 {
		return m.cfg.archiveContentMaxBytes
	}
	return archiveContentMaxBytes
}

// archiveTruncateContent returns a shallow copy of msg with Content truncated
// to maxBytes if it exceeds that limit. The original msg is not mutated.
func archiveTruncateContent(msg providers.Message, maxBytes int) providers.Message {
	if maxBytes <= 0 {
		maxBytes = archiveContentMaxBytes
	}
	if len(msg.Content) <= maxBytes {
		return msg
	}
	original := len(msg.Content)
	msg.Content = msg.Content[:maxBytes] +
		fmt.Sprintf("\n[content truncated: %d bytes total, first %d shown]", original, maxBytes)
	return msg
}

// sanitizeSessionKey converts a session key to a safe filename component,
// matching the logic in pkg/memory.sanitizeKey.
func sanitizeSessionKey(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// archiveWindow returns the effective [minSeq, maxSeq] range of retrievable
// archive messages for this session, applying the archiveMessageCount cap.
// Returns (0, 0) if no archive exists or the archive is unavailable.
func (m *Manager) archiveWindow() (minSeq, maxSeq int64) {
	a := m.getOrOpenArchive()
	if a == nil {
		return 0, 0
	}
	var err error
	minSeq, maxSeq, err = a.Bounds()
	if err != nil || maxSeq == 0 {
		return 0, 0
	}
	// Cutover note: because the archive is now keyed by the memory seq, Bounds()
	// may return a mixed range whose minSeq is an old archive-space value while
	// maxSeq is a high memory seq. That is expected and harmless — see
	// archiveAppend for the full explanation.
	if m.cfg.archiveMessageCount > 0 {
		floor := maxSeq - int64(m.cfg.archiveMessageCount) + 1
		if floor > minSeq {
			minSeq = floor
		}
	}
	if m.cfg.archiveDays > 0 && m.archive != nil {
		cutoff := time.Now().AddDate(0, 0, -m.cfg.archiveDays)
		if dayFloor, err := m.archive.MinSeqAfter(cutoff); err == nil && dayFloor > minSeq {
			minSeq = dayFloor
		}
	}
	return
}

// compress logs the compression trigger and dispatches to the LLM-based
// compressor. If compressHook is set (tests only), the hook short-circuits
// before doCompress so trigger tests remain independent of LLM behavior.
func (m *Manager) compress(ctx context.Context, safetyNet bool) error {
	// Failure circuit breaker: after repeated automatic-compaction failures,
	// suppress the automatic path until enough new messages accumulate. A manual
	// /compact (Compact) and the 413-recovery path (ForceCompress) bypass this.
	if m.autoCompactionSuppressed() {
		logger.InfoCF("llmcontext", "automatic compaction suppressed by circuit breaker", map[string]any{
			"session_key":          m.sessionKey,
			"consecutive_failures": m.consecutiveCompactFailures,
			"resume_at_msg_count":  m.breakerTrippedUntilCount,
		})
		return nil
	}

	history := m.store.GetHistory(m.sessionKey)
	tokens := m.estTokens(history)
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
	err := m.doCompress(ctx, safetyNet)
	m.recordCompactionOutcome(err)
	// Deliver the report for the automatic path. The manual /compact path returns
	// the report to the caller instead and leaves reportCallback unset here.
	if m.cfg.reportCallback != nil && m.lastReport != nil {
		m.cfg.reportCallback(m.channel, m.chatID, m.lastReport.String())
	}
	return err
}

// autoCompactionSuppressed reports whether the failure circuit breaker is
// currently tripped for the automatic compaction path. It clears the breaker
// once enough new messages have accumulated, allowing a fresh attempt.
func (m *Manager) autoCompactionSuppressed() bool {
	if m.breakerTrippedUntilCount == 0 {
		return false
	}
	if m.msgCount >= m.breakerTrippedUntilCount {
		m.breakerTrippedUntilCount = 0
		m.consecutiveCompactFailures = 0
		return false
	}
	return true
}

// recordCompactionOutcome updates the failure circuit breaker from a doCompress
// result. A genuine compression failure increments the consecutive-failure
// counter and trips the breaker at the threshold; success or a benign no-op
// (nothing to compress) resets it. Transient/other errors leave it unchanged.
func (m *Manager) recordCompactionOutcome(err error) {
	switch {
	case err == nil || errors.Is(err, ErrNothingToCompress):
		m.consecutiveCompactFailures = 0
		m.breakerTrippedUntilCount = 0
	case errors.Is(err, ErrCompressionFailed):
		m.consecutiveCompactFailures++
		if m.consecutiveCompactFailures >= defaultMaxConsecutiveCompactFailures && m.breakerTrippedUntilCount == 0 {
			m.breakerTrippedUntilCount = m.msgCount + defaultCompactFailureCooldownMessages
			logger.WarnCF("llmcontext", "compaction circuit breaker tripped; suppressing automatic compaction", map[string]any{
				"session_key":          m.sessionKey,
				"consecutive_failures": m.consecutiveCompactFailures,
				"resume_at_msg_count":  m.breakerTrippedUntilCount,
			})
		}
	}
}

// triggerCheck runs the unified compression trigger. Called at the end of
// AddUserMessage and AddAssistantMessage.
func (m *Manager) triggerCheck(ctx context.Context) error {
	history := m.store.GetHistory(m.sessionKey)
	tokens := m.estTokens(history)
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

// SetSessionToken stores the per-session MCP token for injection into the
// system prompt by Build(). Calling with an empty string disables injection.
func (m *Manager) SetSessionToken(token string) {
	m.sessionToken = token
}

// SetArchiveAppendHook installs a callback invoked at the end of every
// archiveAppend with the seq and message just written. Used by the agent loop
// (cognitive agents only) to notify the cogmem consolidation manager. Passing
// nil disables it.
func (m *Manager) SetArchiveAppendHook(fn func(seq int64, msg providers.Message)) {
	m.archiveAppendHook = fn
}

// SetMemoryBlocks installs a callback that returns the cognitive-memory STABLE
// and ROUTED prompt blocks for the session. Build injects them into the system
// message when either is non-empty. Passing nil disables injection.
func (m *Manager) SetMemoryBlocks(fn func(sessionKey string) (stable, routed string)) {
	m.memoryBlocks = fn
}

func (m *Manager) Build(_ context.Context) ([]providers.Message, error) {
	if m.builder == nil {
		return []providers.Message{}, nil
	}
	history := m.store.GetHistory(m.sessionKey)
	rawSummary := m.store.GetSummary(m.sessionKey)
	archiveMin, archiveMax := m.archiveWindow()
	rendered := renderSummaryFromRaw(rawSummary, archiveMin, archiveMax)

	// When there is no summary yet the rendered block is empty, so the LLM
	// has no knowledge of the archive. Inject a minimal bounds note so the
	// agent always knows the archive exists and which seq range is queryable.
	if rendered == "" && archiveMax > 0 {
		rendered = fmt.Sprintf(
			"## Session Archive\n\nMessages #%d–#%d are stored in the archive. "+
				"Use `mcp__claw__get_session_messages` with `seq_start`/`seq_end` to retrieve them, "+
				"or `mcp__claw__search_session_messages` to search by keyword.",
			archiveMin, archiveMax)
	}

	msgs := m.builder.BuildMessages(history, rendered, "", nil, m.channel, m.chatID)

	// Inject the session token into the system message so the LLM can call
	// mcp__claw__* tools. The token is appended after the static+dynamic
	// prompt so it is always present regardless of caching.
	if m.sessionToken != "" && len(msgs) > 0 && msgs[0].Role == "system" {
		sessionTokenSection := fmt.Sprintf(
			"# Session Token\n\nThe following token is confidential — never echo it to users or write it to files. "+
				"ALL `mcp__claw__*` tool calls MUST include "+
				"the literal string below as the `session_token` parameter.\n\nsession_token: %s",
			m.sessionToken)
		msgs[0].Content += "\n\n---\n\n" + sessionTokenSection
		msgs[0].SystemParts = append(msgs[0].SystemParts, providers.ContentBlock{
			Type: "text",
			Text: sessionTokenSection,
		})
	}

	// Inject cognitive-memory blocks (cognitive agents only; nil otherwise).
	// The STABLE block is cacheable (cache_control: ephemeral) and goes after the
	// static/dynamic prompt; the ROUTED block is per-turn-varying and trails
	// un-cached so it never invalidates the cached prefix. Both are mirrored into
	// msgs[0].Content for adapters that ignore SystemParts, matching the
	// session-token injection above.
	if m.memoryBlocks != nil && len(msgs) > 0 && msgs[0].Role == "system" {
		stable, routed := m.memoryBlocks(m.sessionKey)
		if stable != "" {
			msgs[0].Content += "\n\n---\n\n" + stable
			msgs[0].SystemParts = append(msgs[0].SystemParts, providers.ContentBlock{
				Type:         "text",
				Text:         stable,
				CacheControl: &providers.CacheControl{Type: "ephemeral"},
			})
		}
		if routed != "" {
			msgs[0].Content += "\n\n---\n\n" + routed
			msgs[0].SystemParts = append(msgs[0].SystemParts, providers.ContentBlock{
				Type: "text",
				Text: routed,
			})
		}
	}

	return msgs, nil
}

// Compact triggers a normal LLM-based compression pass, identical to what
// runs when the regular compression threshold is crossed. It is the manual
// /compact entry point and bypasses the failure circuit breaker, but a
// successful manual compaction still resets the breaker so the automatic path
// resumes.
func (m *Manager) Compact(ctx context.Context) error {
	err := m.doCompress(ctx, false)
	m.recordCompactionOutcome(err)
	return err
}

// RenderedSummary returns the current session summary rendered as Markdown
// (the same block Build() injects into the system prompt), or "" when there is
// no summary. Used by session_compact to show the agent what was just preserved.
func (m *Manager) RenderedSummary() string {
	archiveMin, archiveMax := m.archiveWindow()
	return renderSummaryFromRaw(m.store.GetSummary(m.sessionKey), archiveMin, archiveMax)
}

// LastCompactionReport returns the report produced by the most recent
// compaction pass, or nil if none has run on this manager.
func (m *Manager) LastCompactionReport() *CompactionReport {
	return m.lastReport
}

// ForceCompress aggressively reduces context by performing a group-aware backward
// walk: it keeps the most recent complete turn groups that fit within the context
// window, always preserving the current in-progress turn group (the last group in
// history). If the current turn group alone exceeds the context window, it returns
// ErrCompressionFailed with a descriptive message.
//
// A turn group is: a user message + optional assistant tool-call message + all
// matching tool results (matched by ToolCallID). Groups are identified by
// resolveGroup working backward from the last message.
func (m *Manager) ForceCompress(_ context.Context) error {
	history := m.store.GetHistory(m.sessionKey)

	// Separate system message.
	var sysMsg *providers.Message
	conversation := history
	if len(history) > 0 && history[0].Role == "system" {
		sys := history[0]
		sysMsg = &sys
		conversation = history[1:]
	}

	if len(conversation) <= 2 {
		return nil
	}

	// Walk backward collecting complete turn groups, newest first.
	type groupSpan struct{ start, end int }
	var groups []groupSpan
	i := len(conversation) - 1
	for i >= 0 {
		g := resolveGroup(conversation, i)
		groups = append(groups, groupSpan{g.start, g.end})
		i = g.start - 1
	}
	// groups[0] is the current (most recent) turn group.

	if len(groups) == 0 {
		return nil
	}

	// Check whether the current turn group alone fits the context window.
	currentGroupSlice := conversation[groups[0].start : groups[0].end+1]
	currentGroupTokens := m.estTokens(currentGroupSlice)
	sysTokens := 0
	if sysMsg != nil {
		sysTokens = m.estTokens([]providers.Message{*sysMsg})
	}
	if m.cfg.contextWindow > 0 && (currentGroupTokens+sysTokens)*100/m.cfg.contextWindow >= m.cfg.safetyPercent {
		return fmt.Errorf("%w: current turn group (%d tokens) alone exceeds context window (%d tokens)",
			ErrCompressionFailed, currentGroupTokens+sysTokens, m.cfg.contextWindow)
	}

	// Greedily add older groups until the window is full.
	kept := []groupSpan{groups[0]}
	totalTokens := currentGroupTokens + sysTokens
	for _, g := range groups[1:] {
		slice := conversation[g.start : g.end+1]
		cost := m.estTokens(slice)
		if m.cfg.contextWindow > 0 && (totalTokens+cost)*100/m.cfg.contextWindow >= m.cfg.safetyPercent {
			break
		}
		kept = append(kept, g)
		totalTokens += cost
	}

	// kept is newest-first; reverse to chronological order.
	for lo, hi := 0, len(kept)-1; lo < hi; lo, hi = lo+1, hi-1 {
		kept[lo], kept[hi] = kept[hi], kept[lo]
	}

	// Compute how many messages were dropped.
	keptMsgCount := 0
	for _, g := range kept {
		keptMsgCount += g.end - g.start + 1
	}
	droppedCount := len(conversation) - keptMsgCount

	// Build new history.
	capacity := keptMsgCount
	if sysMsg != nil {
		capacity++
	}
	newHistory := make([]providers.Message, 0, capacity)

	if sysMsg != nil {
		// Append compression note to the system prompt to avoid consecutive system messages.
		compressionNote := fmt.Sprintf(
			"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
			droppedCount,
		)
		enhanced := *sysMsg
		enhanced.Content += compressionNote
		newHistory = append(newHistory, enhanced)
	}
	for _, g := range kept {
		newHistory = append(newHistory, conversation[g.start:g.end+1]...)
	}

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
	tokens := m.estTokens(history)
	pct := 0.0
	if m.cfg.contextWindow > 0 {
		pct = float64(tokens) * 100.0 / float64(m.cfg.contextWindow)
	}
	// Estimate summary token count from the raw stored summary string using the
	// configured chars-per-token divisor (no safety margin — this is a stat).
	summaryTokens := 0
	rawSummary := m.store.GetSummary(m.sessionKey)
	if rawSummary != "" {
		cpt := m.cfg.charsPerToken
		if cpt <= 0 {
			cpt = defaultCharsPerToken
		}
		summaryTokens = int(float64(len([]rune(rawSummary))) / cpt)
	}
	return ContextStats{
		TotalMessages:       len(history),
		MeaningfulMessages:  m.msgCount,
		EstimatedTokens:     tokens,
		ContextWindowPct:    pct,
		LastCompressedAt:    m.lastCompressedAt,
		LastCompressionGain: m.lastCompressionGain,
		CompressionCooling:  m.cooling,
		CoolingSinceCount:   m.coolingSinceCount,
		SummaryTokens:       summaryTokens,
	}
}

// Close flushes durable compaction state and closes the archive connection.
// After Close the manager must not be used; it is called by the eviction
// goroutine and during AgentLoop shutdown.
//
// Close is safe to call on a manager that has never opened an archive.
func (m *Manager) Close(ctx context.Context) error {
	// Flush compaction state to the durable store so a new manager created for
	// the same session can restore counts and cooldown.
	if cs, ok := m.store.(CompactionStateStore); ok {
		state := memory.CompactionState{
			MeaningfulCount:             m.msgCount,
			CompressedAtMeaningfulCount: m.compressedAtCount,
			Cooling:                     m.cooling,
			CoolingSinceCount:           m.coolingSinceCount,
			SummaryGeneratedAt:          m.lastCompressedAt,
		}
		if err := cs.SetCompactionState(m.sessionKey, state); err != nil {
			logger.WarnCF("llmcontext", "Close: failed to persist compaction state", map[string]any{
				"session_key": m.sessionKey,
				"error":       err.Error(),
			})
		}
		if err := m.store.Save(m.sessionKey); err != nil {
			logger.WarnCF("llmcontext", "Close: failed to save session", map[string]any{
				"session_key": m.sessionKey,
				"error":       err.Error(),
			})
		}
	}

	// Close the archive connection.
	m.archiveMu.Lock()
	defer m.archiveMu.Unlock()
	if m.archive != nil {
		if err := m.archive.Close(); err != nil {
			logger.WarnCF("llmcontext", "Close: archive close failed", map[string]any{
				"session_key": m.sessionKey,
				"error":       err.Error(),
			})
		}
		m.archive = nil
	}
	return nil
}

// Reset clears the active conversation — history window, current rolling
// summary, and in-memory compression state — but PRESERVES the durable archive
// (long-term memory) and the summary log. After Reset the session starts a fresh
// conversation while retaining full recall via session_messages and
// session_summary_*. A hard wipe (erase long-term memory) is done by deleting the
// per-session .archive.db file manually; there is no destructive clear.
func (m *Manager) Reset(ctx context.Context) error {
	// 1. Clear in-memory compression state.
	m.msgCount = 0
	m.compressedAtCount = 0
	m.cooling = false
	m.coolingSinceCount = 0
	m.lastCompressedAt = time.Time{}
	m.lastCompressionGain = 0

	// 2. Clear the pending-turn flag so no stale flag survives the reset.
	if err := m.store.ClearPendingTurn(m.sessionKey); err != nil {
		logger.WarnCF("llmcontext", "Reset: ClearPendingTurn failed", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
	}

	// 3. Wipe the active history window and the current rolling summary. The
	// archive (keyed by memory seq) and the summary log are intentionally left
	// intact — the agent keeps its long-term memory across a clear; new messages
	// continue under the next memory seq the store assigns.
	m.store.TruncateHistory(m.sessionKey, 0)
	m.store.SetSummary(m.sessionKey, "")

	// 4. If the store implements CompactionStateStore, write zeroed state back.
	if cs, ok := m.store.(CompactionStateStore); ok {
		if setErr := cs.SetCompactionState(m.sessionKey, memory.CompactionState{}); setErr != nil {
			logger.WarnCF("llmcontext", "Reset: failed to persist compaction state", map[string]any{
				"session_key": m.sessionKey,
				"error":       setErr.Error(),
			})
		}
	}

	return m.store.Save(m.sessionKey)
}
