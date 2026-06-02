// ClawEh
// License: MIT

package llmcontext

// Option is a functional option for configuring a Manager.
type Option func(*managerConfig)

const (
	defaultMinPercent            = 20
	defaultNormalPercent         = 50
	defaultSafetyPercent         = 80
	defaultMessageThreshold      = 20
	defaultRetainTokenPercent    = 20
	defaultRetainMinMessages     = 2
	defaultMinCompressionGain    = 0.05
	defaultCooldownMessages      = 5
	defaultLargeMsgOffset        = 20
	defaultArchiveMessageCount   = 5000
	defaultCompressTargetFactor  = 0.5
	defaultMinLoopGain           = 0.10
	defaultMaxCompressIterations = 3
	defaultOverheadTokens        = 4000
	defaultCharsPerToken         = 4.0
	defaultTokenSafetyMargin     = 1.0

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
	minPercent          int
	normalPercent       int
	safetyPercent       int
	messageThreshold    int
	retainTokenPercent  int
	retainMinMessages   int
	compressModel       ModelChain
	compressClients     []LLMClient
	archiveMessageCount int
	archiveDays         int
	archiveDir          string
	contextWindow       int
	overheadTokens      int
	maxSummaryTokens    int // 0 = use 20% of contextWindow at truncation time
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
	// a file named "compression.md" exists there, its content is appended to the
	// summarization prompt so agents can declare role-specific compression rules.
	compressionProfileDir string
}

func defaultManagerConfig() managerConfig {
	return managerConfig{
		minPercent:          defaultMinPercent,
		normalPercent:       defaultNormalPercent,
		safetyPercent:       defaultSafetyPercent,
		messageThreshold:    defaultMessageThreshold,
		retainTokenPercent:  defaultRetainTokenPercent,
		retainMinMessages:   defaultRetainMinMessages,
		archiveMessageCount: defaultArchiveMessageCount,
		contextWindow:       128000,
		overheadTokens:      defaultOverheadTokens,
		charsPerToken:       defaultCharsPerToken,
		tokenSafetyMargin:   defaultTokenSafetyMargin,
	}
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

func WithArchiveMessageCount(n int) Option {
	return func(c *managerConfig) { c.archiveMessageCount = n }
}

// WithArchiveDays limits the retrievable archive window to the last n days.
// 0 (the default) means no time-based limit.
func WithArchiveDays(n int) Option {
	return func(c *managerConfig) { c.archiveDays = n }
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
// "compression.md" exists there it is appended verbatim to every summarization
// prompt, letting agents declare role-specific compression rules and structure.
func WithCompressionProfileDir(dir string) Option {
	return func(c *managerConfig) { c.compressionProfileDir = dir }
}
