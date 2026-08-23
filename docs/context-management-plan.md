# Context management: findings and plan

Branch: `feature/context-management`.

Two separate problems, found while investigating "a compaction covered more than two
weeks of chat and still left over 100 messages":

1. **Compaction never removed old messages** — retention was purely token-budgeted,
   and the budget was never reached. *(implemented, tests passing)*
2. **Prompt caching is broken for every HTTP provider** — a minute-granularity
   timestamp sits ahead of the conversation history in the request, so the cached
   prefix ends before the history begins. *(not started)*

Problem 2 is worth more than problem 1: measured over 31 days of production logs it
accounts for ~48% of amber's and ~61% of wendy's full-price input tokens.

---

## Part 1 — Compaction (implemented)

### Root causes confirmed against production data

| # | Finding | Evidence |
|---|---|---|
| 1 | Tool-call **arguments** were never evictable. The reader sweep only rewrote `role=tool` content; a `file_write` body lives in the assistant message's `ToolCalls` and is counted in full by the estimator. | 45% of wendy's live window was `file_write` arguments |
| 2 | **No age-based trigger or retention** anywhere. `selectTail` retained by token budget only, and `providers.Message` carries no timestamp. | sam: 88 messages spanning 49 days, never compacted |
| 3 | `retain_token_percent` (20) **equalled** `min_percent` (20), so every pass shaved back to exactly the floor and re-fired on the next message. | 29 of 32 production compactions fired at 20.0–22.9% |
| 4 | The **estimator ignored `reasoning_content`** and `ResponsesReasoning`, both replayed to the provider on every turn. | 103 KB — 49% — of wendy's window was uncounted |
| 5 | Three trigger paths measured three different things; the primary one (turn boundary) counted the least. | `triggerCheck` used history only, no reserve, no tool schemas |

### Changes made

- **Estimator** counts `ReasoningContent` and `ResponsesReasoning`; media charged a flat
  per-item figure. `SystemParts` deliberately excluded (it mirrors `Content`), as are
  `Attachments` (never sent).
- **Tool-schema cost** plumbed in via `EstimateToolDefinitionTokens` +
  `SetToolDefinitionTokens`; `Build()` records its own overhead. All three trigger paths
  now share one `contextPercent()`.
- **Argument eviction** — size-based, not a writer-name map, so MCP writers are covered
  too. Rewrites re-marshal through a parsed map so stored arguments stay valid JSON.
- **`trigger.days` / `retain.max_age_days`** — independent values with a validated gap
  (hysteresis). The age trigger bypasses `min_percent`; the age cap is subject to
  `min_messages` and the last-user-message clamp.
- **Config restructured** to `compression.{trigger,retain,estimate}`, pointer fields
  throughout so `0` means *off* rather than *unset*. Legacy flat `compress_*` keys are
  migrated at load with a WARN.
- **Validation**: `retain < min` (clamp), `trigger.days >= retain.max_age_days` (clamp),
  all-retain-bounds-disabled (warn).
- **Archive cap split by role** — 16 KB for user/assistant, 4 KB retained for tool results.
- **Eviction coverage** extended to `file_search_lines` / `file_search_bytes`, keyed on
  `query` so different searches don't supersede each other.
- **WebUI** exposes the new settings; blank means "not configured" and sends `null`
  (merge-patch deletes the key) so saving an untouched page cannot disable a trigger.

### Outstanding decision

`retain.max_tokens` defaults to `0` (off). Without it, a large-window agent's tail is
still bounded only by a percentage of a very large number — amber retains 451 messages /
72 k tokens after compaction because neither the 5-day cap nor 10% of 1 M binds.

| budget | amber | wendy | dawn |
|---|---|---|---|
| 10 k | 67 | 35 | 182 |
| 20 k | 135 | 77 | 256 |
| 40 k | 239 | 81 | 256 |

Recommendation: **20 000**. No-op for 128 k models (10% = 12 800 already binds tighter).

---

## Part 2 — Prompt caching (not started)

### The mechanism

Request order is `tools` → `system` → `messages`. Prefix caching matches the longest
common token prefix, so **anything volatile in the system message invalidates the entire
conversation history behind it**.

`BuildMessages` (`pkg/agent/context.go:560`) plus `Manager.Build` currently produce:

```
static prompt → TIME (minute granularity) → summary →
session token → cogmem STABLE → cogmem ATTACHMENTS → cogmem ROUTED
```

Everything except `static prompt` sits behind a string that changes every minute.

### Measured effect

| | 1st dispatch of a turn | later dispatches (same minute) |
|---|---|---|
| amber | **5.6%** cached | 70.1% cached |
| wendy | **1.6%** cached | 88.8% cached |

Cross-turn caching is absent; within-turn caching works. Over 31 days:

| agent | full-price input today | if the prefix were stable | reduction |
|---|---|---|---|
| amber | 70.4 M | 36.4 M | 48% |
| wendy | 64.0 M | 24.8 M | 61% |

### Target structure

```
[system: static + date line + cogmem STABLE + sticky attachments]   ← cached
[history 0 .. N-1]                                                   ← cached
[history N ..  : previous turn]                                      ← cache breaks here
[current user message + ROUTED + routed attachments]                 ← ephemeral
```

Cache extends to *current turn − 1*: one turn's tokens are re-sent instead of all of them.

### Design decisions taken

- **Date, not time.** `"Monday, Feb 12, 2026 — use time_now for the current time"` in the
  cached static prompt. A model without a date anchor does not ask for one — it silently
  uses its training cutoff, which is a wrong-answer failure, not a missing-tool failure.
  Day granularity breaks the prefix once a day instead of once a minute.
- **A `time_now` tool complements the anchor**, it does not replace it.
- **Attachments follow the memory that cites them.** `refSite` records `memoryID`, and each
  document is headed `From memory <id> ("<headline>")`; separating a document from its
  memory by ~90 k tokens breaks that binding. Sticky-owned documents stay in the prefix
  (and stay cached), routed-owned documents travel with ROUTED.
- **STABLE stays in the prefix.** It is always-on identity content, not material selected
  for this turn, so it gains nothing from adjacency and is free to cache.

Wendy's attachment set changes on only 18% of consecutive turns (177 unchanged / 39
changed), with sizes clustering at ~69 KB and ~39–46 KB — a stable core plus one document
that comes and goes. The sticky/routed split therefore self-tunes: whatever is marked
sticky gets cached, and `set_sticky` on the domain is the operator's lever.

### Work items — all implemented

| # | Item | Commit |
|---|---|---|
| 0 | Remove the dead `cache_control` / `SystemParts` plumbing and the comment that made the timestamp look safe | `Remove dead cache_control plumbing…` |
| 1 | `time_now` tool (date, time, zone, offset, epoch) | `Add the time_now tool` |
| 2 | Date line into the static prompt; timestamp out of `buildDynamicContext` | `Replace the per-minute clock…` |
| 3 | `cachedDate` invalidation in `BuildSystemPromptWithCache` | same |
| 4 | `attachmentsBlock` split by owner, dedup and shared sticky-first budget preserved | `Move routed cognitive memory…` |
| 5 | STABLE + sticky attachments stay in the prefix; ROUTED + routed attachments ride the current turn, ephemeral | same |
| 6 | Attachment-bytes log split by provenance | same |
| 7 | Tests: never persisted, budget shared, shared document appears once | across the above |

Key tests to keep: `TestBuildMessages_NoPerTurnVolatility` (two builds 90s apart must be
byte-identical), `TestRoutedMemory_NeverPersisted` (the injected block must not reach the
store — the failure is silent and cumulative), `TestDateAnchor_RollsOverAtMidnight`,
`TestAttachmentBudgetSharedAcrossPartitions`.

---

## Part 3 — Per-provider caching requirements

Researched 2026-08-22 against vendor docs. **DeepSeek is not special — automatic prefix
caching is the norm.** Anthropic is the one provider that requires an explicit opt-in.

| Provider | Mechanism | Opt-in required | ClawEh today |
|---|---|---|---|
| DeepSeek | automatic prefix match | none | ✅ works |
| OpenAI | automatic prefix match, ≥1024 tok | none; `prompt_cache_key` optional (routing) | ✅ sent, gated to `api.openai.com` / Azure |
| xAI Grok | automatic prefix match | none; **`x-grok-conv-id` header recommended** to pin server affinity | ❌ not sent |
| Google Gemini 2.5+ | implicit caching automatic; explicit caching needs `cache_control` | none for implicit | n/a (gemini-cli only) |
| Anthropic (first-party) | **explicit `cache_control` breakpoints — never automatic** | per-block markers; **no beta header**, GA | ❌ never sent |
| Anthropic via OpenRouter | same, plus a top-level `cache_control` "automatic mode" that advances the breakpoint as the conversation grows | one request field | ❌ never sent |

Cache hits are reported as `usage.prompt_tokens_details.cached_tokens` (OpenAI-compatible,
incl. Grok and OpenRouter) or `usage.cache_read_input_tokens` (Anthropic).

### Anthropic specifics

`"cache_control": {"type": "ephemeral"}` (5 min) or `{"type": "ephemeral", "ttl": "1h"}`.
Max 4 breakpoints. Reads ~0.1×; writes 1.25× (5 min) / 2× (1 h). Minimum cacheable prefix
is model-dependent and **not monotonic** — first-party docs give 512 (Opus 5), 1024
(Opus 4.8 / Sonnet 5 / Sonnet 4.6), 2048 (Opus 4.7), 4096 (Opus 4.6 / 4.5 / Haiku 4.5),
while OpenRouter's page lists 4096 for the whole Opus 4.x line. Verify empirically against
whichever route is used rather than trusting either number.

Two further Anthropic rules that bite agentic loops:

- **History is not cached by the system breakpoint.** A marker on the last system block
  caches `tools` + `system` only. Multi-turn caching needs a breakpoint on the last content
  block of the newest turn.
- **20-block lookback.** Each breakpoint searches back at most 20 content blocks for a
  prior entry. Our turns routinely exceed that with `tool_use`/`tool_result` pairs, so even
  a correct trailing breakpoint silently misses on long turns without an intermediate one.

### ClawEh gaps found

1. **The `cache_control` plumbing in `context.go` is dead code.** `context.go:583` sets
   `CacheControl: ephemeral` on the static `SystemParts` block. The only code that reads
   `part.CacheControl` is `spawnllm/anthropic/provider.go:275` — and that package **is not
   imported by ClawEh**. `factory_provider.go:13` imports `anthropic_messages`, whose
   provider flattens system messages into a plain string (`provider.go:474-556`) with no
   `CacheControl` handling.
2. **Protocol `"anthropic"` is not an Anthropic adapter.** `factory_provider.go:82` routes
   it to `NewHTTPProviderWithOptions` — openai_compat — which strips `SystemParts`
   outright (`openai_compat/provider.go:613`).
3. **No custom-header path in openai_compat.** It sets only `Content-Type` and
   `Authorization` (`provider.go:376-378`), so `x-grok-conv-id` cannot currently be sent.
   The `Headers` map at `config.go:1946` is for MCP sse/http servers, not LLM providers.
4. **No conversation-history breakpoint** anywhere, for any provider.

### Impact assessment

Of 4,325 production dispatches: Claude CLI 1,584, DeepSeek 1,406, OpenRouter Chat 1,213,
**xAI 84**, Codex CLI 34. No Anthropic HTTP provider is used for dispatch — Anthropic
traffic goes through Claude CLI, which manages its own caching.

So gaps 1 and 2 are **latent**: they cost nothing today, but any agent moved to the
Anthropic API would silently pay full price on every turn. Gap 3 is live but small (84
dispatches). **The Part 2 reordering is where the money is** — it fixes DeepSeek,
OpenRouter and Grok at once, because all three do automatic prefix matching and all three
are currently defeated by the same timestamp.

### Additional work items

8. Send `x-grok-conv-id` (session key) for xAI providers. Needs an extra-header mechanism
   in `spawnllm/openai_compat` first — **spawnllm change, separate release**.
9. Make Anthropic caching real, if/when an agent moves to the Anthropic API:
   either teach `anthropic_messages` to consume `SystemParts` with `cache_control`, or
   switch the factory to the `spawnllm/anthropic` package that already does.
   Add a breakpoint on the newest turn, and an intermediate one every ~15 blocks for the
   20-block lookback — **spawnllm change, separate release**.
10. Consider a top-level `cache_control` field for Anthropic-via-OpenRouter, which is a
    single request field rather than block surgery.

Items 8–10 are **not** in this branch: they live in spawnllm, which per project policy must
track a released version rather than a local `replace`. They are written up in full, with
the verified API parameters and the pitfalls, in **`docs/spawnllm-caching.md`** — which
assumes a user may run Claude over the API and the CLI at the same time.

