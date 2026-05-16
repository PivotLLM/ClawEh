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
	defaultArchiveMessageCount   = 250
	defaultCompressTargetFactor  = 0.5
	defaultMinLoopGain           = 0.10
	defaultMaxCompressIterations = 3
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
	contextWindow       int
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

func WithContextWindow(tokens int) Option {
	return func(c *managerConfig) { c.contextWindow = tokens }
}

func WithNotifyCallback(fn func(msg string)) Option {
	return func(c *managerConfig) { c.notifyCallback = fn }
}
