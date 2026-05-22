# Context Summarization Improvements

## Background

The current summarization strategy has two failure modes:

1. **Anchor loss** — founding instructions (playbook config, sampled values, initial goal) are
   exactly what gets summarized away. The prose summary captures narrative but loses precise
   parameters. The last-4 tail gives recent exchanges but no anchor to the current task.

2. **Cron noise** — in cron-driven sessions, the retained tail is consumed by identical ping/response
   pairs that contain no informational value, crowding out meaningful messages.

This document defines the improvements to address both issues.

---

## 1. pkg/llmcontext Package

Context management is extracted into a dedicated `pkg/llmcontext` package. This package owns the
full lifecycle of a session's conversational context: storage coordination, context building,
compression triggers, and statistics.

### Interface

The `ContextManager` interface is defined in `pkg/llmcontext`. Consumers (the agent loop and
other callers) import `pkg/llmcontext` directly and depend on the interface, not on any concrete
type. `pkg/global` remains constants-only.

All compression is **synchronous**. `AddUserMessage` and `AddAssistantMessage` block until any
triggered compression completes before returning.

```go
type ContextManager interface {
    AddUserMessage(ctx context.Context, msg providers.Message) error
    AddAssistantMessage(ctx context.Context, msg providers.Message) error
    SetSystemPrompt(prompt string)
    Build(ctx context.Context) ([]providers.Message, error)
    ForceCompress(ctx context.Context) error
    Stats() ContextStats
    Reset() error
}

type ContextStats struct {
    TotalMessages       int
    MeaningfulMessages  int
    EstimatedTokens     int
    ContextWindowPct    float64
    LastCompressedAt    time.Time
    LastCompressionGain float64 // 0.0–1.0, fraction of tokens removed
    CompressionCooling  bool
    CoolingSinceCount   int
}
```

### LLMClient interface

```go
// LLMClient is the interface pkg/llmcontext uses to call an LLM for summarization.
// Implemented by each provider adapter; the agent passes its normal client (or a
// compression-specific client) at construction time.
type LLMClient interface {
    Complete(ctx context.Context, messages []providers.Message) (providers.Message, error)
}
```

### ModelChain

`ModelChain` is defined within `pkg/llmcontext` to avoid importing `pkg/config`:

```go
type ModelChain struct {
    Primary   string
    Fallbacks []string
}
```

### Constructor and functional options

All defaults are defined as unexported constants within `pkg/llmcontext`. Configuration is
applied via the functional options pattern — callers pass only the options they have explicit
values for; everything else uses the package default.

```go
func New(sessionKey string, store session.Store, llm LLMClient, opts ...Option) ContextManager

type Option func(*config)

func WithMinPercent(pct int) Option
func WithNormalPercent(pct int) Option
func WithSafetyPercent(pct int) Option
func WithMessageThreshold(n int) Option
func WithRetainTokenPercent(pct int) Option
func WithRetainMinMessages(n int) Option
func WithCompressModel(model ModelChain) Option    // records chain for stats/logging only
func WithCompressLLM(clients ...LLMClient) Option  // actual clients used by compress()
func WithArchiveMessageCount(n int) Option
func WithContextWindow(tokens int) Option
func WithNotifyCallback(fn func(msg string)) Option
```

`WithCompressModel` records which model chain is configured (for stats and logging).
`WithCompressLLM` provides the callable clients: the agent layer resolves `ModelChain` →
`[]LLMClient` and passes them in order. If `WithCompressLLM` is not set, the `llm` passed to
`New()` is used for compression.

Calling code resolves options in precedence order (per-agent → agent defaults → nothing) and
passes only the options that have an explicit configured value:

```go
var opts []llmcontext.Option
if agentCfg.CompressNormalPercent != nil {
    opts = append(opts, llmcontext.WithNormalPercent(*agentCfg.CompressNormalPercent))
} else if defaults.CompressNormalPercent != 0 {
    opts = append(opts, llmcontext.WithNormalPercent(defaults.CompressNormalPercent))
}
// ... repeat for each option
mgr := llmcontext.New(sessionKey, store, llm, opts...)
```

If no option is passed for a value, the `pkg/llmcontext` default is used.

### Internal defaults (pkg/llmcontext only)

```go
const (
    defaultMinPercent             = 20   // floor: no compression below this context %
    defaultNormalPercent          = 50   // normal compression trigger
    defaultSafetyPercent          = 80   // safety net trigger
    defaultMessageThreshold       = 20   // meaningful message count trigger
    defaultRetainTokenPercent     = 20   // tail token budget
    defaultRetainMinMessages      = 2    // minimum retained turns
    defaultMinCompressionGain     = 0.05 // 5% — overall gain below this enters cooldown
    defaultCooldownMessages       = 5    // messages to wait after ineffective compression
    defaultLargeMsgOffset         = 20   // safety% - this% = individual message size threshold
    defaultArchiveMessageCount    = 100
    defaultCompressTargetFactor   = 0.5  // target = normalPercent * this (default: 25%)
    defaultMinLoopGain            = 0.10 // 10% — per-iteration gain below this stops the loop
    defaultMaxCompressIterations  = 3    // maximum loop iterations per compression pass
)
```

`largeMsgThreshold = safetyPercent - defaultLargeMsgOffset` (default: 60%). Any individual
message exceeding this percentage of the context window is subject to size handling (see §5).

`compressTarget = normalPercent × defaultCompressTargetFactor` (default: 25%). Both normal
and safety net compression aim for this target.

### Relationship to existing packages

```
pkg/agent/loop.go  →  pkg/llmcontext  →  pkg/session / pkg/memory  (storage)
                                       →  ContextBuilder logic (moved in from pkg/agent/context.go)
```

**Import cycle note:** `pkg/llmcontext` imports `pkg/providers` (for `providers.Message`), and
`pkg/providers` imports `pkg/config` (for factory/dispatch). Therefore `pkg/config` cannot
import `pkg/llmcontext`. The `compress_model` config field uses `AgentModelConfig` (the existing
type in `pkg/config`, structurally identical to `ModelChain`). `pkg/agent/instance.go` converts
`AgentModelConfig` → `llmcontext.ModelChain` when building the options slice.

The `ContextBuilder` in `pkg/agent/context.go` moves into `pkg/llmcontext`. The agent loop
replaces direct session store and ContextBuilder calls with the `ContextManager` interface.

---

## 2. Compression Thresholds

Three configurable thresholds govern when compression fires:

| Threshold | Default | Config field | Role |
|---|---|---|---|
| Floor (min) | 20% | `compress_min_percent` | Below this, no compression fires at all |
| Normal | 50% | `compress_normal_percent` | Triggers regular compression |
| Safety net | 80% | `compress_safety_percent` | Triggers safety net compression |

The message-count trigger (`compress_message_threshold`, default 20) fires normal compression
when the meaningful message count reaches the threshold — but only if `context_pct ≥ minPercent`.
All four can be configured in `AgentDefaults` and per-agent `AgentConfig`. Zero-value semantics
differ by field type: percent thresholds (min, normal, safety) with a value of 0 are replaced by
their package default at construction time and cannot be disabled; the count threshold with a
value of 0 disables the count trigger.

**Validation at instance creation (applied in order):**
1. Any percent threshold (min, normal, safety) that is zero is replaced by its default value.
2. If safety percent ≤ normal percent: emit WARN. No auto-correction — the configuration is
   used as-is; in this state the safety net will never fire.
3. If min percent ≥ normal percent: emit WARN, set min percent to `normalPercent / 2`.

---

## 3. Message Sequence Numbers (Breaking Change)

Every stored message gains a monotonically increasing integer sequence number (`seq`), starting
at 1 for each session. Seq is a storage-layer concern: it is not a field in `providers.Message`
and does not appear in raw message payloads sent to LLM providers. It surfaces to the LLM only
via the structured summary (§6), where it serves as a retrieval reference.

### Storage format

```json
{"seq": 42, "role": "user", "content": "..."}
```

This is a breaking change to the JSONL format.

### Durable sequence state

`next_seq` is stored in `meta.json` and incremented on every message write. It is never derived
from `count` or line position — compaction rewrites the JSONL and resets `count` to active
messages, which would cause seq duplication if seq were position-derived.

### Migration

Existing JSONL files without `seq` are handled at read time: lines missing `seq` are assigned
sequence numbers based on position plus skip offset. `next_seq` is set to `(line count + 1)` on
first read of a legacy file. No rewrite required.

### Reset behaviour

On session reset: `next_seq` resets to 1, JSONL cleared, archive cleared, summary cleared.

---

## 4. Noise Classification and Meaningful Count

### Classifier

A message is classified as **noise** if:

1. It is a cron-wrapper message whose payload (after stripping the template prefix) is
   byte-for-byte identical to the payload of the most recent previous cron-wrapper message in
   this session (not necessarily adjacent); OR
2. Its full content is byte-for-byte identical to the most recent previous message of the same
   role in this session (not necessarily adjacent).

The classifier caches the last written message per role and last written cron-wrapper payload
so comparison requires no additional reads.

Length alone is never a criterion. Short user messages ("yes", "no", "approve", "use port 3000")
are never classified as noise.

### Meaningful count

`meaningful_count` is stored in `meta.json` and incremented at write time for every non-noise
message. The message-count trigger uses `meaningful_count`, not raw `count`.

### Filtering at compaction

Physical removal happens only at compaction time. Consecutive noise messages collapse to at most
one (the most recent).

### Logging

The message-stored log event gains `seq`, `length`, and `counted` (true if meaningful) fields.

---

## 5. Compression Flow

### Unified trigger

The same trigger logic runs both after `AddAssistantMessage` (post-turn) and inside
`AddUserMessage` before the message is stored (pre-turn). Since compression only affects
summarized history — the retained tail is untouched — the pending message is never altered by
a normal compression pass.

```
estimate current token count → compute context_pct

if context_pct < minPercent:
    return  // floor: no compression regardless of other triggers

if context_pct ≥ safetyPercent:
    compress(ctx, safetyNet=true)
    return

countTriggered = (messageThreshold > 0 AND
                  meaningful_count - compressedAtMeaningfulCount >= messageThreshold)

if (context_pct ≥ normalPercent OR countTriggered) AND NOT cooling:
    compress(ctx, safetyNet=false)
```

Cooldown suppresses both percent-triggered and count-triggered normal compression, but never
suppresses safety net compression. The safety net check precedes the count/normal check so that
a session at 85% is never routed to normal compression.

### compress(ctx, safetyNet bool)

A single function handles all compression. Compression is synchronous — callers block until
it completes. Prompts for standard and aggressive variants are defined as unexported string
constants in `pkg/llmcontext`.

The `WithNotifyCallback` option provides a function called at the start of compression and
again on completion (success or failure): `"Context compression in progress…"` and
`"Context compression complete."` (or `"Context compression failed: …"` on error). The agent
uses this to relay status to the user.

**Shared loop logic (both paths):**

```
tokens_before_compress = estimate current token count  // overall baseline for cooldown
target    = normalPercent × defaultCompressTargetFactor  // e.g. 25%
iteration = 0
prompt    = standard

loop:
    tokens_before_iteration = estimate current token count
    try full LLM chain with current prompt
    tokens_after_iteration = estimate current token count
    gain = (tokens_before_iteration - tokens_after_iteration) / tokens_before_iteration

    if tokens_after_iteration / contextWindow < target → done (success)
    if gain ≥ defaultMinLoopGain AND iteration < defaultMaxCompressIterations:
        iteration++
        loop again with same prompt
    else if prompt == standard AND still above normalPercent:
        switch to aggressive prompt, reset iteration = 0, loop again
    else:
        break  // exhausted

tokens_after_compress = estimate current token count
overallGain = (tokens_before_compress - tokens_after_compress) / tokens_before_compress
```

`defaultMaxCompressIterations` applies **per prompt step** (standard and aggressive each have
their own iteration budget). Total maximum attempts is `2 × defaultMaxCompressIterations`.

The loop continues as long as meaningful progress is being made (≥ `defaultMinLoopGain` per
iteration, default 10%) and the per-step iteration cap has not been reached. When progress
stalls while still above the normal threshold, the prompt escalates from standard to aggressive
before giving up. Each prompt step gets a fresh attempt across the full LLM chain.

**Normal (safetyNet=false):**

- Run shared loop.
- If all clients fail on any iteration: treat gain as 0; the loop's normal escalation handles
  the rest. After both prompt steps are exhausted: emit WARN, return nil. Normal compression
  never blocks.
- After the loop: if still ≥ normalPercent, enter cooldown.
- Never drops messages.

**Safety net (safetyNet=true):**

- Run shared loop (same target and escalation logic).
- If all clients fail on any iteration: treat gain as 0; the loop's normal escalation handles
  the rest (escalate to aggressive if on standard, break if on aggressive).
- After the loop: if still ≥ safetyPercent, proceed to message dropping (step 3). If between
  normalPercent and safetyPercent, stop — the normal trigger handles the rest on the next turn.

Step 3 — Drop messages: drop oldest turn groups until context falls below safetyPercent or only
the retained tail remains. Log at WARN.

After step 3, perform **individual message size checks** across all retained messages:

- Any retained message whose individual token count exceeds `largeMsgThreshold`
  (`safetyPercent - defaultLargeMsgOffset`, default 60%): truncate, append
  `[**TRUNCATED DUE TO SIZE**]`.
- If the most recent message exceeds `safetyPercent` on its own:
  - **User message (pre-turn):** return an error — content is too large for the context window.
  - **LLM message (post-turn):** truncate to fit, append marker.

### Compression effectiveness and cooldown

```
overallGain = (tokens_before_compress - tokens_after_compress) / tokens_before_compress
```

On any completion of `compress()` (normal or safety net): update
`compressedAtMeaningfulCount = meaningful_count`. This resets the count trigger window.

After a normal path compression:
- If `overallGain < defaultMinCompressionGain` (5%) AND still ≥ normalPercent: set
  `CompressionCooling = true`, record `coolingSinceCount = meaningful_count`.
- While cooling: skip normal trigger until
  `meaningful_count - coolingSinceCount ≥ defaultCooldownMessages`.
- Safety net always runs regardless of cooling state.

After a successful safety net compression: clear `CompressionCooling`.

---

## 6. Structured Summarization

The current prose summary is replaced with structured JSON stored in `meta.json` and rendered
as Markdown for system prompt injection.

### Internal JSON structure

```json
{
  "version": 1,
  "state": {
    "goals": "...",
    "progress": "...",
    "pending": "...",
    "constraints": "..."
  },
  "key_moments": [
    {"seq": 3, "role": "user", "summary": "...", "exact": "verbatim text if needed"}
  ],
  "message_index": [
    {"seq_start": 12, "seq_end": 12, "role": "user", "label": "confirmed output format"},
    {"seq_start": 15, "seq_end": 28, "role": "assistant", "label": "14 hourly self-checks, no changes"}
  ],
  "covered_seq_start": 1,
  "covered_seq_end": 41,
  "generated_at": "2026-05-13T10:00:00Z",
  "model": "claude-opus-4-7"
}
```

Schema-validated after each LLM response; retried once on schema failure; second failure treated
as a compression failure (see §9 failure handling).

The `covered_seq_start` and `covered_seq_end` fields are populated by the calling code from the
actual message seq range being summarized — they are not generated by the LLM.

If the provider supports JSON mode (structured output), the summarization call uses it to reduce
schema validation failures. Providers that do not support JSON mode receive only the standard
prompt.

### State: Goals/Progress and Constraints

**Goals/Progress** — fully dynamic. The summarizer updates this to reflect only current active
goals. Completed or superseded goals are retired. For long-lived agents (running months), the
current task is authoritative; founding tasks are retired once complete. Key Moments captures
transitions explicitly (e.g., *"Pivoted from email triage to pentest oversight on [date]"*).

**Pending** — immediate next actions in flight at compression time: tasks started but not
complete, things awaiting a response, or next steps the agent was about to take. Answers the
question "what was I about to do?" after a compression hiatus. Example: *"Awaiting user
approval to proceed to exploitation phase; draft report saved to /tmp/report.md."* Cleared or
updated each compression cycle as pending items are completed or superseded.

**Constraints** — semi-persistent. Rules and preferences that survive task changes: communication
style, tool restrictions, persistent user preferences, standing commitments, safety rules.
Preserved verbatim unless the user explicitly changes them.

### Key Moments

Curated, LLM-selected entries of high importance. Exact wording preserved for instructions,
decisions, and configuration values. Routine exchanges omitted or collapsed.

### Message Index (injected subset)

Only messages within the current archive window (`archive_min_seq`–`archive_max_seq`) appear in
the injected index. Consecutive identical entries collapse to a range. A complete index is
stored on disk but not injected wholesale.

### Rendered Markdown for injection

```
## Current State
**Goals:** ...
**Progress:** ...
**Pending:** ...
**Constraints:** ...

## Key Moments
- [#3] user: exact: "use stratified sampling"

## Retrievable History (use mcp__claw__get_session_messages to fetch full content)
- [#12] user: confirmed output format
- [#15–#28] assistant: 14 hourly self-checks, no changes
```

### Prompts

Two prompt variants are defined as unexported string constants in `pkg/llmcontext`:

- **Standard** (`promptStandard`): instructs the LLM to produce the three-section structured
  output; update Goals/Progress dynamically; update Pending with current in-flight actions;
  preserve Constraints; use exact wording for Key Moments; index only archive-resident
  messages; collapse consecutive identical entries.

- **Aggressive** (`promptAggressive`): all instructions from the standard prompt, plus: *"The
  context is at its size limit. Produce the most compact valid output possible. Omit all
  non-essential Key Moments. Collapse aggressively. Be terse."*

### Batch summarization

When the estimated token count of the messages to summarize plus the current accumulated summary
exceeds the compression LLM's context window, the input is split into batches. Batches are
processed oldest-first, with each batch receiving the accumulated summary from prior batches as
context. Prompt: *"This is the summary of all prior messages. Incorporate the following new
messages as if you had been summarizing incrementally."*

The final stored Summary's `covered_seq_start` is the seq of the first message in the first
batch; `covered_seq_end` is the seq of the last message in the last batch.

### Messages to summarize

The summarization input is all stored messages from `last_covered_seq_end + 1` (or seq 1 on
the first compression cycle) up to and including the oldest message not in the retained tail.
Messages in the retained tail are excluded and remain in the live context unchanged.

### Context injection

The rendered Markdown replaces the current `CONTEXT_SUMMARY:` block at the same injection point.

---

## 7. Message Archive and Retrieval Tool

### Archive

A `{key}.archive.jsonl` file holds messages in full `StoredMessage` format. At each compression
cycle the archive is rewritten to retain only the most recent N messages. Sessions that never
trigger compression accumulate the archive without bound; this is intentional since such sessions
have low data volume.

- Default cap: `defaultArchiveMessageCount = 100` (constant in `pkg/llmcontext`)
- Configurable via `WithArchiveMessageCount` option
- Cap is enforced at compression time only
- `meta.json` tracks `archive_min_seq` and `archive_max_seq`
- On session reset: archive cleared with all other session state

### MCP retrieval tool

Registered as `mcp__claw__get_session_messages`, following the same `agent_token` pattern as
all other claw MCP tools. Session scope is a known gap addressed in a follow-on change.

**Parameters:** `agent_token` (required); `seq` (int) or `seq_start`/`seq_end` (int range).

**Returns:** full message content. Out-of-window seqs return
`"not available — message has aged out of the archive"`.

**Design note:** retrieval is optional. State and Key Moments must be self-sufficient. Agents
with restricted tool sets must operate without this tool.

---

## 8. Tail Retention

Token-budget approach replacing fixed last-4:

- `retainTokenPercent` (default 20%): percentage of context window for the retained tail,
  after subtracting the estimated summary and system prompt size.
- `retainMinMessages` (default 2): minimum meaningful turns always retained regardless of budget.

**Turn-group integrity:** retention operates on whole turns (assistant message + its tool result
messages). Groups are kept whole or dropped whole.

**Noise in tail:** consecutive noise messages collapsed to at most one (most recent) per run.

---

## 9. Compression LLM and Failure Handling

### Model selection

`WithCompressModel(ModelChain{...})` records which model chain is configured for stats and
logging. `WithCompressLLM(clients ...LLMClient)` provides the callable clients the Manager
actually uses for compression. The agent layer is responsible for resolving `ModelChain` →
`[]LLMClient` using the existing provider factory, then passing them via `WithCompressLLM`.
If `WithCompressLLM` is not set, the `llm` passed to `New()` is used for both normal turns
and compression.

On any error (including context-too-large), the Manager moves to the next client in the slice.
Each prompt step (standard, aggressive) gets a fresh attempt across the full client slice —
a client that failed on the standard step is retried on the aggressive step.

### Failure handling

**Normal path (safetyNet=false):** if all models fail, emit WARN and return nil. The turn
proceeds; compression will retry on the next trigger.

**Safety net path (safetyNet=true):** if all models fail steps 1 and 2 (standard and aggressive
prompts), proceed directly to step 3 (drop oldest messages). Dropping does not require an LLM.

**If an existing summary is present when summarization fails:** retain the stale summary without
regenerating it; proceed to step 3 (drop oldest groups) if still ≥ safetyPercent. The stale
summary remains in `meta.json` and is included in the next `Build()` call. Emit WARN with
`stale_summary: true`.

**If no summary exists and summarization fails:** skip truncation; proceed to step 3 directly if
still ≥ safetyPercent. Emit WARN with `no_summary: true`.

---

## 10. Configuration Schema Changes

### pkg/llmcontext (all compression defaults — not in pkg/global)

All compression threshold defaults and internal constants are defined within `pkg/llmcontext`
and are not exported to other packages. See §1 for the full constant list.

### pkg/global (new constants)

No new compression constants. All compression defaults live in `pkg/llmcontext`.

### meta.json (new fields)

| Field | Description |
|---|---|
| `next_seq` | Next sequence number to assign |
| `meaningful_count` | Cumulative count of non-noise messages |
| `compressed_at_meaningful_count` | Value of `meaningful_count` at last compression; used to reset the count trigger window |
| `archive_min_seq` | Lowest seq currently in archive |
| `archive_max_seq` | Highest seq currently in archive |
| `summary` | JSON-serialized `Summary` struct; empty string if no summary yet |
| `summary_generated_at` | Timestamp of last successful summarization |
| `summary_model` | Model used for last summarization |
| `compression_cooling` | True if last compression gain was below minimum |
| `cooling_since_count` | `meaningful_count` when cooling began |

### Migration of existing config fields

The existing `SummarizeMessageThreshold` (default 20) and `SummarizeTokenPercent` (default 75)
fields in `AgentDefaults` and their constants in `pkg/config/defaults.go` are **removed and
replaced** by the new fields below. No deprecation period — this is unreleased code. The old
hardcoded fallback values in `pkg/agent/instance.go` (20 and 75) are also removed.

### AgentDefaults (new fields)

| Field | Type | Description |
|---|---|---|
| `compress_min_percent` | int | Context floor; no compression fires below this; 0 = use default |
| `compress_normal_percent` | int | Normal compression trigger; 0 = not configured, use pkg/llmcontext default |
| `compress_safety_percent` | int | Safety net trigger; 0 = not configured, use pkg/llmcontext default |
| `compress_message_threshold` | int | Message count trigger; 0 = not configured, use pkg/llmcontext default |
| `compress_retain_token_percent` | int | % of context for retained tail; 0 = use default |
| `compress_retain_min_messages` | int | Minimum meaningful turns retained; 0 = use default |
| `compress_model` | ModelChain | LLM chain for compression; zero value = use agent model. JSON: `{"primary": "model-id", "fallbacks": ["model-id"]}` |
| `archive_message_count` | int | Max messages in session archive (enforced at compression time); 0 = use default |

**Zero-value semantics for `AgentDefaults` int fields:** 0 means "not configured — use
pkg/llmcontext default." `AgentDefaults` cannot explicitly disable a trigger; only per-agent
`AgentConfig` pointer fields can do that.

### AgentConfig (new fields, override defaults when set)

Same fields as `AgentDefaults`. Integer fields use pointer types (`*int`):
- `nil` = not set, fall through to `AgentDefaults` then `pkg/llmcontext` default
- non-nil with value 0 = explicitly disabled for the count trigger (`CompressMessageThreshold`
  only); percent thresholds set to 0 are replaced by their package default at construction time
- non-nil with value > 0 = explicit override

---

## 11. Web GUI Updates

### Configuration

Form controls for all new `AgentDefaults` and `AgentConfig` fields. The compression model
field uses the same primary + fallbacks UI pattern as the existing model field. Zero-disable
behaviour explained inline for threshold fields.

### Session state panel

Read-only panel in the agent/session view for operator debugging:

- State: Goals/Progress, Pending, and Constraints subsections
- Key Moments timeline
- Retrievable Message Index with seq numbers and archive window range
- `ContextStats`: token percent, cooling state, last compression gain
- Last summarization timestamp and model

---

## 12. What Is Not Changing

- Messages written to disk append-only; filtering never at write time.
- `forceCompression` **moves from `pkg/agent/loop.go` into `pkg/llmcontext`** as the
  implementation of `ForceCompress` on the `ContextManager` interface. The behavior — final
  safety net for hard context limit hits during a live turn — is preserved.
- `providers.Message` struct unchanged; seq is storage-layer only.
- `.meta.json` extended, not replaced. `summary` field now holds JSON; existing prose coexists
  until each session's next compression cycle.
