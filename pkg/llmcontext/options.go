// ClawEh
// License: MIT

package llmcontext

import "github.com/PivotLLM/ClawEh/pkg/providers"

// Option is a functional option for configuring a Manager.
type Option func(*managerConfig)

const (
	defaultMinPercent       = 20
	defaultNormalPercent    = 50
	defaultSafetyPercent    = 80
	defaultMessageThreshold = 100
	// defaultRetainTokenPercent must stay clearly BELOW defaultMinPercent. When
	// the retained tail is the same size as the floor that gates compaction,
	// every pass shaves back to exactly the floor and the next message crosses it
	// again — the session compacts constantly without ever shrinking. See
	// validation rule (d) in New().
	defaultRetainTokenPercent    = 10
	defaultRetainMinMessages     = 2
	defaultMinCompressionGain    = 0.05
	defaultCooldownMessages      = 5
	defaultLargeMsgOffset        = 20
	defaultArchiveMessageCount   = 0
	defaultCompressTargetFactor  = 0.5
	defaultMinLoopGain           = 0.10
	defaultMaxCompressIterations = 3
	defaultOverheadTokens        = 4000
	defaultCharsPerToken         = 4.0
	defaultTokenSafetyMargin     = 1.0

	// defaultTriggerDays fires compaction once the oldest message in the live
	// window is older than this many days, regardless of how little of the
	// context window it occupies. Without it a low-volume session never reaches
	// any percentage threshold and its history sits in the window indefinitely.
	defaultTriggerDays = 7
	// defaultRetainMaxAgeDays caps the age of the retained tail: compaction
	// summarizes anything older, subject to retainMinMessages and the
	// last-user-message clamp. It is deliberately LOWER than defaultTriggerDays
	// so each pass buys (trigger - retain) days of quiet instead of pinning the
	// session to the trigger boundary and re-firing on every message.
	defaultRetainMaxAgeDays = 5

	// mediaTokensPerItem is the flat token cost charged per media item by the
	// estimator. Providers bill images by resolution tiles, so the rune length
	// of a base64 data: URI overstates the real cost by orders of magnitude;
	// a fixed per-item figure is far closer than either counting or ignoring it.
	mediaTokensPerItem = 1500

	// defaultMaxConsecutiveCompactFailures is the number of consecutive failed
	// automatic compactions after which the automatic compaction path is
	// suppressed for a session (the failure circuit breaker trips).
	defaultMaxConsecutiveCompactFailures = 3
	// defaultCompactFailureCooldownMessages is how many additional messages must
	// accumulate after the breaker trips before the automatic path is retried.
	defaultCompactFailureCooldownMessages = 20
)

// managerConfig holds resolved configuration for a Manager.
type managerConfig struct {
	minPercent         int
	normalPercent      int
	safetyPercent      int
	messageThreshold   int
	retainTokenPercent int
	retainMinMessages  int
	// targetPercent is the stop condition for the compaction loop: iterate until
	// the live window is below this percentage of the context window. 0 derives
	// it from normalPercent (see compressTargetPercent), preserving the historic
	// normalPercent*0.5 behaviour for configs that do not set it.
	targetPercent int
	// retainMaxTokens is an absolute ceiling on the retained tail, applied
	// alongside retainTokenPercent (the smaller wins). Percentages alone scale
	// with the window, so a 1M-token model inherits a tail budget tuned for
	// 128k and keeps an absurd absolute amount. 0 disables the cap.
	retainMaxTokens int
	// retainMaxAgeDays caps the age of the retained tail. 0 disables the cap.
	retainMaxAgeDays int
	// triggerDays fires compaction when the oldest live message exceeds this
	// age, bypassing minPercent. 0 disables the trigger.
	triggerDays         int
	compressModel       ModelChain
	compressClients     []LLMClient
	archiveMessageCount int
	archiveDays         int
	// summaryMaxCount caps the number of stored summaries (keep newest N).
	// 0 (the default) disables the count cap.
	summaryMaxCount int
	// summaryRetentionDays deletes summaries older than N days. 0 (the default)
	// disables the age cutoff.
	summaryRetentionDays int
	archiveDir           string
	contextWindow        int
	overheadTokens       int
	maxSummaryTokens     int // 0 = use 20% of contextWindow at truncation time
	// charsPerToken is the divisor used to convert a rune count into an
	// estimated token count. Lower values estimate more tokens per character
	// (more conservative). Default: 4.0.
	charsPerToken float64
	// tokenSafetyMargin multiplies the token estimate so it errs high. A value
	// of 1.1 inflates the estimate by 10%, triggering compression slightly
	// earlier. Default: 1.0 (no inflation).
	tokenSafetyMargin float64
	// archiveContentMaxBytes caps per-message content stored in the archive.
	// 0 (the default) resolves to archiveContentMaxBytes at write time.
	archiveContentMaxBytes int
	notifyCallback         func(msg string)
	// reportCallback, when set, is invoked by the automatic compaction path with
	// the current call's channel/chatID and the formatted compaction report so it
	// can be delivered to the user. The manual /compact path returns the report
	// directly instead and does not use this callback.
	reportCallback func(channel, chatID, text string)
	// compactDebug enables verbatim request/response capture of each
	// summarization LLM invocation to <compressionProfileDir>/compact.jsonl.
	compactDebug bool
	// compressionProfileDir is the agent workspace directory. If non-empty and
	// a file named "COMPRESSION.md" (or legacy "compression.md") exists there, its content is appended to the
	// summarization prompt so agents can declare role-specific compression rules.
	compressionProfileDir string
	// compressFailureDumpDir, when non-empty, is the logs/dumps directory to
	// which the request + raw response of each FAILED summarization attempt is
	// written for diagnosis.
	compressFailureDumpDir string
	// cooldownPolicy, when non-nil, sets the summarization-model cooldown policy.
	// Nil resolves to providers.DefaultCooldownPolicy() in New().
	cooldownPolicy *providers.CooldownPolicy
	// cooldownTracker, when non-nil, is shared with the compaction path instead
	// of building a private tracker (so cooldowns are unified with the main chain).
	cooldownTracker *providers.CooldownTracker
	// eviction is the per-turn tool-result eviction policy. Defaults to
	// DefaultEvictionPolicy() (enabled); override via WithEvictionPolicy.
	eviction EvictionPolicy
}

func defaultManagerConfig() managerConfig {
	return managerConfig{
		minPercent:          defaultMinPercent,
		normalPercent:       defaultNormalPercent,
		safetyPercent:       defaultSafetyPercent,
		messageThreshold:    defaultMessageThreshold,
		retainTokenPercent:  defaultRetainTokenPercent,
		retainMinMessages:   defaultRetainMinMessages,
		retainMaxAgeDays:    defaultRetainMaxAgeDays,
		triggerDays:         defaultTriggerDays,
		archiveMessageCount: defaultArchiveMessageCount,
		contextWindow:       128000,
		overheadTokens:      defaultOverheadTokens,
		charsPerToken:       defaultCharsPerToken,
		tokenSafetyMargin:   defaultTokenSafetyMargin,
		eviction:            DefaultEvictionPolicy(),
	}
}

// WithEvictionPolicy sets the per-turn tool-result eviction policy. The agent
// layer resolves per-agent + defaults config into a single EvictionPolicy and
// passes it here. When unset, DefaultEvictionPolicy() applies.
func WithEvictionPolicy(p EvictionPolicy) Option {
	return func(c *managerConfig) { c.eviction = p }
}

func WithMinPercent(pct int) Option {
	return func(c *managerConfig) { c.minPercent = pct }
}

func WithNormalPercent(pct int) Option {
	return func(c *managerConfig) { c.normalPercent = pct }
}

func WithSafetyPercent(pct int) Option {
	return func(c *managerConfig) { c.safetyPercent = pct }
}

func WithMessageThreshold(n int) Option {
	return func(c *managerConfig) { c.messageThreshold = n }
}

func WithRetainTokenPercent(pct int) Option {
	return func(c *managerConfig) { c.retainTokenPercent = pct }
}

func WithRetainMinMessages(n int) Option {
	return func(c *managerConfig) { c.retainMinMessages = n }
}

// WithTargetPercent sets the compaction loop's stop condition: iterate until the
// live window falls below this percentage of the context window. 0 restores the
// derived default (normalPercent * defaultCompressTargetFactor).
func WithTargetPercent(pct int) Option {
	return func(c *managerConfig) { c.targetPercent = pct }
}

// WithRetainMaxTokens sets an absolute ceiling on the retained tail, applied
// alongside the percentage budget (the smaller wins). 0 disables the cap.
func WithRetainMaxTokens(n int) Option {
	return func(c *managerConfig) { c.retainMaxTokens = n }
}

// WithRetainMaxAgeDays caps the age of the retained tail. Anything older is
// summarized, subject to the retainMinMessages floor and the last-user-message
// clamp. 0 disables the cap.
func WithRetainMaxAgeDays(d int) Option {
	return func(c *managerConfig) { c.retainMaxAgeDays = d }
}

// WithTriggerDays fires compaction once the oldest live message is older than d
// days. Unlike the percentage and count triggers it bypasses minPercent, so a
// low-volume session still ages out. 0 disables the trigger.
func WithTriggerDays(d int) Option {
	return func(c *managerConfig) { c.triggerDays = d }
}

// WithCompressModel records the model chain for stats and logging only.
func WithCompressModel(model ModelChain) Option {
	return func(c *managerConfig) { c.compressModel = model }
}

// WithCompressLLM sets the callable clients used by compress(). The agent layer
// resolves ModelChain → []LLMClient and passes them here. If not set, the llm
// passed to New() is used for compression.
func WithCompressLLM(clients ...LLMClient) Option {
	return func(c *managerConfig) { c.compressClients = clients }
}

// WithCooldownPolicy sets the cooldown policy applied to summarization models so
// the compaction path matches the main fallback chain. When unset, the built-in
// default policy is used. Ignored when WithCooldownTracker is also set.
func WithCooldownPolicy(p providers.CooldownPolicy) Option {
	return func(c *managerConfig) { c.cooldownPolicy = &p }
}

// WithCooldownTracker shares an existing cooldown tracker with the compaction
// path — pass the main fallback chain's tracker so a model parked by either path
// (e.g. an out-of-credits 402) is skipped by both. Takes precedence over
// WithCooldownPolicy.
func WithCooldownTracker(t *providers.CooldownTracker) Option {
	return func(c *managerConfig) { c.cooldownTracker = t }
}

func WithArchiveMessageCount(n int) Option {
	return func(c *managerConfig) { c.archiveMessageCount = n }
}

// WithArchiveDays limits the retrievable archive window to the last n days.
// 0 (the default) means no time-based limit.
func WithArchiveDays(n int) Option {
	return func(c *managerConfig) { c.archiveDays = n }
}

// WithSummaryMaxCount caps the number of stored summary checkpoints, keeping the
// newest n. 0 (the default) disables the count cap (falls back to days).
func WithSummaryMaxCount(n int) Option {
	return func(c *managerConfig) { c.summaryMaxCount = n }
}

// WithSummaryRetentionDays deletes stored summaries older than n days. 0 (the
// default) means no time-based limit.
func WithSummaryRetentionDays(n int) Option {
	return func(c *managerConfig) { c.summaryRetentionDays = n }
}

func WithContextWindow(tokens int) Option {
	return func(c *managerConfig) { c.contextWindow = tokens }
}

// WithArchiveDir sets the directory used to store per-session SQLite archive
// databases. The ContextManager derives the archive path as
// filepath.Join(dir, sanitizedKey+".archive.db") on first write.
// If dir is empty, archive writes are silently skipped.
func WithArchiveDir(dir string) Option {
	return func(c *managerConfig) { c.archiveDir = dir }
}

// WithOverheadTokens sets the fixed token overhead added to the post-Build token
// estimate in CheckAndCompress. This accounts for the system prompt, rendered
// summary, tool definitions, and completion budget combined. Default: 4000.
func WithOverheadTokens(n int) Option {
	return func(c *managerConfig) { c.overheadTokens = n }
}

// WithMaxSummaryTokens sets the maximum token budget for the serialized summary.
// If n == 0 (the default), the effective limit is 20% of contextWindow, computed
// at truncation time. After successful summarization the summary is truncated by
// removing the oldest key_moments and retrievable_history entries until it fits.
func WithMaxSummaryTokens(n int) Option {
	return func(c *managerConfig) { c.maxSummaryTokens = n }
}

func WithNotifyCallback(fn func(msg string)) Option {
	return func(c *managerConfig) { c.notifyCallback = fn }
}

// WithCompactionReporter sets the callback used by the automatic compaction path
// to deliver the formatted compaction report to the user's channel.
func WithCompactionReporter(fn func(channel, chatID, text string)) Option {
	return func(c *managerConfig) { c.reportCallback = fn }
}

// WithCompactDebug enables verbatim capture of each summarization request and
// response to <workspace>/compact.jsonl. Debugging only; off by default.
func WithCompactDebug(enabled bool) Option {
	return func(c *managerConfig) { c.compactDebug = enabled }
}

// WithCharsPerToken sets the divisor used to convert a rune count into an
// estimated token count. Lower values produce a higher (more conservative)
// token estimate. Values <= 0 are ignored and the default (4.0) is retained.
func WithCharsPerToken(v float64) Option {
	return func(c *managerConfig) {
		if v > 0 {
			c.charsPerToken = v
		}
	}
}

// WithTokenSafetyMargin sets the multiplier applied to every token estimate so
// it errs high, triggering compression earlier. A value of 1.1 inflates the
// estimate by 10%. Values <= 0 are ignored and the default (1.0) is retained.
func WithTokenSafetyMargin(v float64) Option {
	return func(c *managerConfig) {
		if v > 0 {
			c.tokenSafetyMargin = v
		}
	}
}

// WithArchiveContentMaxBytes sets the maximum per-message content size stored
// in the archive. Messages whose Content exceeds this are truncated before
// writing. Values <= 0 are ignored and the default (archiveContentMaxBytes) is
// used at write time.
func WithArchiveContentMaxBytes(n int) Option {
	return func(c *managerConfig) {
		if n > 0 {
			c.archiveContentMaxBytes = n
		}
	}
}

// WithCompressionProfileDir sets the agent workspace directory. If the file
// "COMPRESSION.md" (or legacy "compression.md") exists there it is appended verbatim to every summarization
// prompt, letting agents declare role-specific compression rules and structure.
func WithCompressionProfileDir(dir string) Option {
	return func(c *managerConfig) { c.compressionProfileDir = dir }
}

// WithCompressFailureDumpDir sets the logs/dumps directory to which the request
// and raw model response of each failed summarization attempt are written.
// Empty (the default) disables the dumps.
func WithCompressFailureDumpDir(dir string) Option {
	return func(c *managerConfig) { c.compressFailureDumpDir = dir }
}

// CompressionSettings is a read-only snapshot of the compaction knobs an Option
// set resolves to. It exists so callers that translate configuration into
// Options — and the tests that guard them — can verify what those Options
// actually say, without reaching into Manager's unexported state.
type CompressionSettings struct {
	MinPercent         int
	NormalPercent      int
	SafetyPercent      int
	TargetPercent      int
	MessageThreshold   int
	TriggerDays        int
	RetainTokenPercent int
	RetainMaxTokens    int
	RetainMaxAgeDays   int
	RetainMinMessages  int
	ContextWindow      int
	OverheadTokens     int
	CharsPerToken      float64
	TokenSafetyMargin  float64
}

// SettingsFromOptions applies opts over the package defaults and reports the
// result. It deliberately performs none of New()'s validation clamps: it answers
// "what did these options ask for", which is the question a configuration
// mapper needs answered. New() remains the only place policy is enforced.
func SettingsFromOptions(opts ...Option) CompressionSettings {
	cfg := defaultManagerConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return CompressionSettings{
		MinPercent:         cfg.minPercent,
		NormalPercent:      cfg.normalPercent,
		SafetyPercent:      cfg.safetyPercent,
		TargetPercent:      cfg.targetPercent,
		MessageThreshold:   cfg.messageThreshold,
		TriggerDays:        cfg.triggerDays,
		RetainTokenPercent: cfg.retainTokenPercent,
		RetainMaxTokens:    cfg.retainMaxTokens,
		RetainMaxAgeDays:   cfg.retainMaxAgeDays,
		RetainMinMessages:  cfg.retainMinMessages,
		ContextWindow:      cfg.contextWindow,
		OverheadTokens:     cfg.overheadTokens,
		CharsPerToken:      cfg.charsPerToken,
		TokenSafetyMargin:  cfg.tokenSafetyMargin,
	}
}
