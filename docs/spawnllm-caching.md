# Prompt caching in spawnllm — what is missing and what to build

Audience: whoever works on `github.com/PivotLLM/spawnllm`. Everything here is a
change in **that** repo, not ClawEh. Written after tracing why ClawEh's
cross-turn cache hit rate was effectively zero (see
`docs/context-management-plan.md` for the ClawEh half, which is fixed).

**Assume a user may run Claude over the Anthropic HTTP API, over the Claude CLI,
or both at once** — different agents, same install. The CLI route manages its own
caching and needs nothing here. The API route currently gets no caching at all.

---

## Current state

| Protocol (ClawEh `factory_provider.go`) | spawnllm package | Caching today |
|---|---|---|
| `openai-chat` | `openai_compat` | automatic prefix match by the provider; `prompt_cache_key` sent only to OpenAI/Azure |
| `openai-responses` | `openai_responses` | automatic prefix match |
| `azure` | `azure` | automatic prefix match |
| `anthropic` | `openai_compat` ⚠️ | **none** — strips `SystemParts`, and Anthropic never caches implicitly |
| `anthropic-messages` | `anthropic_messages` | **none** — flattens system to a string, no `cache_control` anywhere |
| `claude-cli` etc. | CLI providers | handled by the CLI itself; out of scope |

There is a `spawnllm/anthropic` package that *does* read
`part.CacheControl` (`provider.go:275`) — but nothing imports it. ClawEh wires
`anthropic_messages`.

## Why this matters

Anthropic is the **only** major provider that does not cache implicitly. DeepSeek,
OpenAI, xAI Grok and Gemini 2.5+ all match prefixes automatically; Anthropic
requires explicit `cache_control` breakpoints on content blocks. So an agent
pointed at `protocol: "anthropic-messages"` today re-processes its entire
conversation at full price on every turn, with nothing in the logs to say so.

For scale: on ClawEh's production instance the two busiest agents send 103k and
131k input tokens per turn, over ~1,100 turns each per month. Caching is not a
micro-optimisation at that size.

---

## Work item 1 — Anthropic `cache_control` (the important one)

**Where:** `anthropic_messages/provider.go`. It currently accumulates system
messages into a plain string (~line 474) and emits `result["system"] = systemPrompt`
(~line 556).

**What to build:**

1. **Emit `system` as an array of content blocks**, not a string, and honour
   `Message.SystemParts[].CacheControl` when present. Fall back to the existing
   single-block behaviour when `SystemParts` is empty.

   A marker on the last system block caches `tools` + `system` together, since the
   render order is `tools` → `system` → `messages`.

2. **Add a breakpoint to the conversation**, not just the system block. This is the
   part that actually matters: a system breakpoint alone caches the preamble and
   still re-reads the whole history. Put `cache_control` on the last content block
   of the most recently appended turn; earlier breakpoints stay valid read points,
   so hits accrue as the conversation grows.

3. **Respect the 20-block lookback.** Each breakpoint searches back at most 20
   content blocks for a prior cache entry. Agentic turns routinely exceed that
   with `tool_use`/`tool_result` pairs, so a single trailing breakpoint silently
   misses on long turns. Place an intermediate breakpoint roughly every 15 blocks.

4. **Stay within 4 breakpoints per request** — that is the hard API limit. With
   one on system and up to three walking the conversation, the budget is tight;
   spend it deliberately.

**Parameters (verified 2026-08-22):**

- `"cache_control": {"type": "ephemeral"}` → 5-minute TTL (default)
- `"cache_control": {"type": "ephemeral", "ttl": "1h"}` → 1-hour TTL
- **No beta header.** Prompt caching is GA.
- Reads cost ~0.1× base input. Writes cost 1.25× (5 min) or 2× (1 h). Break-even
  is two requests at 5 min, three at 1 h.
- Minimum cacheable prefix is model-dependent and **not monotonic across
  generations**: 512 tokens (Opus 5), 1024 (Opus 4.8, Sonnet 5, Sonnet 4.6),
  2048 (Opus 4.7), 4096 (Opus 4.6, Opus 4.5, Haiku 4.5). Below the minimum
  nothing caches and no error is raised — `cache_creation_input_tokens` is just 0.
  Note OpenRouter's docs list 4096 for the whole Opus 4.x line, disagreeing with
  Anthropic's first-party figures; verify empirically for the route in use rather
  than trusting either.
- Verify with `usage.cache_read_input_tokens`. Total prompt size is
  `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`;
  `input_tokens` alone is the uncached remainder, so a small value there means
  caching is working, not that the prompt was small.

**Invalidation hierarchy** (useful for deciding what is safe to vary per request):
changing tool definitions or the model invalidates everything; changing the system
prompt invalidates system + messages; changing `tool_choice` or toggling thinking
invalidates messages only. So per-request `tool_choice` is cheap; a per-request
tool list is catastrophic.

## Work item 2 — fix `protocol: "anthropic"`

`factory_provider.go:82` maps `"anthropic"` to `NewHTTPProviderWithOptions`,
i.e. the OpenAI-compatible provider. Anyone who writes `"protocol": "anthropic"`
expecting the Anthropic API silently gets an OpenAI-shaped request with
`SystemParts` stripped.

Either route it to the real Anthropic adapter or reject it at config validation
with a message naming `anthropic-messages`. Silently doing something else is the
worst of the three options. (This one is a **ClawEh** change; it is listed here
because it is discovered from the same trace.)

## Work item 3 — custom headers, for `x-grok-conv-id`

`openai_compat/provider.go` sets only `Content-Type` and `Authorization`
(~line 376). xAI caches automatically, but recommends the `x-grok-conv-id`
header: it routes requests with the same conversation id to the same server, and
cache entries are per-server. Without it, cache hits depend on load-balancer luck.

Add a per-provider extra-header map to the openai_compat options, then have
ClawEh pass the session key as `x-grok-conv-id` for xAI providers. The Responses
API equivalent is the existing `prompt_cache_key` field.

## Work item 4 — top-level `cache_control` for Anthropic via OpenRouter

OpenRouter supports an "automatic mode": a `cache_control` field at the request
root, which places the breakpoint on the last cacheable block and advances it as
the conversation grows. For an Anthropic model reached through OpenRouter's
OpenAI-compatible endpoint, that is one request field instead of block surgery —
much cheaper than item 1, though it only helps that route.

---

## What NOT to do

- **Do not put anything per-request in the system block.** That is the defect this
  investigation started from. Render order is `tools` → `system` → `messages`, so a
  timestamp in `system` invalidates the entire history behind it. ClawEh now
  guarantees a stable system message; spawnllm should not reintroduce volatility
  (request ids, per-call metadata) into the prefix.
- **Do not vary the tool list per request.** Tools render at position 0. Adding,
  removing or reordering one invalidates everything. Serialize deterministically
  (sorted by name).
- **Do not rebuild `system`/`tools`/`model` differently for a fork.** Summarization
  and sub-agent calls that reconstruct the preamble miss the parent's cache
  entirely. Copy the parent's verbatim and append.
- **Do not assume caching is on because a marker exists.** ClawEh set
  `cache_control` on `SystemParts` for a long time; no wired adapter ever read it.
  Assert on `cache_read_input_tokens` in a test, not on the request shape.

## Suggested verification

A cache is easy to *think* you have. Split the measurement by dispatch position
within a turn: iterations of one turn share a prefix trivially and will look
healthy even when cross-turn caching is completely broken. The metric that
matters is the hit rate on the **first** dispatch of a turn. On ClawEh before the
fix that was 5.6% and 1.6% for the two busiest agents, against 70–89% for later
dispatches in the same turn — the aggregate figure of 42–68% hid the problem
entirely.
