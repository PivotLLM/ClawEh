package config

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// CooldownConfig sets, per HTTP-status category, how long a model that keeps
// failing is taken out of rotation (the "settled" cooldown reached after the
// short 1/3/5-minute escalation on the first three consecutive failures). Each
// value is in MINUTES: 0 uses the built-in default; a negative value disables
// cooldown for that category (the model is never taken out for it). 413
// (context-too-large) and errors with no HTTP status never cool — they are
// per-request or transient.
type CooldownConfig struct {
	// BillingAuthMinutes covers HTTP 401, 402, 403 (auth / out-of-credits).
	BillingAuthMinutes int `json:"billing_auth_minutes,omitempty" env:"CLAW_COOLDOWN_BILLING_AUTH_MINUTES"`
	// RateLimitMinutes covers HTTP 429.
	RateLimitMinutes int `json:"rate_limit_minutes,omitempty" env:"CLAW_COOLDOWN_RATE_LIMIT_MINUTES"`
	// BadRequestMinutes covers HTTP 400.
	BadRequestMinutes int `json:"bad_request_minutes,omitempty" env:"CLAW_COOLDOWN_BAD_REQUEST_MINUTES"`
	// ClientErrorMinutes covers other 4xx (404, 408, …; not 400/401/402/403/429/413).
	ClientErrorMinutes int `json:"client_error_minutes,omitempty" env:"CLAW_COOLDOWN_CLIENT_ERROR_MINUTES"`
	// ServerErrorMinutes covers 5xx.
	ServerErrorMinutes int `json:"server_error_minutes,omitempty" env:"CLAW_COOLDOWN_SERVER_ERROR_MINUTES"`
}

// Cooldown category defaults (minutes). Billing/auth is the longest because the
// operator usually has to top up or rotate a key, but it is still bounded at 30
// minutes: it doubles as the recheck interval, so a longer value means a
// resolved billing problem goes unnoticed for that much longer. A rejected 402
// costs a round trip and no inference, so probing more often is cheap.
const (
	DefaultCooldownBillingAuthMinutes = 30
	DefaultCooldownRateLimitMinutes   = 10
	// DefaultCooldownBadRequestMinutes is 0 (never cool): a bad request is a
	// request-shape rejection, so the fallback should try the next candidate
	// (including a sibling config of the same provider+model, e.g. thinking-off)
	// instead of parking the model.
	DefaultCooldownBadRequestMinutes  = 0
	DefaultCooldownClientErrorMinutes = 10
	DefaultCooldownServerErrorMinutes = 10
)

// minutesOrDefault maps a config value to a duration: 0 → def, <0 → 0 (disabled).
func minutesOrDefault(v, def int) time.Duration {
	if v < 0 {
		return 0
	}
	if v == 0 {
		return time.Duration(def) * time.Minute
	}
	return time.Duration(v) * time.Minute
}

func (c CooldownConfig) BillingAuth() time.Duration {
	return minutesOrDefault(c.BillingAuthMinutes, DefaultCooldownBillingAuthMinutes)
}

func (c CooldownConfig) RateLimit() time.Duration {
	return minutesOrDefault(c.RateLimitMinutes, DefaultCooldownRateLimitMinutes)
}

func (c CooldownConfig) BadRequest() time.Duration {
	return minutesOrDefault(c.BadRequestMinutes, DefaultCooldownBadRequestMinutes)
}

func (c CooldownConfig) ClientError() time.Duration {
	return minutesOrDefault(c.ClientErrorMinutes, DefaultCooldownClientErrorMinutes)
}

func (c CooldownConfig) ServerError() time.Duration {
	return minutesOrDefault(c.ServerErrorMinutes, DefaultCooldownServerErrorMinutes)
}

// Provider is a named endpoint a model is reached through. It owns the wire
// protocol, the base URL, the credentials, and endpoint-scoped quirks. Models
// reference a provider by Name; the WebUI groups models by provider.
type Provider struct {
	Name string `json:"name"` // Unique identifier referenced by ModelConfig.Provider
	// Protocol is the wire format: openai-chat, openai-responses, azure,
	// anthropic, anthropic-messages, claude-cli, codex-cli, antigravity-cli,
	// cursor-cli. "anthropic" and "anthropic-messages" are the same thing —
	// Anthropic speaks one wire format — and both reach the Messages adapter.
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	// Endpoint-scoped openai-compat knobs.
	StrictCompat bool `json:"strict_compat,omitempty"`
	// RequireReasoningContent backfills a placeholder into reasoning_content on
	// assistant messages that carry none, for endpoints that reject history
	// missing the field. It is the exact opposite of StrictCompat, which strips
	// the field for endpoints that reject its presence — set one or the other,
	// never both.
	//
	// The case this exists for is DeepSeek V4 thinking mode: when a request
	// includes tools, DeepSeek requires reasoning_content on every assistant
	// message in history and answers 400 otherwise, even for turns that made no
	// tool call. Without the backfill, switching an agent from a CLI model (which
	// records no reasoning) to DeepSeek wedges the session — every turn replays
	// the same reasoning-less history and every turn is rejected.
	//
	// Only ever ADDS a placeholder where the field is empty; real reasoning is
	// passed through untouched.
	RequireReasoningContent bool `json:"require_reasoning_content,omitempty"`
	NoParallelToolCalls     bool `json:"no_parallel_tool_calls,omitempty"`
	ResponseFormatJSON      bool `json:"response_format_json,omitempty"`
	// Command overrides the binary path for CLI protocols (claude-cli, etc.).
	Command string `json:"command,omitempty"`
	// BypassRestrictions ("Allow CLI to bypass restrictions" in the WebUI) passes the
	// CLI's skip-permissions / sandbox-bypass flag on every invocation, so it
	// can run commands and edit files anywhere the service user can, without
	// asking. Off by default: the CLI then runs under its own permission
	// settings, and a tool call it cannot approve is refused. One setting per
	// CLI provider, shared by all of its models.
	BypassRestrictions bool `json:"bypass_restrictions,omitempty"`
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The model field uses protocol prefix format: [protocol/]model-identifier
// Supported protocols: openai, anthropic, claude-cli, codex-cli, antigravity-cli
// Default protocol is "openai" if no prefix is specified.
// Vision passthrough modes for ModelConfig.Vision.
const (
	VisionOff          = "off"           // default: tool images are not sent to the model
	VisionUserMessage  = "user_message"  // inject images as a follow-up user turn (Chat Completions)
	VisionToolResponse = "tool_response" // attach images to the tool result (Responses API only)
)

type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Model     string `json:"model"`      // Raw model id the endpoint expects (no claw protocol prefix)
	Provider  string `json:"provider"`   // Name of the Provider this model is reached through

	// Special providers (CLI-based, OAuth, etc.)
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM            int    `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokens      int    `json:"max_tokens,omitempty"`       // Maximum tokens per response; overrides agent defaults
	ContextWindow  int    `json:"context_window,omitempty"`   // Actual model context window size in tokens
	MaxTokensField string `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int    `json:"request_timeout,omitempty"`
	ThinkingLevel  string `json:"thinking_level,omitempty"` // Extended thinking: off|low|medium|high|xhigh|adaptive
	NoTools        bool   `json:"no_tools,omitempty"`       // When true, tools are not passed to this model
	// Vision controls how images returned by tools (e.g. MCP screenshots) reach
	// this model: "off"/"" (default) drops them; "user_message" injects them as a
	// follow-up user turn (works on Chat Completions, where tool messages are
	// text-only); "tool_response" attaches them to the tool result itself (only
	// valid on the Responses API, whose function_call_output accepts images).
	Vision    string            `json:"vision,omitempty"`
	ExtraArgs []string          `json:"extra_args,omitempty"` // Additional CLI arguments appended after required flags
	Env       map[string]string `json:"env,omitempty"`        // Environment variables for CLI-based providers (merged with os.Environ)
	Enabled   bool              `json:"enabled"`              // If false, model is skipped in all operations

	// ResponseLogFile, when non-empty, causes every raw HTTP response body from
	// the openai_compat provider to be appended to the given path. Diagnostic
	// feature only; no rotation, no expansion of ~ or env vars. Ignored by
	// providers other than openai_compat.
	ResponseLogFile string `json:"response_log_file,omitempty"`

	// ReasoningEffort sets the OpenAI-style reasoning_effort request field for
	// models that natively accept it (notably Grok). Valid values are "none",
	// "low", "medium", "high", or empty. Empty omits the field entirely; "none"
	// is sent explicitly (e.g. to disable reasoning on models that support it).
	// Providers that don't understand the field will silently ignore it.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// ExtraBody is a free-form passthrough map merged into the JSON request
	// body for OpenAI-compatible providers. Use it for per-provider knobs that
	// claw does not model natively. Keys colliding with claw-managed fields
	// (see reservedRequestBodyKeys) are rejected at config load.
	ExtraBody map[string]any `json:"extra_body,omitempty"`

	// DropParams lists top-level request-body fields to strip before sending to
	// OpenAI-compatible providers. Use it to suppress a parameter a model or
	// upstream rejects — e.g. "temperature" for OpenRouter reasoning models that
	// don't advertise it and would 404 under provider.require_parameters.
	// Stripping is applied last (after extra_body), so it always wins. It is a
	// literal filter: listing structural fields like "messages" or "model" will
	// break the request. Ignored by providers other than openai_compat.
	DropParams []string `json:"drop_params,omitempty"`

	// StrictAlternation rewrites the outbound message list for chat-only models
	// that require strict user/assistant alternation and reject system/tool roles
	// (e.g. Gemma on some gateways): the system prompt is folded into the first
	// user turn, tool results become user turns, and consecutive same-role
	// messages are merged. It is model-scoped because models on the same endpoint
	// differ (Gemma needs it; Claude/Nova don't). Pair with no_tools, since
	// tool_calls are dropped. Ignored by providers other than openai_compat.
	StrictAlternation bool `json:"strict_alternation,omitempty"`
}

// reservedRequestBodyKeys lists the JSON request fields owned by claw's own
// request builder. ExtraBody entries colliding with these keys are rejected at
// config load — the collision guard there is what guarantees the merge step in
// the request builder cannot overwrite a claw-managed field.
var reservedRequestBodyKeys = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"stream":                {},
	"tools":                 {},
	"tool_choice":           {},
	"parallel_tool_calls":   {},
	"reasoning_effort":      {},
	"temperature":           {},
	"max_tokens":            {},
	"max_completion_tokens": {},
	"top_p":                 {},
	"n":                     {},
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return errors.New("model_name is required")
	}
	if c.Model == "" {
		return errors.New("model is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("model %q: provider is required", c.ModelName)
	}
	switch c.ReasoningEffort {
	case "", "none", "low", "medium", "high":
		// ok
	default:
		return fmt.Errorf(
			"model %q: invalid reasoning_effort %q (valid: none, low, medium, high, or omit)",
			c.ModelName, c.ReasoningEffort,
		)
	}
	for k := range c.ExtraBody {
		if _, reserved := reservedRequestBodyKeys[k]; reserved {
			return fmt.Errorf(
				"model %q: extra_body key %q collides with claw-managed request field",
				c.ModelName, k,
			)
		}
	}
	return nil
}

// HasCLIProvider reports whether any enabled model in ModelList uses a
// *-cli protocol (claude-cli, codex-cli, gemini-cli). Those CLIs rely on
// the MCP host to call claw's tools natively.
func (c *Config) HasCLIProvider() bool {
	for i := range c.Models {
		if !c.Models[i].Enabled {
			continue
		}
		prov, err := c.GetProvider(c.Models[i].Provider)
		if err != nil {
			continue
		}
		if IsCLIProtocol(prov.Protocol) {
			return true
		}
	}
	return false
}

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in models or providers", modelName)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := rrCounter.Add(1) % uint64(len(matches))
	return &matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []ModelConfig {
	var matches []ModelConfig
	for i := range c.Models {
		if c.Models[i].ModelName == modelName && c.Models[i].Enabled {
			matches = append(matches, c.Models[i])
		}
	}
	return matches
}

// validProtocols is the set of wire protocols a Provider may declare. Each maps
// to an internal provider implementation in providers.
var validProtocols = map[string]struct{}{
	"openai-chat":        {},
	"openai-responses":   {},
	"azure":              {},
	"anthropic":          {},
	"anthropic-messages": {},
	"claude-cli":         {},
	"codex-cli":          {},
	"antigravity-cli":    {},
	// Retained alias: Google deprecated the Gemini CLI in favour of
	// Antigravity, and a released config naming this must keep validating.
	"gemini-cli": {},
	"cursor-cli": {},
	// A model on it represents a person: see human.go.
	HumanProtocol: {},
}

// httpProtocols are the protocols that require a base_url.
var httpProtocols = map[string]struct{}{
	"openai-chat":        {},
	"openai-responses":   {},
	"azure":              {},
	"anthropic":          {},
	"anthropic-messages": {},
}

// IsCLIProtocol reports whether the protocol is a subprocess CLI provider,
// which authenticates out-of-band and needs no API key.
func IsCLIProtocol(protocol string) bool {
	switch protocol {
	// "gemini-cli" is retained as an alias for "antigravity-cli": Google
	// deprecated the Gemini CLI, and a config still naming it must keep
	// starting the MCP host, or its CLI would silently lose every claw tool.
	case "claude-cli", "codex-cli", "antigravity-cli", "gemini-cli", "cursor-cli":
		return true
	default:
		return false
	}
}

// HasCredentials reports whether this provider carries enough to authenticate:
// CLI providers always qualify (they auth out-of-band), and so does the human
// protocol, which reaches a person and needs none; HTTP providers need an API
// key.
func (p *Provider) HasCredentials() bool {
	if IsCLIProtocol(p.Protocol) || IsHumanProtocol(p.Protocol) {
		return true
	}
	return p.APIKey != ""
}

// GetProvider resolves a provider by name. The lookup is case-sensitive on the
// configured Name.
func (c *Config) GetProvider(name string) (*Provider, error) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("provider %q not found", name)
}

// FindProviderByProtocol returns the first provider declaring the given
// protocol, or nil. Used by flows that target a wire family rather than a
// specific named endpoint (e.g. OAuth login attaching to the anthropic
// provider).
func (c *Config) FindProviderByProtocol(protocol string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Protocol == protocol {
			return &c.Providers[i]
		}
	}
	return nil
}

// ValidateProviders checks that provider names are unique and non-empty, each
// protocol is recognised, and HTTP protocols carry a base_url.
func (c *Config) ValidateProviders() error {
	seen := make(map[string]struct{}, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("providers[%d]: name is required", i)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("providers[%d]: duplicate provider name %q", i, p.Name)
		}
		seen[p.Name] = struct{}{}
		if _, ok := validProtocols[p.Protocol]; !ok {
			return fmt.Errorf("provider %q: unknown protocol %q", p.Name, p.Protocol)
		}
		if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url is required for protocol %q", p.Name, p.Protocol)
		}
	}
	return nil
}

// ValidateModels validates all ModelConfig entries in the models,
// including that each model's provider reference resolves to a configured
// provider. Multiple entries with the same model_name are allowed for load
// balancing.
func (c *Config) ValidateModels() error {
	for i := range c.Models {
		if err := c.Models[i].Validate(); err != nil {
			return fmt.Errorf("models[%d]: %w", i, err)
		}
		if _, err := c.GetProvider(c.Models[i].Provider); err != nil {
			return fmt.Errorf("models[%d] (%q): %w", i, c.Models[i].ModelName, err)
		}
	}
	return nil
}

// RenameModelReferences repoints every reference to a model alias from oldName
// to newName across agent defaults, per-agent model chains, image models, and
// the global summarization chain. Used when a model's
// model_name changes via the WebUI so existing references are not orphaned.
// No-op when oldName is empty or unchanged. Mutates in-memory config only.
func (c *Config) RenameModelReferences(oldName, newName string) {
	if oldName == "" || oldName == newName {
		return
	}
	for _, site := range c.modelRefSites() {
		if site.slice != nil {
			renameInSlice(*site.slice, oldName, newName)
		} else {
			*site.scalar = renameScalar(*site.scalar, oldName, newName)
		}
	}
}

func renameScalar(s, oldName, newName string) string {
	if s == oldName {
		return newName
	}
	return s
}

func renameInSlice(ss []string, oldName, newName string) {
	for i := range ss {
		if ss[i] == oldName {
			ss[i] = newName
		}
	}
}

// ValidateProvider validates a single provider (the one at idx) without
// rejecting the whole config because OTHER providers are invalid. It checks the
// provider's own name, protocol, and base_url, plus that its name does not
// collide with another provider. The per-provider WebUI endpoints use this so an
// operator can repair one entry at a time (e.g. during a protocol migration)
// even while other entries remain invalid.
func (c *Config) ValidateProvider(idx int) error {
	if idx < 0 || idx >= len(c.Providers) {
		return fmt.Errorf("provider index %d out of range", idx)
	}
	p := &c.Providers[idx]
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("provider name is required")
	}
	for i := range c.Providers {
		if i != idx && c.Providers[i].Name == p.Name {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
	}
	if _, ok := validProtocols[p.Protocol]; !ok {
		return fmt.Errorf("provider %q: unknown protocol %q", p.Name, p.Protocol)
	}
	if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required for protocol %q", p.Name, p.Protocol)
	}
	return nil
}

// PruneInvalid removes providers and models that fail validation, logging a WARN
// for each dropped entry, and returns how many of each were removed. It is the
// lenient counterpart to ValidateProviders/ValidateModels: the gateway calls it
// at startup so a single bad entry (e.g. a stale/unknown protocol, or a model
// pointing at a missing provider) degrades gracefully instead of aborting the
// whole process. It mutates the in-memory config only — it never writes to disk.
func (c *Config) PruneInvalid() (droppedProviders, droppedModels int) {
	seen := make(map[string]struct{}, len(c.Providers))
	valid := make(map[string]struct{}, len(c.Providers))
	keptProviders := make([]Provider, 0, len(c.Providers))
	for i := range c.Providers {
		p := c.Providers[i]
		var reason string
		if strings.TrimSpace(p.Name) == "" {
			reason = "name is required"
		} else if _, dup := seen[p.Name]; dup {
			reason = "duplicate provider name"
		} else if _, ok := validProtocols[p.Protocol]; !ok {
			reason = fmt.Sprintf("unknown protocol %q", p.Protocol)
		} else if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
			reason = fmt.Sprintf("base_url is required for protocol %q", p.Protocol)
		}
		if reason != "" {
			logger.WarnCF("config", "ignoring invalid provider", map[string]any{
				"provider": p.Name,
				"reason":   reason,
			})
			droppedProviders++
			continue
		}
		seen[p.Name] = struct{}{}
		valid[p.Name] = struct{}{}
		keptProviders = append(keptProviders, p)
	}
	c.Providers = keptProviders

	keptModels := make([]ModelConfig, 0, len(c.Models))
	for i := range c.Models {
		m := c.Models[i]
		if err := m.Validate(); err != nil {
			logger.WarnCF("config", "ignoring invalid model", map[string]any{
				"model":  m.ModelName,
				"reason": err.Error(),
			})
			droppedModels++
			continue
		}
		if _, ok := valid[m.Provider]; !ok {
			logger.WarnCF("config", "ignoring model with unknown provider", map[string]any{
				"model":    m.ModelName,
				"provider": m.Provider,
			})
			droppedModels++
			continue
		}
		keptModels = append(keptModels, m)
	}
	c.Models = keptModels
	return droppedProviders, droppedModels
}

func MergeAPIKeys(apiKey string, apiKeys []string) []string {
	seen := make(map[string]struct{})
	var all []string

	if k := strings.TrimSpace(apiKey); k != "" {
		if _, exists := seen[k]; !exists {
			seen[k] = struct{}{}
			all = append(all, k)
		}
	}

	for _, k := range apiKeys {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			if _, exists := seen[trimmed]; !exists {
				seen[trimmed] = struct{}{}
				all = append(all, trimmed)
			}
		}
	}

	return all
}
