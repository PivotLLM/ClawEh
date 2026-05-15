# Context Summarization — Implementation Plan

Reference design: `docs/design/context-summarization-improvements.md`

Phases are ordered by dependency. After Phase 2 completes, Phases 3 and 7 can start
immediately. Phases 4 and 5 can proceed in parallel after Phase 3. Phase 6 requires Phases 4
and 5. Phase 9 is last.

---

## Phase 0 — pkg/llmcontext Package Skeleton

**Files:** new `pkg/llmcontext/` package

**Work:**

1. Define `ContextManager` interface (including `ForceCompress(ctx context.Context) error`),
   `ContextStats` struct, `ModelChain` struct, and `LLMClient` interface as specified in design §1.

2. Define all `Option` types and `With*` constructor functions including `WithMinPercent`,
   `WithCompressModel`, `WithCompressLLM`, and `WithNotifyCallback`.
   Define all internal default constants including `defaultMinPercent = 20` (see §1 internal
   defaults table).

3. Create a concrete `Manager` type implementing `ContextManager`. At this phase it is a thin
   wrapper: `AddUserMessage` and `AddAssistantMessage` delegate to the existing session store;
   `Build` delegates to the existing `ContextBuilder`; `Stats` returns zero values.

4. Constructor: `New(sessionKey string, store session.Store, llm LLMClient, opts ...Option) ContextManager`.
   Apply options over defaults at construction time; validate in order:
   (a) any percent threshold that is zero is replaced by its default value;
   (b) safety ≤ normal → emit WARN (no correction);
   (c) min ≥ normal → emit WARN, set min = normalPercent/2.

5. Update the agent loop to instantiate a `ContextManager` per session using the functional
   options pattern. Resolve per-agent and default config values into options before calling
   `New`. If `CompressModel` is configured, resolve it to `[]LLMClient` using the existing
   provider factory and pass via `WithCompressLLM`; also pass `WithCompressModel` for logging.
   The agent loop no longer calls the session store or `ContextBuilder` directly.

6. Move `pkg/agent/context.go` logic into `pkg/llmcontext`. `ContextBuilder` is no longer
   exported from the agent package.

7. Migrate `forceCompression` from `pkg/agent/loop.go` into `pkg/llmcontext` as the
   implementation of `ForceCompress`. The agent loop's context-too-large error handler calls
   `mgr.ForceCompress(ctx)` in place of the old `al.forceCompression(...)` call. Remove
   `forceCompression` from `pkg/agent/loop.go`.

**Verification:** `go build ./...`, all existing tests pass, behaviour unchanged.

---

## Phase 1 — Config Schema

**Files:** `pkg/config/config.go`, `pkg/config/defaults.go`, `pkg/agent/instance.go`

No new constants are added to `pkg/global`. All compression defaults live in `pkg/llmcontext`
(Phase 0). Config fields are optional overrides; zero/nil means "use the llmcontext default."

**Work:**

1. **Remove** the following existing fields (unreleased code — no deprecation):
   - `AgentDefaults.SummarizeMessageThreshold` and its constant in `defaults.go` (was 20)
   - `AgentDefaults.SummarizeTokenPercent` and its constant in `defaults.go` (was 75)

2. Add to `AgentDefaults` in `config.go`:
   - `CompressMinPercent int`
   - `CompressNormalPercent int`
   - `CompressSafetyPercent int`
   - `CompressMessageThreshold int`
   - `CompressRetainTokenPercent int`
   - `CompressRetainMinMessages int`
   - `CompressModel llmcontext.ModelChain`
   - `ArchiveMessageCount int`

   For int fields: 0 means "not configured — use pkg/llmcontext default." AgentDefaults cannot
   explicitly disable a trigger; only per-agent AgentConfig pointer fields can.

3. Add to `AgentConfig` in `config.go` (pointer types — nil means unset, not zero):
   - `CompressMinPercent *int`
   - `CompressNormalPercent *int`
   - `CompressSafetyPercent *int`
   - `CompressMessageThreshold *int`
   - `CompressRetainTokenPercent *int`
   - `CompressRetainMinMessages *int`
   - `CompressModel *llmcontext.ModelChain`
   - `ArchiveMessageCount *int`

4. Update `agent/instance.go`:
   - Remove the hardcoded fallback values (20, 75) from summarization threshold handling.
   - Build the `[]llmcontext.Option` slice by checking per-agent then defaults for each field;
     only append an option when an explicit value is configured.
   - Threshold validation (zero percent → default, safety ≤ normal WARN only, min ≥ normal
     WARN+clamp) is handled inside `llmcontext.New` — no validation needed in instance.go.

**Verification:** `go build ./...`, existing tests pass.

---

## Phase 2 — Storage Layer

**Files:** `pkg/memory/jsonl.go`, `pkg/session/jsonl_backend.go`

This phase makes breaking changes to the JSONL format. All subsequent phases depend on it.

### 2a — StoredMessage and sequence numbers

1. Define `StoredMessage` in `pkg/memory`:
   ```go
   type StoredMessage struct {
       Seq int `json:"seq"`
       providers.Message
   }
   ```

2. Update all JSONL read/write paths to use `StoredMessage`.

3. Migration at read time: lines missing `seq` (or `seq == 0`) are assigned seq from line
   position plus skip offset. `next_seq` in meta is set to `(line count + 1)` on first
   encounter of a legacy file.

### 2b — Extended meta.json

Add to `sessionMeta`:
- `NextSeq int`
- `MeaningfulCount int`
- `CompressedAtMeaningfulCount int` — value of MeaningfulCount at last compression; count trigger fires when `MeaningfulCount - CompressedAtMeaningfulCount >= messageThreshold`
- `ArchiveMinSeq int`
- `ArchiveMaxSeq int`
- `SummaryGeneratedAt time.Time`
- `SummaryModel string`
- `CompressionCooling bool`
- `CoolingSinceCount int`

### 2c — Noise classifier

Implement `isNoise(msg StoredMessage, cache *noiseCache) bool` in `pkg/memory`:
- Cache holds: last written message per role, last written cron-wrapper payload.
- True if content is byte-for-byte identical to the cached previous message of the same role.
- For cron-wrapper messages: strip the wrapper, compare payloads against the cached cron payload.
- Length is never a criterion.
- Update cache after each write.

### 2d — Write-time meaningful_count

In `AddMessage()`:
- Run the noise classifier.
- Increment `NextSeq` always; increment `MeaningfulCount` only if not noise.
- Emit `message_stored` INFO log with `seq`, `length`, `counted` fields.

`CompressedAtMeaningfulCount` is updated by `compress()` (Phase 6), not at write time.

### 2e — Archive file

1. Maintain `{key}.archive.jsonl` in `StoredMessage` format.
2. On `AddMessage()`: append to archive.
3. Archive cap enforced at summarization/compaction time (not per-write): rewrite archive
   keeping the last N messages; update `ArchiveMinSeq` and `ArchiveMaxSeq` in meta.
4. On session reset: delete archive file, clear archive meta fields.

**Verification:** `go build ./...`; unit tests for seq assignment stability across write →
compact → read cycles; migration of legacy files; noise classifier coverage.

---

## Phase 3 — Unified Trigger (stub)

**Files:** `pkg/llmcontext/manager.go`

**Work:**

1. Implement the unified trigger check in `AddUserMessage` and `AddAssistantMessage`:
   - Estimate token count; compute `context_pct`.
   - If `context_pct < minPercent`: return (floor — no compression regardless of other triggers).
   - If `context_pct ≥ safetyPercent`: call `compress(ctx, true)` and return.
   - Check `countTriggered = (messageThreshold > 0 AND meaningful_count - compressedAtMeaningfulCount >= messageThreshold)`.
   - If (`context_pct ≥ normalPercent` OR `countTriggered`) AND NOT cooling: call
     `compress(ctx, false)`.
   - Cooldown suppresses both percent and count triggers; never suppresses safety net.

2. Implement a **stub** `compress(ctx context.Context, safetyNet bool) error` that logs at
   INFO (`"compression triggered"`, `safetyNet`, `context_pct`), updates
   `CompressedAtMeaningfulCount = MeaningfulCount` in meta (so the count trigger window resets
   correctly even in stub mode), and returns nil.
   The stub exists so the trigger can be tested and wired before full compress() is built.

**Verification:** unit tests for trigger boundary conditions (below normal, between thresholds,
above safety, cooling suppression, message count trigger, cooling never blocks safety net).

---

## Phase 4 — Structured Summarization

**Files:** new `pkg/llmcontext/summary.go`, `pkg/llmcontext/manager.go`

### 4a — Summary schema

Define Go types (`Summary`, `SummaryState`, `KeyMoment`, `IndexEntry`) as per design §6.
Include:
- `Validate() error` — checks required fields, valid version, covered_seq consistency.
- `Render() string` — produces the Markdown block for system prompt injection.

### 4b — Summary injection into Build()

In `Build()`: retrieve stored summary from meta; attempt unmarshal as `Summary`. If valid,
call `Render()` and inject the Markdown at the `CONTEXT_SUMMARY` position. If unmarshal fails
(legacy prose or empty), inject as-is. The `CONTEXT_SUMMARY:` prefix already exists in
`pkg/agent/context.go` — no template changes required.

### 4c — Prompt construction

Write `buildSummarizationPrompt(existing *Summary, messages []StoredMessage, archiveMin,
archiveMax int, aggressive bool) string`:
- Standard variant: structured output instructions, Goals/Progress dynamic, Pending updated
  with current in-flight actions, Constraints semi-persistent, archive-only Message Index.
- Aggressive variant: adds tightness instruction for safety path Step 2.
- If the provider supports JSON mode, the caller enables it on the request.
- `covered_seq_start` and `covered_seq_end` are set by the calling code from the actual message
  seq range; the LLM is not asked to produce these fields.

### 4d — Schema validation and retry

After LLM response: unmarshal → validate → retry once with correction prompt on failure →
on second failure, treat as summarization failure.

### 4e — Batch order and split

Process batches oldest-first. Split when estimated token count of pending messages plus current
accumulated summary exceeds the compression model's context window. Each batch receives the
accumulated summary as context. The final stored Summary's `covered_seq_start` is the first seq
of the first batch; `covered_seq_end` is the last seq of the last batch.

### 4f — Storage

Serialize `Summary` to JSON string; store in `meta.json` `summary` field. Update
`SummaryGeneratedAt` and `SummaryModel` in meta on success.

On read: if `summary` does not unmarshal as `Summary`, wrap as legacy prose in
`State.Goals` until next cycle.

**Verification:** unit tests for schema validation, Markdown rendering, legacy migration,
oldest-first accumulation, aggressive vs standard prompt selection, covered_seq set by code
not LLM, summary injection in Build().

---

## Phase 5 — Tail Retention

**Files:** `pkg/llmcontext/manager.go`

**Work:**

1. Replace fixed last-4 count with token-budget selection:
   - Budget = `(ContextWindow * CompressRetainTokenPercent / 100)` minus estimated summary
     and system prompt tokens.
   - Walk history newest-to-oldest in **turn groups**. When a tool result message is
     encountered, scan backward to find the assistant message whose `ToolCalls` match
     the result's `ToolCallID`; treat all messages between them as one atomic group.
     Include groups that fit within the budget; stop when budget is exhausted.
   - Always include at least `CompressRetainMinMessages` meaningful turns.

2. After selection, collapse consecutive noise messages in the tail to at most one.

**Verification:** unit tests for budget-only retention, minimum-floor override, tool-call
group integrity (kept whole or dropped whole), noise collapse in tail.

---

## Phase 6 — Full compress() and LLM Chain

**Files:** `pkg/llmcontext/compress.go`, `pkg/llmcontext/manager.go`

Replaces the Phase 3 stub. Requires Phase 4 (summarization) and Phase 5 (tail retention).

**Work:**

1. At construction time, store the compression client slice from `WithCompressLLM`. If not
   provided, use the `llm` passed to `New()` as the sole compression client. Store on the
   `Manager`. `WithCompressModel` metadata (for logging) is stored separately.

2. Implement the full `compress(ctx context.Context, safetyNet bool) error`:

   **Shared loop (both paths):**
   - `tokens_before_compress` = estimate token count before starting (overall baseline).
   - `target = normalPercent × defaultCompressTargetFactor`.
   - Start with `promptStandard`; `defaultMaxCompressIterations` applies per prompt step.
   - Track `tokens_before_iteration` at the start of each iteration.
   - When progress stalls above `normalPercent`, escalate to `promptAggressive` and reset
     iteration count to 0.
   - Each iteration: fresh attempt across the full LLM chain (a model that failed on
     standard is retried on aggressive).

   **Normal (safetyNet=false):**
   - Run shared loop.
   - All clients fail on any iteration → treat gain as 0, let loop logic handle escalation.
     After both prompt steps exhausted: emit WARN, return nil (never blocks).
   - After loop: compute `overallGain` from `tokens_before_compress`. If < `defaultMinCompressionGain`
     AND still ≥ normalPercent → set `CompressionCooling = true`, record `CoolingSinceCount`.

   **Safety net (safetyNet=true):**
   - Run shared loop. All clients fail on any iteration → treat gain as 0, let loop escalate
     naturally (standard → aggressive → break).
   - After loop: if still ≥ safetyPercent → step 3 (drop messages); if between normalPercent
     and safetyPercent → stop.
   - On success (context drops below safetyPercent): clear `CompressionCooling`.
   - Step 3 — Drop oldest turn groups until below safetyPercent or only retained tail remains.
     Log at WARN.
   - After step 3: individual message size checks across all retained messages:
     - Any message > `largeMsgThreshold` → truncate, append `[**TRUNCATED DUE TO SIZE**]`.
     - Most recent message > `safetyPercent` on its own:
       user (pre-turn) → return error; LLM (post-turn) → truncate with marker.

3. Failure handling:
   - Safety net, all models fail, existing summary present: retain stale summary as-is; proceed
     to step 3 if still ≥ safetyPercent. Emit WARN `stale_summary: true`.
   - Safety net, all models fail, no summary: proceed to step 3 directly.
     Emit WARN `no_summary: true`.
   - Normal path, all models fail: emit WARN, return nil.

4. On any completion of `compress()` (regardless of path or outcome): update
   `CompressedAtMeaningfulCount = MeaningfulCount` in meta. This resets the count trigger window.

5. Call the `notifyCallback` (if set) at the start of compress() and on completion.

**Verification:** unit tests for compress_model chain (primary success, fallback success, all
fail), normal-path all-fail (nil return), safety-path all-fail (→ drop), stale-summary
truncation, no-summary skip, loop iterates while progress, escalates to aggressive, stops on
low gain, notification callback called on start and completion.

---

## Phase 7 — MCP Retrieval Tool

**Files:** new `pkg/tools/session_history.go`, agent tool registration

**Work:**

1. Implement `get_session_messages` as a `tools.Tool` following the existing claw tool pattern:
   - Parameters: `agent_token` (required, standard pattern), `seq` (int) or
     `seq_start`/`seq_end` (int range). If `seq` is provided it takes precedence;
     `seq_start`/`seq_end` are ignored.
   - Read from `{key}.archive.jsonl` by seq number.
   - Return full message content; for out-of-window seqs return
     `"not available — message has aged out of the archive"`.
   - Session scoping is a known gap; to be resolved in a follow-on change.

2. Register in each agent's tool registry at startup, following the existing registration
   pattern for claw tools.

**Verification:** unit tests for valid single seq, valid range, below-min (not available),
above-max (not available).

---

## Phase 8 — Web GUI

**Files:** `web/frontend/` (settings and session view components)

**Work:**

1. **Configuration forms** — controls for all new `AgentDefaults` and `AgentConfig` fields:
   - Min, normal, and safety compress percent (with inline explanation of zero → default behavior)
   - Message threshold, retain percent, retain min messages
   - Compression model (primary + fallbacks, same UI as existing model field)
   - Archive message count

2. **Session state panel** — read-only panel in agent/session view:
   - State: Goals, Progress, Pending, and Constraints subsections
   - Key Moments timeline
   - Retrievable Message Index with seq numbers and archive window range
   - `ContextStats`: token percent, cooling state, last compression gain
   - Last summarization timestamp and model

**Verification:** `pnpm run build:backend`; all new fields round-trip through save/load;
session state panel renders correctly for structured, legacy prose, and empty summary states.

---

## Dependency Summary

```
Phase 0 (llmcontext skeleton + forceCompression migration)
    └── Phase 1 (config schema + field migration)
            └── Phase 2 (storage layer)
                    ├── Phase 3 (trigger check + compress() stub)
                    │       ├── Phase 4 (structured summarization + Build() injection)
                    │       ├── Phase 5 (tail retention)
                    │       └── Phase 6 (full compress() + LLM chain) ← requires 4 and 5
                    └── Phase 7 (MCP retrieval tool)
Phase 8 (GUI) — after all others
```

Phases 4 and 5 can be developed in parallel after Phase 3. Phase 7 is independent of Phases
4–6 and can proceed after Phase 2.

---

## Known Gaps (follow-on changes)

- **Session scoping for `get_session_messages`:** the MCP tool currently identifies the agent
  from the token but not the specific session. This affects multiple tools and will be addressed
  in a separate change.
