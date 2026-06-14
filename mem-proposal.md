# Cognitive Memory Engine - Architecture Proposal

**Status:** Draft v7 - consolidated single plan, implementation-detail pass
**Project:** ClawEh (`github.com/PivotLLM/ClawEh`)
**Date:** 2026-06-14
**Objective:** Make long-running agents smarter over time by automatically
distilling their own conversation history into durable, de-duplicated,
contradiction-free memory, and composing the right system prompt per turn.

Pure Go, no CGO, no external embedding or vector service. The five standard
OpenClaw markdown files are retained for compatibility and become the
human-curated layer; a per-session SQLite database holds the learned layer.
Cognitive memory is activated per agent by allowing the `cogmem` tools - there is
no separate engine flag.

---

## 1. Decisions Locked

| # | Decision | Outcome |
|---|---|---|
| D1 | Memory scope | **Per session.** One `.cogmem.db` per session key, colocated with that session's `.archive.db`. `Session.Mode` sets the privacy boundary (unified → per-agent; per-user → per-user). |
| D2 | Retrieval | **No embeddings, no FTS.** In-prompt domain index + LLM self-routing + recency pre-load + a deterministic load-by-id tool. Vectors deferred (§18). |
| D3 | Routing trace | A trace/test mode showing which domains were selected and why is a first-class feature (out-of-band, never in the cached prompt). |
| D4 | Consolidation model | **Configurable per agent**; if unset, falls back to the agent's default model. Prompts are externalized/editable and per-run debug records support comparing models (§11.8). |
| D5 | Domain discovery | **Hybrid.** Explicit creation via tools; the sleep cycle may propose domains in `review` status, kept out of prompts until confirmed. |
| D6 | Sleep-cycle triggers | **Per-agent configurable.** Defaults: every **50** meaningful messages, **60** idle minutes, nightly **on**. |
| D7 | Retention guard | **Per-agent `protect_unconsolidated`** prevents archive pruning from removing unconsolidated messages. |
| D8 | Curated files + persona | The **5 standard files are kept** as the verbatim, authoritative human layer; the agent writes learned memory to the **DB, not the files**; consolidation reconciles learned memory against the curated files (curated always wins). |
| D9 | Forget/purge | `cogmem_forget` retires active memory + regenerates the read-only export. Physical archive purge stays an operator workflow, never an LLM tool. |
| D10 | Activation | **No `engine` flag.** Cognitive memory is active for an agent exactly when that agent is allowed the `cogmem` tools (existing per-agent tool allowlist). Allowing/denying those tools is the on/off control (§3.3). |
| D11 | File access | General file-write tools are confined by default to `<workspace>/files/`; the curated `.md` files, `memory/`, and `sessions/` are **not** agent-writable. Memory is written only via `cogmem` tools (§3.4). |
| D12 | Addressing | Domains and hooks have **short, stable, system-assigned ids**. The index shows `id · name · summary`; tools key on **id**. Names are LLM-supplied labels and need not be unique (§10, §12). |
| - | Tunables | `top_k_domains`, `min_confidence`, `max_chars`, nightly time, batch caps - all **per-agent configurable** with recommended defaults. |
| - | Caching | The composed prompt is ordered **stable → volatile** so caching is preserved; the per-turn routed block sits at the tail (§17). |

---

## 2. The Core Idea

Two independent problems - keep them separate:

1. **Learning** - distilling conversation into durable, de-duplicated,
   contradiction-free knowledge. *This is the source of "smarter over time."* It
   is an LLM + SQLite job; no embeddings.
2. **Retrieval** - getting the right memory into the prompt. With ClawEh's scale
   (< ~10 projects per agent) this is trivial: list the domains in the prompt and
   let the LLM pick, backed by a load tool. Embeddings/FTS only matter when there
   are too many items to list - not ClawEh's situation today.

ClawEh already persists each session to a SQLite archive
(`<workspace>/sessions/<key>.archive.db`), with deterministic, stable session
keys (`pkg/routing/session_key.go`) and an archive that survives context clears
(`pkg/llmcontext/manager.go` `Reset` - only manual file deletion removes it).
Compression already protects the context window and the archive's FTS already
lets the agent reach back into raw history. The missing piece is **automated,
structured learning** so memory stops being append-only, manual, and
contradiction-prone.

---

## 3. Two-Layer Memory Model

Memory is split by **who authors it**. This is the central design choice.

| | **Curated layer** | **Learned layer** |
|---|---|---|
| Author | The human | The agent / sleep cycle |
| Store | The 5 standard `.md` files | Per-session `.cogmem.db` |
| Contents | Persona, identity, voice, standing user facts, standing notes | Project state, lessons, inferred preferences/facts |
| In prompt | Injected **verbatim** | Composed from structured rows |
| Authority | **Wins all conflicts** | Below the curated layer |
| Human access | Read + edit directly | Read via generated export; edit via tools/conversation |

The human edits the `.md` files the familiar way; those win. The agent records to
the DB (immediately, or via consolidation if it doesn't). When the agent learns
something that contradicts a curated file, the curated text wins in the prompt
*and* the next sleep cycle retires the stale learned item.

### 3.1 The Five Standard Files (kept for compatibility - D8)

Retained and used exactly as today, read **verbatim** into the prompt:

- `AGENTS.md` - operating instructions / rules
- `SOUL.md` - personality / voice
- `IDENTITY.md` - who the agent is
- `USER.md` - who the user is
- `MEMORY.md` - human-curated standing notes

(`BOOTSTRAP.md` and `COMPRESSION.md` are functional, not persona, and are
untouched.) For an agent with the `cogmem` tools enabled, the **only** behavior
change is that the agent no longer auto-writes these files - its prompt
instruction to "update MEMORY.md" is replaced by "use the `cogmem` tools," and
the file-write confinement (§3.4) makes that structural. The files remain yours;
nothing silently edits them.

> **Integration note (B5).** Today `MEMORY.md` and recent daily notes are
> injected via `MemoryStore.GetMemoryContext()`, which cognitive mode *replaces*
> with the cogmem stable block. So in cognitive mode `MEMORY.md` must be read into
> the **verbatim bootstrap block** (alongside the other four, in
> `LoadBootstrapFiles`), and the rolling **daily notes are not injected** - their
> accumulating role moves to the DB. Without this, `MEMORY.md` would silently drop
> out when cognitive memory is enabled.

### 3.2 The Learned Layer (DB)

The DB holds what accumulates and contradicts itself, where structure earns its
keep: structured domains + hooks, conflict resolution, audit trail, and a
read-only `GENERATED_*.md` export so the human can browse what the agent believes.

### 3.3 Activation (no engine flag - D10)

There is **no `memory.engine` on/off setting.** Cognitive memory is active for an
agent exactly when that agent is allowed the `cogmem` tools, via the existing
per-agent tool allowlist (`AgentConfig.Tools` / `IsToolAllowed`,
`pkg/config/config.go`). Allowing the `cogmem` provider for an agent wires the
whole subsystem for it - the DB-backed Compose path, the sleep cycle, and the
file-write confinement (§3.4). Denying those tools leaves the agent on today's
file-based behavior. The tool allowlist is the single source of truth for "does
this agent participate in cognitive memory."

### 3.4 Workspace & File Access (D11)

The agent's general file tools (`write_file`, `edit_file`, `append_file`,
`copy_file`) are confined by default to a working-documents directory:

```
<workspace>/files/      ← drafts and working documents (the only agent-writable area)
```

Everything else in the workspace is **not** agent-writable by default - in
particular the curated `.md` files (workspace root), the `memory/` directory
(including the `GENERATED_*` export), and `sessions/` (the `.archive.db` and
`.cogmem.db` files). **Reads** remain broad within the workspace.

This is a structural boundary, not a prompt instruction. Enforcement reuses the
file-tool allowlist that already exists (`pkg/tools/files/global_provider.go`):
`Tools.AllowWritePaths` defaults to `<workspace>/files/`, and
`RestrictToWorkspace` already bounds the agent to its workspace. The write tools
physically cannot touch protected paths.

Memory is never written through general file tools: a cognitive agent records
learned memory exclusively through the `cogmem_*` tools (which target the DB). So
confining file writes to `files/` costs the agent nothing it still needs - the
two mechanisms separate cleanly: **file tools = working documents; cogmem tools =
memory.** A prompt line should still direct working files to `files/` and memory
to the `cogmem` tools (defense in depth), but the allowlist is the enforcement.

*Migration note:* an agent **without** cogmem tools that still relies on
file-based memory writes (`MEMORY.md`, daily notes under `memory/`) needs
`memory/` added to its `AllowWritePaths`, or it should be moved to the `cogmem`
tools. New deployments default to `files/`-only writes.

---

## 4. The Loop

1. **Observe** - read new messages from the session's existing `.archive.db`
   (read-only). No new hot-path writes.
2. **Consolidate** - a background sleep cycle periodically distills new messages
   into structured learned memory, resolving contradictions and recording
   evidence + an audit event, and reconciling against the curated files.
3. **Compose** - each turn, inject the curated files (verbatim) + the always-on
   learned domains + a one-line index of all domains, and pre-load the full state
   of the most-recently-active domain. The agent can pull any other domain by id.
4. **Act immediately** - safe MCP tools let the agent record, forget, and update
   project state without raw SQL or raw file writes.

---

## 5. Scope: One Memory DB Per Session (D1)

```
<workspace>/sessions/<sanitized_key>.archive.db    (existing - source feed, read-only)
<workspace>/sessions/<sanitized_key>.cogmem.db     (new - learned memory, read/write)
```

- **Privacy for free.** Unified mode (default) → one `:main` session → per-agent.
  Per-user mode → one session per user → per-user memory. No extra code.
- **1:1 loop.** One archive feeds one memory DB; a single consolidation watermark.
- **Single-writer safety.** Separate files keep each one's single-writer
  invariant. The sleep-cycle worker is the sole writer of `.cogmem.db`; it opens
  `.archive.db` read-only (`memory.OpenReadOnly`).
- **Multi-session modes & resources.** In per-platform/per-account modes one agent
  can own many sessions, hence many `.cogmem.db` files, baselines, and
  consolidation runs. Bound this: **consolidate only sessions with new activity**;
  let idle sessions lie untouched. The *learned* baseline is per-session in these
  modes, but the **curated files keep persona consistent** across all of an
  agent's sessions, so divergence is limited to learned inferences.

---

## 6. Core Concepts

- **Memory domain** - a coherent body of learned knowledge for a session.
  Always-on types: `baseline` (global learned rules), `user_profile` (learned user
  facts). Routed types: `project`, `workflow`, `repo`. Typically < 10 active per
  session.
- **Hook** - a short, active learned statement (rule, fact, preference, lesson,
  state pointer). The unit of conflict resolution and audit. Lessons are hooks
  (`kind=lesson`), not a domain type.
- **Domain state** - structured JSON: summary, blockers, next actions,
  constraints, domain-specific fields. Validated by schema version.
- **Id** - every domain and hook has a short, stable, system-assigned id (e.g.
  `d7`, `h31`). The LLM addresses everything by id and never invents ids (D12).
- **Evidence** - why a hook/state change exists: archive `(seq_start, seq_end)`
  or an explicit tool event.

---

## 7. What Counts As Learned Memory

| Kind | Example | Treatment |
|---|---|---|
| `preference` | "Eric prefers concise final answers." | Baseline (always-on) |
| `rule` | "Do not deploy this project on Fridays." | Domain-scoped or global |
| `fact` | "The BioTech report targets Q3." | Relevant by domain |
| `project_state` | Summary, blockers, next actions, decisions | Relevant by domain |
| `workflow` | "For PR reviews, findings first." | Relevant by task type |
| `lesson` | "This repo uses `rg`; avoid grep." | Relevant by repo/tooling |
| `profile` | Learned user/team facts | `user_profile` (always-on) |

Reject or down-rank: greetings/filler/jokes; turn-only temporary instructions;
stale guesses later contradicted; **secrets, API keys, session/callback tokens,
credentials**; the contents of the generated export. Be conservative - skipping a
weak candidate beats persisting noise that biases every future turn.

---

## 8. Prompt Layout (always-on vs routed; caching-aware)

The composed prompt is ordered **most-stable first, most-volatile last** so
provider prompt caching reuses the long stable prefix (§17):

```
[ identity / system header ]                         STABLE  ─┐
[ tool definitions ]                                          │ cacheable
[ curated files verbatim: AGENTS, SOUL, IDENTITY, USER, MEMORY ] (authoritative)
[ learned baseline domain  +  learned user_profile domain ]   │
[ pending (unconfirmed) digest: capped candidate list ]       │
[ domain index: one line per domain, STABLE sort ]          ─┘ ← cache breakpoint
------------------------------------------------------------- VOLATILE
[ routed block: full state of the most-relevant domain ]      (per turn)
[ conversation history ]
[ new user message ]
```

- **Always-on, in full, every turn:** curated files + `baseline` + `user_profile`
  + the index. These change rarely, so they live in the cached region.
- **Index** = `<id> · <name> — <one-line summary>` per domain (a couple hundred
  tokens at < 10 domains). This is what lets the LLM route itself. Sorted by a
  **stable key (id)**, never by recency, so it doesn't reshuffle each turn.
- **Pending digest** = a small, capped list of `review` (unconfirmed) candidate
  items, labeled as *not yet applied*. It lets the agent ask you to confirm them
  (§11.6); it is **never treated as active rules/facts**. It changes only when
  consolidation runs, so it stays in the cached region.
- **Routed block** = full state + active hooks of the domain pre-loaded this turn.
  It is the only per-turn-varying memory, and it sits at the **tail** so it never
  invalidates the cached prefix.
- **Rule scope:** global hooks live in `baseline` (always loaded); domain hooks
  load with their domain. A domain rule outranks a global one *for that domain*
  (§11.5 rule 4).

---

## 9. Data Model

One database per session: `<workspace>/sessions/<key>.cogmem.db`. No vector
columns, no FTS.

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL
);

-- Single-row bookkeeping. `stable_rev` is bumped (same transaction) whenever
-- always-on content changes: any baseline/user_profile edit, or any domain
-- create/rename/summary/status change that affects the index. Compose reads it
-- cheaply to decide whether the cached stable block is still valid (B4).
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY, value TEXT NOT NULL
);  -- seeded with ('stable_rev','0')

CREATE TABLE IF NOT EXISTS domains (
  id TEXT PRIMARY KEY,              -- short, stable, system-assigned (e.g. d7)
  agent_id TEXT NOT NULL, session_key TEXT NOT NULL,  -- constant per DB; for portability/debug
  type TEXT NOT NULL,               -- baseline, user_profile, project, workflow, repo
  name TEXT NOT NULL,               -- LLM-supplied label; not required unique
  status TEXT NOT NULL,             -- active, review, archived
  version INTEGER NOT NULL,         -- optimistic concurrency
  summary TEXT NOT NULL DEFAULT '', -- one line; shown in the index
  state_json TEXT NOT NULL DEFAULT '{}',
  schema_name TEXT NOT NULL, schema_version INTEGER NOT NULL,
  last_active_at INTEGER,           -- recency signal for Compose pre-load
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER
);

CREATE TABLE IF NOT EXISTS hooks (
  id TEXT PRIMARY KEY,              -- short, stable, system-assigned (e.g. h31)
  domain_id TEXT NOT NULL REFERENCES domains(id),
  kind TEXT NOT NULL,               -- preference, rule, fact, project_state, workflow, lesson
  text TEXT NOT NULL,
  status TEXT NOT NULL,             -- active, review, retired, rejected
  confidence REAL NOT NULL, priority INTEGER NOT NULL,
  source TEXT NOT NULL,             -- user_explicit, assistant_inferred, tool_write, migration
  source_session TEXT, source_seq_start INTEGER, source_seq_end INTEGER,
  supersedes_hook_id TEXT, retire_reason TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hooks_domain ON hooks(domain_id, status);

CREATE TABLE IF NOT EXISTS consolidation_state (   -- 1:1 with this session's archive
  archive_path TEXT PRIMARY KEY,
  consolidated_seq INTEGER NOT NULL, last_seen_seq INTEGER NOT NULL,
  meaningful_count INTEGER NOT NULL DEFAULT 0,      -- since last run, for the message trigger
  last_run_at INTEGER, updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_events (         -- append-only audit log
  id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,         -- create, update, retire, merge, reject, conflict_resolved, gap
  domain_id TEXT, hook_id TEXT, old_json TEXT, new_json TEXT,
  reason TEXT NOT NULL, evidence_json TEXT NOT NULL,
  actor TEXT NOT NULL,              -- sleep_cycle, mcp_tool, migration, operator
  model TEXT, prompt_hash TEXT, created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS worker_leases (
  name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at INTEGER NOT NULL
);

-- Per-run debug/observability so different consolidation models can be compared.
CREATE TABLE IF NOT EXISTS consolidation_runs (
  id TEXT PRIMARY KEY,
  trigger TEXT NOT NULL,            -- message, idle, nightly, manual
  model TEXT NOT NULL,
  seq_start INTEGER, seq_end INTEGER,
  input_tokens INTEGER, output_tokens INTEGER,
  status TEXT NOT NULL,             -- ok, invalid_json, aborted, error
  ops_applied INTEGER NOT NULL DEFAULT 0,
  error TEXT, prompt_hash TEXT,
  started_at INTEGER NOT NULL, finished_at INTEGER
);
```

> **Archive change (point 4).** The existing archive `messages` table
> (`pkg/memory/archive.go`) gains a `consolidated INTEGER NOT NULL DEFAULT 0`
> column. The worker sets it after a successful run (§11.4); retention/pruning
> skips `consolidated = 0` rows when `protect_unconsolidated` is on (§11.4, D7).
> Archive **size/date retention config is untouched.** This makes the worker a
> second, occasional writer to `archive.db` - safe under WAL, since it only flips
> the flag on already-committed old rows while appends touch new rows.

**Why structured rows, not one JSON blob:** item-level conflicts are auditable;
retiring one bad memory doesn't rewrite unrelated state; optimistic concurrency is
cleaner per-row. Keep `state_json` for structured domain state; represent discrete
knowledge as hooks.

**Search at this scale.** `cogmem_search` runs a plain SQL `LIKE`/scan over active
hooks - trivially fast over a few hundred rows. No FTS5 table is needed; the
archive keeps its own FTS for raw-history search, which is a different corpus and
purpose.

---

## 10. Compose: Per-Turn Prompt Memory (D2, D3, D12)

No embeddings, no FTS, no network on the hot path. Two parts, split for caching:

**Stable part (cacheable):** curated files (verbatim, incl. `MEMORY.md` - see
B5 note in §3.1) + `baseline` + `user_profile` + the capped **pending digest**
(§11.6) + the domain index (stable sort by id). It carries the `meta.stable_rev`
value so the caller can cache it and invalidate only when `stable_rev` changes
(B4).

**Dynamic part (per turn, tail):** the full state + active hooks of the
**most-recently-active** domain(s) by `last_active_at`, up to `top_k_domains`,
bounded by `max_chars`. This covers continuation. On a **topic switch** the LLM
sees the index and pulls the right domain with `cogmem_get_domain(id)` - one tool
round-trip, rare and cheap at < 10 domains. (Recency is the only pre-load signal;
keyword/vector pre-load is deferred - §18.)

```go
// package cogmem
type Composer interface {
    // StableBlock returns the always-on memory plus its revision. The caller
    // caches the text and rebuilds only when rev changes.
    StableBlock(ctx context.Context, agentID, workspace, sessionKey string) (text string, rev int64, err error)
    // RoutedBlock returns the per-turn relevant domain(s), pre-loaded by recency.
    RoutedBlock(ctx context.Context, req ComposeRequest) (ComposeResult, error)
}
type ComposeRequest struct {
    AgentID, Workspace, SessionKey, RouteText string
    MaxChars int
    Trace    bool
}
type ComposeResult struct {
    Text   string
    Loaded []string          // domain ids pre-loaded
    Trace  []DomainSelection // id, name, signal (recency), score - out-of-band
}
```

**Output (routed block):** rendered with hook ids so the LLM can act on a specific
hook (e.g. `cogmem_retire_hook`):

```markdown
## Active Context: d7 · Website Redesign
Summary: CSS grid migration, launch blocked on QA sign-off
Blockers:
- (h31) QA backlog
Next actions:
- (h44) Re-test mobile breakpoints
```

No `review`/low-confidence hooks. No raw transcripts. **Compose never fails the
turn** - on DB failure the routed block is empty and a warning is logged; the
curated files + always-on domains still render.

**Trace / test mode (D3).** With `include_debug_trace` on, the selection
(candidate domains, signal, scores, what was loaded) is surfaced **out-of-band**
(returned to the caller / logged), never injected into the cached prompt:

```
[memory] index: 4 domains | pre-loaded by recency: d7 Website Redesign
```

### 10.1 Code Seam

- `pkg/agent/context.go` `BuildSystemPromptWithCache()` keeps building identity,
  tool defs, curated bootstrap files, and skills. For a cognitive agent it also
  appends the cogmem **stable block** in place of `GetMemoryContext()`, **adds
  `MEMORY.md` to the verbatim bootstrap read, and omits daily notes** (B5). Its
  cache keys on the existing file mtimes **plus `meta.stable_rev`** (a single
  cheap row read), so DB changes to always-on content invalidate the cache (B4).
- The cogmem **routed block** is added on the per-turn message path
  (`pkg/agent/context.go` `BuildMessages` / `pkg/llmcontext/manager.go` `Build`),
  at the tail, using the latest user message as route text.
- For an agent **without** the cogmem tools, `pkg/agent/memory.go`
  `GetMemoryContext()` is used exactly as today; cogmem is inert.
- **Read connections.** Compose reads `.cogmem.db` every turn while the worker
  may be writing it. Use WAL with short-lived read-only connections (the
  `ArchiveStore.OpenReadOnly` pattern) or a small read pool; never block the turn
  on a writer.

---

## 11. Consolidate: The Sleep Cycle

Where the agent learns. Background only, never on the user-turn hot path.

### 11.1 Triggers (D6 - per-agent configurable)

Any enabled trigger fires a run: **message threshold** (default every 50
meaningful messages), **idle** (default 60 minutes), **nightly** (default on,
jittered, configurable time), **manual** (non-blocking MCP tool). More frequent =
fresher memory and more consolidation-model spend.

A **meaningful message** is a user or assistant turn that carries content - it
excludes tool-call/tool-result plumbing and the noise the `cronmsg` collapse
already filters. The count is tracked in `consolidation_state.meaningful_count`
and reset on each run.

### 11.2 Scheduler & Integration (B3)

A single `consolidate.Manager` (in `pkg/cogmem/consolidate`), started by the
gateway alongside the other services, owns triggering and dispatch:

- **Message trigger:** the message path that appends to the archive notifies the
  Manager (a cheap in-process signal) for cognitive agents; the Manager increments
  `meaningful_count` and enqueues a job at the threshold. A periodic sweep is the
  backstop if a signal is missed.
- **Idle trigger:** the Manager tracks last-activity per active session and
  enqueues after `idle_minutes`.
- **Nightly:** one jittered scheduled sweep enqueues all **active** sessions
  (those with new archive messages since their watermark). Idle sessions are
  skipped (§5 resource bound).
- **Dispatch:** jobs run on a bounded worker pool; each job acquires the session's
  `worker_leases` row before doing work.

This mirrors ClawEh's existing background patterns (cron, mtime-based config
reload) and keeps all triggering out of the hot path.

### 11.3 Worker Lease

One active worker per session, guarded by a SQLite lease (expiry, owner,
heartbeat). Prevents nightly/idle/manual runs and multiple process instances from
clobbering the same DB.

### 11.4 Algorithm

1. acquire the lease;
2. read archive bounds vs `consolidated_seq`; read the new-message batch
   (`max_batch_messages` cap; on first run this may chew through the existing
   backlog over several batches - see cold-start below);
3. assemble the consolidation **input** (§11.8): curated files + current
   domains/hooks (active + review) + the new-message batch;
4. call the **configured** consolidation model (D4) in JSON-object mode;
5. **validate the payload locally against the JSON Schema** (§11.8) - provider
   JSON mode is not sufficient on its own;
6. apply deterministic conflict rules (§11.5), with the curated files outranking
   learned memory;
7. assign ids to created domains/hooks; apply all ops, write a `memory_events`
   row per op, bump `meta.stable_rev` if always-on content changed, and advance
   the watermark - **all in one transaction**;
8. mark the archive: `UPDATE messages SET consolidated=1 WHERE seq <= consolidated_seq`
   (point 4 - so retention can protect unconsolidated messages, D7);
9. record a `consolidation_runs` row (+ the `debug_dump` file if enabled);
10. regenerate the `GENERATED_*` export if enabled.

Invalid output aborts that unit and leaves the watermark unchanged for retry.
Optimistic concurrency: each `update` op carries the domain's `expected_version`;
if a live MCP tool wrote first, that op fails and the worker reloads and retries.

**Cold start.** On first enable, learned domains are empty and the first
consolidation processes the *entire existing archive* in `max_batch_messages`
chunks across successive runs. Expect higher first-run cost; the watermark makes
it resumable and idempotent.

### 11.5 Conflict Rules

1. immutable system/developer/tool-safety instructions outrank all memory;
2. **curated files outrank learned memory**;
3. explicit user instruction outranks assistant inference;
4. domain-specific instruction outranks global preference for that domain;
5. newer explicit instruction outranks older at the same scope;
6. high-confidence evidence outranks low-confidence inference;
7. contradictions retire/supersede old hooks rather than leaving both active;
8. uncertain conflicts move to `review` and are omitted from prompts.

Every resolution writes a `memory_events` row (old, new, evidence, reason).

### 11.6 Domain Discovery (D5 - hybrid)

- **Explicit** - agent/user calls `cogmem_create_domain`.
- **Proposed** - the sleep cycle may create a domain in **`review` status**, kept
  out of prompts until confirmed by later evidence or an explicit `cogmem_remember`
  / promotion. Broad coverage without junk topics in routing.
- **Promotion (point 3)** - by default (`auto_promote: false`, conservative)
  `review` items become `active` only on explicit confirmation; the sleep cycle
  does **not** auto-promote its own inferences. Set `auto_promote: true` for eager
  learning (the model may promote on strong corroboration) - faster, slightly
  riskier.
- **Pending confirmations.** `review` items are not silently shelved: a capped
  **pending digest** is surfaced in the prompt (§8/§10) so the agent can confirm
  them with you **opportunistically** - prompt guidance is "ask at natural moments,
  batch, do not pester." On "yes" the agent calls `cogmem_remember` and the item
  becomes `active`; on "no" it is retired/rejected. Pending items also appear in
  the export (§13) so you can confirm/reject proactively. Per-agent
  `pending.surface` selects `ask` (default) or `export_only` (no proactive asking);
  `pending.max` caps how many are shown.

### 11.7 Self-Improvement Signals

Look for operating lessons, not just facts: repeated corrections about
tone/format/workflow; tool failures followed by a working alternative;
repo-specific patterns the agent had to rediscover; user-approved decisions;
recurring blockers and fixes; tasks that took too many turns. These become
`lesson`/`workflow` hooks only when durable and scoped; one-offs stay in `review`.

### 11.8 Consolidation Contract (the core artifact)

The contract between ClawEh and the consolidation model. This is the heart of the
learning loop; everything else is plumbing.

**Input** (assembled by the worker, sent as the user message in JSON-object mode):

```jsonc
{
  "curated": {                      // read-only, highest authority (§11.5 rule 2)
    "AGENTS_md": "...", "SOUL_md": "...", "IDENTITY_md": "...",
    "USER_md": "...", "MEMORY_md": "..."
  },
  "current_state": {                // so the model updates vs duplicates
    "domains": [
      { "id": "d7", "type": "project", "name": "Website Redesign",
        "status": "active", "version": 4, "summary": "...",
        "state": { "blockers": [...], "next_actions": [...], "constraints": [...] },
        "hooks": [ { "id": "h31", "kind": "rule", "text": "...", "confidence": 0.9 } ] }
    ]
  },
  "new_messages": [                 // the unconsolidated batch, with seq + role
    { "seq": 412, "role": "user", "text": "Actually, use blue for the layout." }
  ]
}
```

**Output** (strict JSON, validated locally against a schema):

```jsonc
{
  "domain_ops": [
    { "op": "create", "tmp_id": "t1", "type": "project", "name": "...",
      "summary": "...", "status": "review",          // inferred → review by default
      "evidence": { "seq_start": 400, "seq_end": 410 } },
    { "op": "update", "id": "d7", "expected_version": 4,
      "summary": "...", "state": { "blockers": ["..."], "next_actions": ["..."] },
      "evidence": { "seq_start": 412, "seq_end": 412 } },
    { "op": "archive", "id": "d9", "reason": "...", "evidence": { ... } }
  ],
  "hook_ops": [
    { "op": "add", "domain": "d7", "kind": "rule", "text": "...",
      "confidence": 0.95, "source": "user_explicit", "evidence": { ... } },
    { "op": "supersede", "old_id": "h31", "domain": "d7", "kind": "rule",
      "text": "Use blue for the layout.", "confidence": 0.95, "evidence": { ... } },
    { "op": "retire", "id": "h12", "reason": "no longer relevant", "evidence": { ... } }
  ],
  "conflict_ledger": [
    { "resolved": "Replaced 'never use blue' with 'use blue for the layout'",
      "reason": "explicit user instruction, seq 412 (§11.5 rule 3/5)",
      "evidence": { "seq_start": 412, "seq_end": 412 } }
  ]
}
```

Rules baked into the prompt + validator:
- `tmp_id` lets hooks in the same payload reference a domain being created; the
  worker maps `tmp_id` → assigned id on commit.
- Every op **must** carry `evidence` (a seq range in the batch) or be rejected.
- **Inferred** domains/hooks default to `status: review`; only explicit user
  instruction (or strong corroboration) may be `active`.
- Reject ops referencing unknown ids, secrets/tokens in `text`, oversized `text`,
  or invalid `kind`/`type`/`status` enums.
- A whole-payload validation failure aborts the run (watermark unchanged, retry
  next cycle). Partial application is never allowed.

**Prompt (sketch).** A system instruction that states: the engine's purpose; the
conflict rules (§11.5); what counts and what to reject (§7); "return only JSON
matching this schema; do not duplicate anything already in `curated` or
`current_state`; inferred items are `review`; cite evidence seq ranges; resolve
contradictions by retiring/superseding, never by keeping both."

**Prompts are external and editable.** The default consolidation prompt ships as
an embedded template (`templates/COGMEM_CONSOLIDATION.md`) and is overridable per
agent via `consolidation.prompt_file` - edit a text file, no recompile, and A/B
prompts alongside models. The prompt and the model are the two quality levers.

**Per-run debug (for model comparison).** Each run records a `consolidation_runs`
row (trigger, model, seq range, input/output tokens, latency, status,
ops-applied, error, prompt_hash), surfaced by `cogmem_status`. With `debug_dump`
on, the full prompt + raw response + parsed ops are written to
`memory/consolidation/<timestamp>.json` so you can read exactly what each model
produced and why it was accepted or rejected.

**Safety recap.** The model only *proposes*; ClawEh validates and applies. A weak
or misbehaving model cannot corrupt memory - an invalid payload aborts the run
(watermark unchanged) and is retried, never partially applied.

---

## 12. Agent MCP Tools

Small, high-level surface - no raw SQL, no raw memory-file writes. A
transport-neutral provider under `pkg/tools/cogmem`, mounted in
`internal/gateway/tool_providers.go`:

```go
tools.RegisterProvider(tools.NamespacedProvider("cogmem", cogmem.GlobalProvider))
```

Built for agents whose tool allowlist includes the `cogmem` provider - this
allowlist **is** the on/off control for cognitive memory (no separate `engine`
flag; §3.3). Write tools are session-scoped so the server attaches source evidence
automatically. All tools address domains/hooks **by id** (D12).

| Tool | Mode | Purpose |
|---|---|---|
| `cogmem_get_domain` | read | Load one domain by **id**: summary/state/hooks (incl. hook ids). The LLM's pull path. |
| `cogmem_search` | read | Keyword/substring search (SQL scan) over active hooks. |
| `cogmem_list_domains` | read | List active/review/archived domains (id · name · summary). |
| `cogmem_explain` | read | Explain why a hook/domain (by id) is active, with evidence. |
| `cogmem_remember` | write | Add/update a durable hook from explicit instruction or strong evidence. |
| `cogmem_update_domain` | write | Typed patch (summary/blockers/next-actions/constraints) by id with `expected_version`. |
| `cogmem_retire_hook` | write | Retire a hook by id with a reason (stays audited). |
| `cogmem_create_domain` | write | Create a project/workflow/repo/profile domain; **returns the assigned id** (D5). |
| `cogmem_archive_domain` | write | Archive a domain by id, out of default prompting. |
| `cogmem_forget` | write | Retire active memory for a topic; regenerate export (D9). |
| `cogmem_consolidate` | control | Queue a consolidation run (non-blocking). |
| `cogmem_status` | read | DB health, last consolidation, degraded mode. |

`cogmem_update_domain` uses a typed patch + `expected_version` (optimistic
concurrency). `cogmem_forget` retires matching hooks and regenerates the export;
it never rewrites raw archives - physical purge is operator-only (D9).

**Never exposed:** raw SQL; raw writes to the curated files or the export; direct
`state_json` edits; archive purge; ACL/allowlist mutation; anything touching
immutable policy. Self-improvement is bounded to learned memory, not the runtime's
security model.

---

## 13. Generated Export (read-only)

The learned layer is mirrored to read-only files for human browsing:
`memory/GENERATED_PROJECTS.md`, `GENERATED_LESSONS.md`,
`GENERATED_USER_LEARNED.md`, etc. Each starts with:

```markdown
<!--
DO NOT EDIT. Autogenerated from the cognitive memory database.
Changes will be overwritten. To change memory: edit the standard .md files
(curated) or use the cogmem tools (learned).
-->
```

These are **never read back into prompts**. They exist only so the human can see
what the agent has learned. The export includes a **"Pending (unconfirmed)"**
section listing `review` items, so you can confirm or reject the agent's
inferences proactively (§11.6). The five standard files remain the editable
surface.

---

## 14. Configuration (all keys per-agent overridable)

Additive. The `memory` settings apply only to agents allowed the `cogmem` tools
(there is no on/off flag - §3.3). Keys under `agents.defaults.memory` are
overridable under `agents.list[].memory`.

```jsonc
{
  "agents": {
    "defaults": {
      "memory": {
        "prompt": {
          "top_k_domains": 3,
          "max_chars": 4000,
          "min_confidence": 0.65,
          "include_debug_trace": false,
          "pending": { "surface": "ask", "max": 8 }   // "ask" | "export_only" (§11.6)
        },
        "consolidation": {
          "model": "",                // optional; falls back to the agent's default model (D4)
          "prompt_file": "",          // optional editable consolidation-prompt override (§11.8)
          "debug_dump": false,        // write per-run prompt+response to memory/consolidation/<ts>.json
          "auto_promote": false,      // false = conservative: inferred items stay in `review` (point 3)
          "every_n_messages": 50,     // D6
          "idle_minutes": 60,         // D6
          "nightly": true,            // D6 on/off
          "nightly_at": "03:20",
          "propose_domains": true,    // D5
          "max_batch_messages": 200,
          "max_runtime_seconds": 120
        },
        "retention": { "protect_unconsolidated": true },  // D7
        "export":    { "enabled": true }
      }
    },
    "list": [
      { "id": "default",
        "tools": ["...", "cogmem_*"],   // allowing cogmem_* activates cognitive memory (D10)
        "memory": { "consolidation": { "model": "<high-reasoning model alias>" } } }
    ]
  }
}
```

Add matching structs to `AgentDefaults` and `AgentConfig` in
`pkg/config/config.go`. File-write confinement (`Tools.AllowWritePaths` →
`<workspace>/files/`, §3.4) is a tools-level default applied to cognitive agents.

---

## 15. Packages And Verified Seams

```text
pkg/cogmem/
  types.go          Domain, Hook, ComposeRequest/Result, Composer interface
  composer.go       stable block (+rev) + recency routed block + trace
  policy.go         conflict rules, prompt budget
pkg/cogmem/store/
  sqlite.go         modernc open, WAL, migrations, meta/stable_rev
  domains.go        domain CRUD + optimistic concurrency + id assignment
  hooks.go          hook lifecycle + LIKE search + id assignment
  consolidation.go  watermark, meaningful_count
  events.go         audit ledger
pkg/cogmem/consolidate/
  manager.go        triggers, scheduling, dispatch (B3)
  worker.go         leases, batching, curated-file reconciliation
  contract.go       input assembly + output JSON schema + validation (B1)
  prompt.go         consolidation prompt
pkg/tools/cogmem/
  global_provider.go
  tools.go
```

| Concern | Current code | Change |
|---|---|---|
| Static prompt | `pkg/agent/context.go` `BuildSystemPromptWithCache` | For cognitive agents append the cogmem **stable block** (incl. `MEMORY.md` verbatim, no daily notes); cache key += `stable_rev`. |
| Per-turn prompt | `pkg/agent/context.go` `BuildMessages`, `pkg/llmcontext/manager.go` `Build` | Append the cogmem **routed block** at the tail. |
| File memory | `pkg/agent/memory.go` `GetMemoryContext` | Used only for agents **without** the `cogmem` tools. |
| File access | `pkg/tools/files/global_provider.go` (`AllowWritePaths`, `RestrictToWorkspace`) | Default write-allow to `<workspace>/files/`; protect curated files, `memory/`, `sessions/` (§3.4). |
| Activation | `pkg/config/config.go` `IsToolAllowed`, agent tool allowlist | Presence of the `cogmem` provider activates the subsystem; no `engine` flag (§3.3). |
| Archive feed | `pkg/memory/archive.go`, `pkg/llmcontext/manager.go` `getOrOpenArchive` | Read the batch read-only; **add a `consolidated` column** set by the worker after each run; retention skips unconsolidated rows when `protect_unconsolidated` (point 4, D7). |
| Prompt caching | `pkg/providers/openai_compat`, `pkg/providers/common`, `ModelConfig` | Add model-level `prompt_caching`; when on, emit `cache_control` instead of stripping `SystemParts` (§17). |
| Consolidation trigger | message-append path + gateway services | Notify `consolidate.Manager`; start it with other services (§11.2). |
| Token scrubbing | existing MCP result scrubber (`pkg/mcpserver`) | Reuse to redact tokens/secrets in tool inputs and consolidation. |
| Session keys | `pkg/routing/session_key.go` | Reused verbatim to locate `.cogmem.db`. |
| Tools | `internal/gateway/tool_providers.go` | Register `pkg/tools/cogmem` under `cogmem`. |
| Config | `pkg/config/config.go` | Add `MemoryConfig` to defaults + agent config. |
| Summarization | `pkg/llmcontext` | Unchanged; complements compression. |

---

## 16. Reliability Requirements

- WAL on the memory DB; owner-only file permissions where supported.
- All multi-row updates (incl. `stable_rev` bump + watermark) commit in one
  transaction.
- Domain updates use optimistic concurrency (`version` / `expected_version`).
- Worker lease prevents concurrent sleep cycles per session.
- Invalid consolidation output never advances the watermark; partial application
  never happens.
- Compose never fails the LLM turn (curated files + always-on domains always
  render); reads use WAL read-only connections.
- With `protect_unconsolidated`, retention cannot prune unconsolidated messages;
  any gap emits a `gap` event.
- Every active hook has evidence or a migration source.
- Every write emits an audit event.
- The generated export is clearly banner-marked and never read back.
- Tool schemas enforce max lengths, enums, required reasons.
- Memory tools and the consolidator redact tokens/secrets (reuse the existing
  MCP scrubber).
- Tests cover crash/retry/idempotency.

---

## 17. Prompt-Caching Considerations

Provider caching reuses the longest stable **prefix**, so volatile content near
the front invalidates everything after it.

**Built into this design (required):**
- **Stable → volatile ordering** (§8): curated files + always-on domains + index
  in the cached region; the routed block at the tail.
- **Stable index sort** by id, never by recency.
- **Trace stays out-of-band** - never injected into the cached prompt.
- **Stable-block versioning** via `meta.stable_rev`: recompute the stable block
  only when always-on content changes (B4).

**Caveat (cost assumption):** ClawEh emits `cache_control` only on the native
Anthropic provider (`pkg/providers/anthropic/provider.go`); the OpenAI-compatible
path (used by OpenRouter) strips it (`pkg/providers/common/common.go`). So today:
OpenAI-family models via OpenRouter cache **automatically**; **Claude via
OpenRouter is not cached** and pays full input price per turn. The design
therefore does **not** depend on caching for viability - but since almost all of
your traffic is OpenRouter, the model-level flag below is the lever that makes the
always-on block cheap.

**In scope - model-level `prompt_caching` flag:**
- A per-model option (in `models[]`, `pkg/config` `ModelConfig`) that makes the
  openai_compat/OpenRouter path **emit `cache_control` breakpoints instead of
  stripping them**, so Claude/Gemini via OpenRouter get prompt caching. Off by
  default; enable per model. This closes the Claude-via-OpenRouter gap above and
  benefits every agent, not just cognitive ones.

**Related ClawEh caching improvements (separate from cogmem, optional):**
- Move the rotating callback token to the prompt tail so its rotation doesn't
  invalidate the cached persona prefix.
- Append tool-discovery-promoted tools at the end of the tool list so promotion
  turns don't bust the stable tool-definition prefix.
- Consider Anthropic's 1-hour cache TTL for sporadically-active long-running
  agents.

These four touch the provider/prompt layer, not cogmem; they benefit every agent.

---

## 18. Optional Future Phase: Smarter Retrieval (only if needed)

Not part of the core. Add **only** if an agent's memory grows past what fits in a
prompt index (many tens of active domains, or hooks the LLM can't all be shown).
It slots in behind the same `RoutedBlock` interface as an extra pre-load signal
alongside recency:

- **Keyword (FTS5):** add a synced `hooks_fts` table for keyword pre-load.
- **Vectors:** store unit-normalized `float32` BLOBs in `.cogmem.db`; score with a
  Go dot product (pure Go, no CGO, no ANN at this scale); embeddings via an
  OpenAI-compatible endpoint (e.g. OpenRouter `openai/text-embedding-3-small`,
  1536-dim), batched, content-hash cached, short timeout.

Until that threshold, the in-prompt index + LLM routing + recency is simpler,
cheaper, and at least as accurate. **Defer.**

---

## 19. Security And Privacy

- Never memorize credentials, tokens, private keys, callback URLs with tokens,
  session tokens, or API keys (validator rejects them in hook/state text).
- Memorize sensitive personal data only on explicit request and when useful.
- Memory cannot override higher-priority instructions or tool ACLs.
- The export excludes raw transcripts by default.
- `cogmem_forget` lets the user remove active memory without a human file edit.
- Raw archive purge stays an operator workflow, not an LLM tool.
- Log memory operations structurally; avoid logging full sensitive content unless
  `log_message_content` is enabled.

---

## 20. Testing And Acceptance

**Unit:** migrations; `stable_rev` bump on always-on change; domain/hook id
assignment; domain version conflicts; hook lifecycle; `LIKE` search; watermark +
meaningful_count + crash retry; conflict-rule determinism (incl. curated-file
authority); consolidation-payload schema validation (valid, invalid id, secret in
text, partial-failure abort); export banner/content; Compose stable-block (+rev)
and recency routed-block selection.

**Tools** (extend `tests/test_mcpserver.sh`): `cogmem_get_domain`/`cogmem_search`
empty + positive; `cogmem_create_domain` returns an id; `cogmem_remember` creates
a hook with evidence; `cogmem_update_domain` rejects stale `expected_version`;
`cogmem_retire_hook` removes a hook from Compose; `cogmem_forget` retires matches;
write tools reject missing session context; cogmem tools absent when the `cogmem`
provider isn't in the agent's allowlist; writes outside `<workspace>/files/`
rejected; explicit allowlists respected.

**Integration:** enable cognitive for a temp agent, verify another keeps
file-based behavior; verify the five curated files (incl. `MEMORY.md`) render
verbatim and outrank learned memory, and daily notes are omitted; run a sleep
cycle over a real archive and verify the watermark advances and `stable_rev`
bumps; invalid JSON → no watermark advance; `protect_unconsolidated` →
unconsolidated messages survive a retention pass or a `gap` event; cold-start over
a large archive completes across batches; verify the stable block stays
byte-identical across turns when nothing changed (caching).

---

## 21. Phased Implementation

- **Phase 0 - spike:** prove the stable/routed Compose split (incl. `stable_rev`
  cache keying) without breaking the existing prompt cache; verify memory-DB
  WAL/open/close with `modernc.org/sqlite`; finalize `MemoryConfig`.
- **Phase 1 - store + Compose:** `pkg/cogmem/store` schema/migrations + id
  assignment + `stable_rev`; `Composer` (stable block + index + recency routed
  block + trace); activation via the `cogmem` tool allowlist; curated files
  verbatim + authoritative (incl. `MEMORY.md`, no daily notes); file writes
  confined to `<workspace>/files/`; unit + one integration test. Prompts become
  relevance-scoped.
- **Phase 2 - MCP tools:** `pkg/tools/cogmem` (id-addressed) with validation,
  evidence, audit; register; extend MCP tests; cognitive-only gating.
- **Phase 3 - sleep cycle:** `consolidate.Manager` (triggers/scheduling, B3),
  worker leases, the consolidation contract (B1) + JSON validation, conflict rules
  + curated-file reconciliation, transactions, domain proposals (D5), retention
  guard (D7), cold-start backlog, export, metrics. Agents now learn automatically.
- **Phase 4 - rollout:** operator docs (enable, inspect, roll back); A/B cognitive
  vs file-based on a non-critical agent by enabling its `cogmem` tools. Agents
  without the `cogmem` tools keep today's file-based behavior, so rollout is per
  agent and reversible by toggling the allowlist.
- **Phase 5 (optional, deferred):** smarter retrieval (§18), only if scale demands.

---

## 22. Summary

A small, reliable subsystem - not an "agent brain" rewrite:

- keep the five standard `.md` files as the human-curated, verbatim, authoritative
  layer (full compatibility);
- add one per-session DB for the learned layer beside the existing archive;
- learn in the background via consolidation against a strict I/O contract:
  distill, de-dup, resolve contradictions, audit - the source of "smarter over
  time";
- retrieve with an in-prompt id·name index + LLM self-routing + recency pre-load
  and a load-by-id tool - no embeddings, no FTS, no external service;
- order the prompt stable → volatile so caching is preserved;
- the agent writes learned memory to the DB, never to your files; you edit the
  files, and they always win;
- expose safe, id-addressed MCP tools, never raw storage access;
- keep keyword/vector retrieval as a clean, deferred option for if scale ever
  demands it.

This makes ClawEh agents durably smarter over time while staying simple,
debuggable, reversible, and pure Go.
