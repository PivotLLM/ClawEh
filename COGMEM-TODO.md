# ClawEh Cognitive Memory — Implementation TODO & Decision Log

Branch: `feature/cogmem`. Design: `mem-proposal.md` (v7). This file tracks
implementation progress and records decisions made autonomously for review.

**Working rule honoured this session:** another agent is active in this repo, so
all work lands in **new, self-contained `cogmem` packages**. Edits to shared
integration files (`pkg/agent/context.go`, `pkg/config/config.go`,
`pkg/memory/archive.go`, `internal/gateway/tool_providers.go`,
`pkg/providers/...`) are **deferred and listed in §4** rather than made now, to
avoid clobbering the other agent.

## Session 1 outcome (2026-06-14)

Built and **green** (`go build ./...`, `go test ./pkg/cogmem/...`, `go vet`,
`gofmt`): Phases 0, 1, 1b, 1c. **No shared files touched. No commit made**
(awaiting your review). Branch `feature/cogmem`. New files:

- `templates/COGMEM_CONSOLIDATION.md` — editable consolidation prompt (operator copy).
- `COGMEM-TODO.md` — this file.
- `pkg/cogmem/store/` — schema, store (WAL/migrations/tx), ids, domains (CRUD +
  optimistic concurrency), hooks (lifecycle + LIKE search + pending), audit
  (events/runs/watermark/leases). `store_test.go` (8 tests).
- `pkg/cogmem/` — composer: StableBlock (+ stable_rev), RoutedBlock (recency),
  pending digest, trace. `composer_test.go` (3 tests).
- `pkg/cogmem/consolidate/` — contract (I/O types + strict validation + secret
  scan), batch (count + token budget + truncation), prompt (embed + override),
  apply (validated ops → store in one txn, tmp_id mapping, audit). Embedded
  `default_prompt.md`. Tests for all four.

What this gives you to look at tomorrow: the **consolidation prompt** and the
**§3 batching decisions**, plus a working, tested storage + compose + validate +
apply core. Not yet wired into the running app (that's the deferred §4 shared
edits) and the worker/tools (Phases 2–3) are next.

Bug found & fixed mid-session: `last_active_at` used unix **seconds**, so
same-second touches tied and recency ordering fell back to id order. Switched the
recency key to unix **nanoseconds** (ordering-only field).

---

## 1. Decisions made autonomously (review these)

- **DEC-1 Module layout.** Core storage types (`Domain`, `Hook`, …) live in the
  leaf package `pkg/cogmem/store` to avoid import cycles. `pkg/cogmem` (composer)
  and `pkg/cogmem/consolidate` import `store`; `pkg/tools/cogmem` imports both.
- **DEC-2 IDs.** Short, stable, human-echoable ids: domains `d<N>`, hooks
  `h<N>`, where `<N>` is a per-DB monotonic counter stored in `meta`
  (`next_domain_seq`, `next_hook_seq`). Not UUIDs — the LLM must copy them
  verbatim, so short wins. `google/uuid` is reserved for run/event ids
  (`consolidation_runs.id`, `memory_events.id`) where humans don't type them.
- **DEC-3 Consolidation prompt location.** Default shipped at
  `templates/COGMEM_CONSOLIDATION.md` (editable); per-agent override via
  `memory.consolidation.prompt_file`. Loader falls back to the embedded default.
- **DEC-4 Batching / context size (the question asked).** See §3.
- **DEC-5 stable_rev.** A single `meta.stable_rev` integer, bumped in the same
  transaction as any change to always-on content (baseline/user_profile hooks,
  or any domain create/rename/summary/status/archive that alters the index, or
  the pending set). Compose reads it to validate its cached stable block.
- **DEC-6 Pending default.** `auto_promote=false`; `pending.surface="ask"`,
  `pending.max=8`. (Per your confirmation.)
- **DEC-7 Search.** `cogmem_search` is a SQL `LIKE` scan over active hooks — no
  FTS5 table (dropped per design). Case-insensitive, capped result count.
- **DEC-8 Archive `consolidated` flag.** Implemented as a deferred shared edit
  (§4) since it touches `pkg/memory/archive.go` (other agent's territory). The
  cogmem watermark (`consolidation_state.consolidated_seq`) is the source of
  truth in the meantime; the archive flag is a projection for retention.

---

## 2. Phased checklist

### Phase 0 — spike  ✅ (this session)
- [x] Confirm `modernc.org/sqlite` WAL open/migrate works for the memory DB
      (covered by `store` tests).
- [x] Decide `MemoryConfig` shape (drafted in §5; structs deferred to integration).

### Phase 1 — store + types  ✅ (this session, self-contained)
- [x] `pkg/cogmem/store/schema.go` — DDL (domains, hooks, consolidation_state,
      meta, memory_events, worker_leases, consolidation_runs).
- [x] `pkg/cogmem/store/store.go` — Open (WAL, busy_timeout), migrations, close.
- [x] `pkg/cogmem/store/ids.go` — monotonic id allocation (DEC-2).
- [x] `pkg/cogmem/store/types.go` — Domain, Hook, enums, ComposeBlock types.
- [x] `pkg/cogmem/store/domains.go` — CRUD + optimistic concurrency + stable_rev.
- [x] `pkg/cogmem/store/hooks.go` — lifecycle (add/supersede/retire) + LIKE search.
- [x] `pkg/cogmem/store/meta.go` — stable_rev, counters.
- [x] `pkg/cogmem/store/events.go` — audit ledger + consolidation_runs.
- [x] `pkg/cogmem/store/store_test.go` — open/migrate, id assignment, version
      conflict, hook lifecycle, stable_rev bump, search.

### Phase 1b — composer  ✅ (this session)
- [x] `pkg/cogmem/types.go` — `Composer`, `ComposeRequest/Result`.
- [x] `pkg/cogmem/composer.go` — StableBlock (curated+baseline+user_profile+
      pending+index, +rev) and RoutedBlock (recency pre-load), trace.
- [x] `pkg/cogmem/composer_test.go`.

### Phase 1c — consolidation contract  ✅ (this session)
- [x] `pkg/cogmem/consolidate/contract.go` — input/output Go types + strict
      validation (evidence-in-range, id existence, enums, secret scan, tmp_id).
- [x] `pkg/cogmem/consolidate/batch.go` — batching by count + token budget (§3).
- [x] `pkg/cogmem/consolidate/prompt.go` — load template (override → embedded).
- [x] `pkg/cogmem/consolidate/apply.go` — apply validated ops in one txn.
- [x] `pkg/cogmem/consolidate/*_test.go`.

### Phase 2 — MCP tools  ⏳ (next session)
- [ ] `pkg/tools/cogmem/global_provider.go` + `tools.go` — the 12 tools (§12 of
      proposal), id-addressed, via the `pkg/global` provider pattern.
- [ ] Unit tests; probe cases for `tests/test_mcpserver.sh`.

### Phase 3 — sleep cycle / scheduler  ⏳ (next session)
- [ ] `pkg/cogmem/consolidate/worker.go` — lease, batch loop, model call,
      validate, apply, runs record, export.
- [ ] `pkg/cogmem/consolidate/manager.go` — triggers (message/idle/nightly/manual),
      bounded pool, active-session selection.
- [ ] `pkg/cogmem/export.go` — GENERATED_*.md + Pending section.

### Phase 4 — rollout/docs  ⏳

---

## 3. Batching & context-size strategy (DEC-4)

The consolidation input = prompt + `curated` + `current_state` + `new_messages`.
We must not overflow the consolidation model's context, and we want runs cheap
and bounded. Decisions:

- **Two hard caps, whichever hits first:**
  - `max_batch_messages` (count) — default **200**.
  - `max_input_tokens` (size) — default **96_000**. Token estimate is
    `len(text)/4` (cheap, conservative); no tokenizer dependency.
- **Per-message truncation:** any single message longer than
  `per_message_char_cap` (default **12_000** chars ≈ 3k tokens) is truncated with
  a `…[truncated]` marker, so one huge tool dump can't dominate a batch.
- **`current_state` is included whole** (at < 10 domains it is small). Its
  estimated tokens are subtracted from `max_input_tokens` before filling the
  message batch; if `current_state` alone is large, the message budget shrinks
  (a signal memory has grown beyond the no-vector regime — see proposal §18).
- **Meaningful messages only:** the batch contains user/assistant content
  messages. Raw tool-call/tool-result plumbing is excluded (it is also what the
  `meaningful_count` trigger counts). Assistant messages that are pure tool calls
  are dropped; tool *results* are summarised to a short stub if referenced.
- **Oldest-first, resumable:** messages are taken in `seq` order starting at
  `consolidated_seq+1`; the watermark advances per committed batch, so a large
  backlog (cold start) is processed across successive runs idempotently.
- **Output bound:** request `max_output_tokens` (default **8_000**) — the ops
  JSON is small; this caps cost and runaway responses.
- All five knobs are per-agent configurable under `memory.consolidation`.

Rationale: count-cap keeps runs predictable; token-cap protects small-context
models; per-message truncation defends against pathological inputs; estimating
tokens by `chars/4` avoids a tokenizer dependency and errs conservative.

---

## 4. Deferred shared-file integration (do with other agent's awareness)

These were intentionally NOT edited this session:

1. **`pkg/config/config.go`** — add `MemoryConfig` to `AgentDefaults` +
   `AgentConfig` (shape in §5). Add `prompt_caching bool` to `ModelConfig`.
2. **`pkg/agent/context.go`** — for cognitive agents, append the cogmem stable
   block in `BuildSystemPromptWithCache` (key cache on `stable_rev`), add
   `MEMORY.md` to the verbatim bootstrap read, omit daily notes; append the
   routed block on the per-turn path.
3. **`pkg/memory/archive.go`** — add `consolidated INTEGER NOT NULL DEFAULT 0`
   column + the worker's `UPDATE … WHERE seq<=N`; retention skips unconsolidated
   when `protect_unconsolidated`.
4. **`internal/gateway/tool_providers.go`** — register
   `cogmem.GlobalProvider` under namespace `cogmem`.
5. **`pkg/providers/openai_compat` + `pkg/providers/common`** — when the model
   has `prompt_caching`, stop stripping `SystemParts`; emit `cache_control`
   breakpoints (OpenRouter/Anthropic/Gemini).
6. **`internal/gateway` services** — start `consolidate.Manager`; wire the
   message-append notification.
7. **`tests/test_mcpserver.sh`** — probe each `cogmem_*` tool.

---

## 5. Draft `MemoryConfig` (for §4.1 when wiring config)

```go
type MemoryConfig struct {
    Prompt        MemoryPromptConfig        `json:"prompt"`
    Consolidation MemoryConsolidationConfig `json:"consolidation"`
    Retention     MemoryRetentionConfig     `json:"retention"`
    Export        MemoryExportConfig        `json:"export"`
}
type MemoryPromptConfig struct {
    TopKDomains       int            `json:"top_k_domains"`        // 3
    MaxChars          int            `json:"max_chars"`            // 4000
    MinConfidence     float64        `json:"min_confidence"`       // 0.65
    IncludeDebugTrace bool           `json:"include_debug_trace"`  // false
    Pending           PendingConfig  `json:"pending"`
}
type PendingConfig struct {
    Surface string `json:"surface"` // "ask" | "export_only"
    Max     int    `json:"max"`     // 8
}
type MemoryConsolidationConfig struct {
    Model            string `json:"model"`              // "" → agent default model
    PromptFile       string `json:"prompt_file"`        // "" → embedded default
    DebugDump        bool   `json:"debug_dump"`
    AutoPromote      bool   `json:"auto_promote"`       // false (conservative)
    EveryNMessages   int    `json:"every_n_messages"`   // 50
    IdleMinutes      int    `json:"idle_minutes"`       // 60
    Nightly          bool   `json:"nightly"`            // true
    NightlyAt        string `json:"nightly_at"`         // "03:20"
    ProposeDomains   bool   `json:"propose_domains"`    // true
    MaxBatchMessages int    `json:"max_batch_messages"` // 200
    MaxInputTokens   int    `json:"max_input_tokens"`   // 96000
    PerMessageChars  int    `json:"per_message_chars"`  // 12000
    MaxOutputTokens  int    `json:"max_output_tokens"`  // 8000
    MaxRuntimeSecs   int    `json:"max_runtime_seconds"`// 120
}
type MemoryRetentionConfig struct { ProtectUnconsolidated bool `json:"protect_unconsolidated"` } // true
type MemoryExportConfig struct { Enabled bool `json:"enabled"` } // true
```

---

## 6. Open questions for review tomorrow

- Confirm the **batching defaults** in §3 (esp. `max_input_tokens=96k`) suit the
  models you'll trial.
- Confirm **DEC-2 id format** (`d7`/`h31`) — happy for the LLM to address by these?
- The consolidation prompt (`templates/COGMEM_CONSOLIDATION.md`) is a first
  draft — review wording before trialling models.
- Order of remaining phases: tools (Phase 2) vs worker (Phase 3) first? I plan
  tools next (smaller, lets you exercise memory by hand before automation).
