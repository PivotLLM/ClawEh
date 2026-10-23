package config

// SummarizationConfig is the global, deployment-wide summarization model chain.
// Models are tried in order for context compaction across all agents; each entry
// is a models alias (or a raw protocol/model string). The agent's own
// primary model is always appended as a final last-resort fallback at runtime.
// An empty Models list means summarization runs against each agent's own model.
type SummarizationConfig struct {
	Models []string `json:"models,omitempty"`
	// DebugCapture, when true, appends the verbatim request and response of every
	// summarization LLM invocation to <agent-workspace>/compact.jsonl. Debugging
	// only; off by default.
	DebugCapture bool `json:"debug_capture,omitempty" env:"CLAW_SUMMARIZATION_DEBUG_CAPTURE"`
}

// MemoryConfig configures the cognitive-memory subsystem for an agent. The
// subsystem is ACTIVE for an agent only when that agent is allowed the cogmem
// tools (there is no separate engine flag). The consolidation model is NOT
// configured here: it reuses the agent's summarization ("Memory") model chain
// (SummarizationModels → global Summarization.Models → the agent's own model).
type MemoryConfig struct {
	Prompt        MemoryPromptConfig        `json:"prompt"`
	Consolidation MemoryConsolidationConfig `json:"consolidation"`
	Retention     MemoryRetentionConfig     `json:"retention"`
	Export        MemoryExportConfig        `json:"export"`
}

// MemoryPromptConfig tunes per-turn prompt composition.
type MemoryPromptConfig struct {
	TopKDomains       int     `json:"top_k_domains"`
	MaxChars          int     `json:"max_chars"`
	MinConfidence     float64 `json:"min_confidence"`
	IncludeDebugTrace bool    `json:"include_debug_trace"`
	// Budgets for markdown files attached to memories (memory.file_ref). These
	// are separate from MaxChars: an attached document is injected whole, not
	// squeezed into the routed block's line budget.
	FileMaxBytes      int `json:"file_max_bytes"`       // per attachment
	FileTotalMaxBytes int `json:"file_total_max_bytes"` // all attachments in one turn
}

// MemoryConsolidationConfig tunes the background sleep cycle.
type MemoryConsolidationConfig struct {
	EveryNMessages   int    `json:"every_n_messages"`
	IdleMinutes      int    `json:"idle_minutes"`
	Nightly          bool   `json:"nightly"`
	NightlyAt        string `json:"nightly_at"`
	ProposeDomains   bool   `json:"propose_domains"`
	AutoPromote      bool   `json:"auto_promote"`
	DebugDump        bool   `json:"debug_dump"`
	MaxBatchMessages int    `json:"max_batch_messages"`
	MaxInputTokens   int    `json:"max_input_tokens"`
	PerMessageChars  int    `json:"per_message_chars"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	MaxRuntimeSecs   int    `json:"max_runtime_seconds"`
}

// MemoryRetentionConfig bounds how long transient memory rows are kept.
type MemoryRetentionConfig struct {
	// EventDays is how long an `event` memory is kept before it is deleted.
	// Events are things that happened at a point in time — a trip, a delivery,
	// a scheduled run — and they stop being useful long before they stop
	// accumulating: one agent recorded an hourly "nothing changed" note and
	// reached 300 of them.
	//
	// 0 uses DefaultEventRetentionDays; -1 keeps them forever. Only `event`
	// memories are ever deleted by age — a fact, preference, rule or
	// operational note is permanent, so no policy here can silently drop a
	// standing instruction.
	EventDays int `json:"event_days,omitempty" env:"CLAW_MEMORY_RETENTION_EVENT_DAYS"`

	// RetiredDays is how long a retired memory is kept before it is deleted.
	// Retiring takes a memory out of use but leaves the row, so a store that
	// retires steadily grows forever while showing nothing for it.
	//
	// 0 uses DefaultRetiredRetentionDays; -1 keeps them forever. Measured from
	// when the memory was retired, not when it was created.
	RetiredDays int `json:"retired_days,omitempty" env:"CLAW_MEMORY_RETENTION_RETIRED_DAYS"`
}

// Retention defaults. Deliberately not configurable: they are the meaning of an
// unset field, and a default that can itself be changed is one more thing to
// reason about when a memory disappears.
const (
	DefaultEventRetentionDays   = 30
	DefaultRetiredRetentionDays = 90
)

// EffectiveEventDays resolves EventDays: 0 means the default, negative means
// never expire (reported as 0 days, which callers treat as "no sweep").
func (r MemoryRetentionConfig) EffectiveEventDays() int {
	return resolveRetention(r.EventDays, DefaultEventRetentionDays)
}

// EffectiveRetiredDays resolves RetiredDays the same way.
func (r MemoryRetentionConfig) EffectiveRetiredDays() int {
	return resolveRetention(r.RetiredDays, DefaultRetiredRetentionDays)
}

func resolveRetention(v, def int) int {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0 // never expire
	default:
		return v
	}
}

// MemoryExportConfig controls the read-only GENERATED_*.md export.
type MemoryExportConfig struct {
	Enabled bool `json:"enabled"`
}

// CompressionConfig is the context-compaction policy for an agent, split into
// the three things it actually controls. The flat compress_* keys it replaces
// mixed all three under one prefix, which is how compress_message_threshold came
// to be documented as firing "regardless of context %" when the percentage floor
// gated it all along.
//
//   - Trigger: WHEN a compaction runs.
//   - Retain:  WHAT survives it.
//   - Estimate: how token cost is measured, which every threshold is relative to.
//
// Every field is a pointer so a per-agent block overrides the defaults block
// field by field, and so 0 can mean "off" rather than "unset" — the plain-int
// form could not express disabling a trigger at the defaults level.
type CompressionConfig struct {
	Trigger  *CompressionTriggerConfig  `json:"trigger,omitempty"`
	Retain   *CompressionRetainConfig   `json:"retain,omitempty"`
	Estimate *CompressionEstimateConfig `json:"estimate,omitempty"`

	// TargetPercent is the compaction loop's stop condition: keep summarizing
	// until the live window is below this percentage of the context window. It
	// belongs to neither group — it is not a trigger and not a retention bound.
	// nil derives it from Trigger.NormalPercent, as the code always has.
	TargetPercent *int `json:"target_percent,omitempty"`
}

// CompressionTriggerConfig decides when a compaction runs. The percentage and
// count triggers are gated by MinPercent; Days is not, because a low-volume
// session never reaches any percentage and its history would otherwise sit in
// the window indefinitely.
type CompressionTriggerConfig struct {
	// MinPercent is the floor below which the percentage and count triggers
	// never fire. nil => 20.
	MinPercent *int `json:"min_percent,omitempty"`
	// NormalPercent is the routine compaction threshold. nil => 50.
	NormalPercent *int `json:"normal_percent,omitempty"`
	// SafetyPercent is the emergency threshold, checked mid-turn as well as at
	// the turn boundary, and not gated by MinPercent. nil => 80.
	SafetyPercent *int `json:"safety_percent,omitempty"`
	// MessageCount fires after this many new messages since the last compaction.
	// nil => 100, 0 => off.
	MessageCount *int `json:"message_count,omitempty"`
	// Days fires once the oldest message in the live window is older than this,
	// bypassing MinPercent. Set it HIGHER than Retain.MaxAgeDays: the gap is what
	// stops the session pinning to the trigger boundary and re-compacting on
	// every message. nil => 7, 0 => off.
	Days *int `json:"days,omitempty"`
}

// CompressionRetainConfig decides what survives a compaction. Every bound here
// is subject to MinMessages and to the rule that the most recent user message is
// never archived.
type CompressionRetainConfig struct {
	// TokenPercent is the retained tail budget as a percentage of the context
	// window. Must be clearly below Trigger.MinPercent. nil => 10.
	TokenPercent *int `json:"token_percent,omitempty"`
	// MaxTokens is an absolute ceiling on the retained tail, applied alongside
	// TokenPercent (the smaller wins). Percentages scale with the window, so a
	// million-token model otherwise inherits a budget tuned for 128k.
	// nil/0 => no absolute cap.
	MaxTokens *int `json:"max_tokens,omitempty"`
	// MaxAgeDays caps the age of the retained tail; anything older is
	// summarized. nil => 5, 0 => off.
	MaxAgeDays *int `json:"max_age_days,omitempty"`
	// MinMessages is the floor that overrides every other bound. nil => 2.
	MinMessages *int `json:"min_messages,omitempty"`
}

// CompressionEstimateConfig tunes how token cost is estimated from text. Every
// percentage threshold is relative to this, so it is the highest-leverage and
// most dangerous block to change.
type CompressionEstimateConfig struct {
	// CharsPerToken is the divisor turning runes into tokens. Lower estimates
	// more tokens per character (more conservative). nil => 4.0.
	CharsPerToken *float64 `json:"chars_per_token,omitempty"`
	// TokenSafetyMargin multiplies the estimate so it errs high. nil => 1.0.
	TokenSafetyMargin *float64 `json:"token_safety_margin,omitempty"`
}

// EffectiveCompression returns the compaction policy for an agent: the defaults
// block with any per-agent block overlaid field by field. Either may be nil.
func (a *AgentConfig) EffectiveCompression(defaults *CompressionConfig) *CompressionConfig {
	out := &CompressionConfig{}
	out.overlay(defaults)
	if a != nil {
		out.overlay(a.Compression)
	}
	return out
}

// overlay merges src into c, leaving fields src does not set untouched.
func (c *CompressionConfig) overlay(src *CompressionConfig) {
	if src == nil {
		return
	}
	if src.TargetPercent != nil {
		c.TargetPercent = src.TargetPercent
	}
	if src.Trigger != nil {
		if c.Trigger == nil {
			c.Trigger = &CompressionTriggerConfig{}
		}
		t, s := c.Trigger, src.Trigger
		if s.MinPercent != nil {
			t.MinPercent = s.MinPercent
		}
		if s.NormalPercent != nil {
			t.NormalPercent = s.NormalPercent
		}
		if s.SafetyPercent != nil {
			t.SafetyPercent = s.SafetyPercent
		}
		if s.MessageCount != nil {
			t.MessageCount = s.MessageCount
		}
		if s.Days != nil {
			t.Days = s.Days
		}
	}
	if src.Retain != nil {
		if c.Retain == nil {
			c.Retain = &CompressionRetainConfig{}
		}
		r, s := c.Retain, src.Retain
		if s.TokenPercent != nil {
			r.TokenPercent = s.TokenPercent
		}
		if s.MaxTokens != nil {
			r.MaxTokens = s.MaxTokens
		}
		if s.MaxAgeDays != nil {
			r.MaxAgeDays = s.MaxAgeDays
		}
		if s.MinMessages != nil {
			r.MinMessages = s.MinMessages
		}
	}
	if src.Estimate != nil {
		if c.Estimate == nil {
			c.Estimate = &CompressionEstimateConfig{}
		}
		e, s := c.Estimate, src.Estimate
		if s.CharsPerToken != nil {
			e.CharsPerToken = s.CharsPerToken
		}
		if s.TokenSafetyMargin != nil {
			e.TokenSafetyMargin = s.TokenSafetyMargin
		}
	}
}

// ContextEvictionConfig controls the per-turn, LLM-free eviction sweep that
// collapses re-retrievable tool results (file reads, web fetches) in the live
// window to a placeholder so long sessions rarely trigger summarization
// compaction. All fields are pointers so a per-agent block overrides the
// defaults block field by field; an unset field falls back to the built-in
// default (see ctxengine.DefaultEvictionPolicy).
type ContextEvictionConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`       // nil => enabled
	ProtectTurns *int  `json:"protect_turns,omitempty"` // nil => 3
	EvictTurns   *int  `json:"evict_turns,omitempty"`   // nil => 10
	BudgetBytes  *int  `json:"budget_bytes,omitempty"`  // nil => derived (~40% of window)
	NotifyUser   *bool `json:"notify_user,omitempty"`   // nil => off
	// ArgBytes is the size at which an aged-out tool-call ARGUMENT (a file_write
	// body, an edit's replacement text) is replaced by a placeholder. Arguments
	// are counted in full by the token estimator but live on the assistant
	// message, so the reader sweep never reached them. nil => 1024, 0 => off.
	ArgBytes *int `json:"arg_bytes,omitempty"`
}

// CognitiveMemoryEnabled reports whether the cognitive-memory suite + subsystem
// (tools, prompt injection, archive hook, consolidation) is active for this
// agent. It is the per-agent `cogmem` toggle, defaulting ON: nil or true ⇒
// enabled; false ⇒ disabled. (Previously keyed off the per-tool allowlist; it is
// now an all-or-nothing suite gated as a unit.)
func (a *AgentConfig) CognitiveMemoryEnabled() bool {
	if a == nil {
		return false
	}
	return a.Cogmem == nil || *a.Cogmem
}

// EffectiveMemory returns the memory config for an agent: the per-agent block
// if present, otherwise the defaults.
func (d *AgentDefaults) EffectiveMemory(a *AgentConfig) MemoryConfig {
	mem := d.Memory
	if a != nil && a.Memory != nil {
		mem = *a.Memory
	}
	// Per-agent retention is layered on afterwards, so an agent can shorten its
	// own window without taking over the whole memory block.
	if a != nil {
		if a.EventRetentionDays != nil {
			mem.Retention.EventDays = *a.EventRetentionDays
		}
		if a.RetiredRetentionDays != nil {
			mem.Retention.RetiredDays = *a.RetiredRetentionDays
		}
	}
	return mem
}
