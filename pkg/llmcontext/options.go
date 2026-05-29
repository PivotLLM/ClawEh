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
	notifyCallback      func(msg string)
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
