# Cognitive Memory Engine - Revised Architecture Proposal

**Status:** Draft v3, reviewer comments incorporated
**Project:** ClawEh (`github.com/PivotLLM/ClawEh`)
**Date:** 2026-06-13
**Objective:** Add a production-grade cognitive memory system so each agent
periodically reviews its conversation history, learns durable preferences and
project state, and improves future turns without manual edits to markdown files.

This proposal replaces static, ever-growing prompt memory with a per-agent,
database-backed memory engine. The engine is opt-in, pure Go, no CGO, and
implemented behind narrow seams so the existing file-based behavior remains the
default until an agent explicitly enables cognitive memory.

All inline review notes from the previous draft have been folded into the
design: optimized pure-Go vector search remains the recommendation,
optimistic concurrency is now required, generated markdown gets an explicit
do-not-edit banner, and the sleep cycle now runs on message count, idle time,
and a nightly sweep.

---

## 1. Executive Summary

ClawEh already persists each session to a SQLite archive under
`<workspace>/sessions/<key>.archive.db`. Today those archives are useful for
history search and summarization, but the agent's long-term operating memory is
still assembled from static workspace markdown: `AGENTS.md`, `USER.md`,
`IDENTITY.md`, `SOUL.md`, `MEMORY.md`, and recent daily notes. Those files are
hand-curated, context-insensitive, and contradiction-prone.

The Cognitive Memory Engine adds a closed loop:

1. **Observe:** Read new messages and tool results from the existing session
   archive databases. Do not add any new hot-path write burden.
2. **Consolidate:** Periodically distill unconsolidated messages into structured
   per-agent memory: user preferences, project state, workflow rules, lessons
   learned, and durable facts. Every mutation records evidence and an audit
   event.
3. **Compose:** On each LLM turn, inject only the baseline persona plus memory
   relevant to the current turn, bounded by a hard prompt budget.
4. **Act immediately:** Give the agent safe MCP tools for explicit "remember
   this", "forget that", and "update project state" operations without exposing
   raw SQL or raw file writes.

The agent becomes smarter because repeated corrections, project-specific facts,
and successful operating patterns are promoted into durable memory. It remains
safe because memory writes are validated, versioned, auditable, reversible, and
scoped per agent.

---

## 2. Design Principles

- **Opt-in and reversible:** `memory.engine: "files"` stays the default.
  `memory.engine: "cognitive"` enables this engine per agent.
- **Pure Go, no CGO:** Keep using `modernc.org/sqlite`. Store vectors as BLOBs
  and score them in Go.
- **No hot-path hard network dependency:** Compose works with FTS5 alone.
  Embeddings improve recall, but failures degrade to keyword routing.
- **Evidence first:** Every learned item must cite source session IDs and
  sequence ranges or an explicit agent tool write.
- **Structured state, not append-only prose:** Hooks and domain state are
  normalized and mutable; old contradictions are retired, not kept active.
- **Immutable authority boundary:** Cognitive memory cannot override system,
  developer, sandbox, ACL, or tool safety instructions. It can only add learned
  operating preferences and domain knowledge below those authorities.
- **Crash-safe and idempotent:** Consolidation is watermark-driven and commits
  state, audit events, and watermarks in one transaction.
- **Human inspectability:** Markdown remains as generated, read-only mirrors of
  what the agent believes, not as the prompt source of truth.

---

## 3. What Counts As Memory

Memory is only durable, reusable knowledge that should change future behavior.
The sleep cycle and write tools should preserve:

| Kind | Examples | Prompt treatment |
|---|---|---|
| `preference` | "Eric prefers concise final answers." | Often relevant, high priority |
| `rule` | "Do not deploy this project on Fridays." | High priority when domain matches |
| `fact` | "The BioTech report targets Q3." | Relevant by project/domain |
| `project_state` | Summary, blockers, next actions, decisions | Relevant by project/domain |
| `workflow` | "For PR reviews, findings first." | Relevant by task type |
| `lesson` | "This repo uses `rg`; avoid grep for searches." | Relevant by repo/tooling |
| `profile` | Stable user or team profile facts | Relevant when user-facing |

The engine must reject or down-rank:

- greetings, filler, jokes, and one-off conversational glue;
- temporary instructions that were only for the current turn;
- stale implementation guesses contradicted by later code inspection;
- secrets, API keys, session tokens, callback tokens, and credentials;
- generated markdown mirror contents, because the DB is the source of truth.

Memory should be conservative. It is better to skip a weak candidate than to
persist noise that will bias every future turn.

---

## 4. Core Concepts

- **Agent:** A named ClawEh assistant with its own workspace, model, tools, and
  session archives. Cognitive memory is owned per agent.
- **Memory domain:** A coherent body of long-lived knowledge owned by one agent:
  user profile, a project, a workflow, a repo, a research stream, or learned
  operating preferences.
- **Hook:** A short, active memory statement used for retrieval and prompt
  composition. Hooks are rules, facts, preferences, lessons, or state pointers.
- **Domain state:** Structured JSON for summary, blockers, next actions,
  constraints, and domain-specific fields. Validated by schema version.
- **Baseline domain:** Always-loaded learned operating preferences and persona
  mirror. It may supplement, but never replace, immutable system/developer
  instructions.
- **Evidence:** Source references proving why a hook or state change exists.
  Evidence points to `(session_key, seq_start, seq_end)` or to a tool mutation
  event.

---

## 5. Data Model

Use one cognitive memory database per agent, for example:

`<workspace>/memory/cogmem.db`

The session archives stay exactly where they are today:

`<workspace>/sessions/<session-key>.archive.db`

### 5.1 Required Tables

```sql
-- Schema and feature gates.
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

-- One row per memory domain.
CREATE TABLE IF NOT EXISTS domains (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  type TEXT NOT NULL,              -- baseline, user_profile, project, workflow, repo, lesson
  name TEXT NOT NULL,
  status TEXT NOT NULL,            -- active, archived, review
  version INTEGER NOT NULL,        -- optimistic concurrency control
  summary TEXT NOT NULL DEFAULT '',
  state_json TEXT NOT NULL DEFAULT '{}',
  schema_name TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  archived_at INTEGER
);

-- Retrieval and mutation unit.
CREATE TABLE IF NOT EXISTS hooks (
  id TEXT PRIMARY KEY,
  domain_id TEXT NOT NULL REFERENCES domains(id),
  kind TEXT NOT NULL,              -- preference, rule, fact, project_state, workflow, lesson
  text TEXT NOT NULL,
  status TEXT NOT NULL,            -- active, retired, review, rejected
  confidence REAL NOT NULL,        -- 0.0 to 1.0
  priority INTEGER NOT NULL,       -- prompt ordering
  source TEXT NOT NULL,            -- user_explicit, assistant_inferred, tool_write, migration
  source_session TEXT,
  source_seq_start INTEGER,
  source_seq_end INTEGER,
  supersedes_hook_id TEXT,
  retire_reason TEXT,
  content_hash TEXT NOT NULL,
  needs_reembed INTEGER NOT NULL DEFAULT 1,
  embedding BLOB,
  embedding_model TEXT,
  embedding_dim INTEGER,
  embedding_updated_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- FTS5 index over active hook text.
CREATE VIRTUAL TABLE IF NOT EXISTS hooks_fts USING fts5(
  text,
  kind,
  domain_name,
  content='hooks',
  content_rowid='rowid'
);

-- Per-session consolidation watermarks.
CREATE TABLE IF NOT EXISTS session_watermarks (
  session_key TEXT PRIMARY KEY,
  archive_path TEXT NOT NULL,
  consolidated_seq INTEGER NOT NULL,
  last_seen_seq INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Append-only audit log.
CREATE TABLE IF NOT EXISTS memory_events (
  id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,        -- create, update, retire, merge, reject, conflict_resolved
  domain_id TEXT,
  hook_id TEXT,
  old_json TEXT,
  new_json TEXT,
  reason TEXT NOT NULL,
  evidence_json TEXT NOT NULL,
  actor TEXT NOT NULL,             -- sleep_cycle, mcp_tool, migration, operator
  model TEXT,
  prompt_hash TEXT,
  created_at INTEGER NOT NULL
);

-- Async embedding work.
CREATE TABLE IF NOT EXISTS embedding_jobs (
  id TEXT PRIMARY KEY,
  hook_id TEXT NOT NULL,
  status TEXT NOT NULL,            -- queued, running, done, failed
  attempts INTEGER NOT NULL,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Worker leases to prevent concurrent sleep cycles from clobbering each other.
CREATE TABLE IF NOT EXISTS worker_leases (
  name TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
```

Implementation can adjust exact column names, but the concepts are mandatory:
domain versions, hook lifecycle, evidence, audit events, watermarks, and worker
leases.

### 5.2 Why Not One JSON Blob Per Project

The source notes proposed a single `projects.metadata` JSON column. That is
easy to prototype but weak for production:

- conflicts are harder to audit at item level;
- retrieval wants hook-level FTS and vector scoring;
- retiring one bad memory should not rewrite unrelated state;
- optimistic concurrency is cleaner when domains and hooks have versions.

Keep `state_json` for structured domain state, but represent prompt-retrievable
knowledge as hooks.

---

## 6. Retrieval And Vector Decision

Retrieval is hybrid:

1. deterministic domain lookups;
2. SQLite FTS5/BM25 over active hooks;
3. optional semantic vector scoring over hook embedding BLOBs;
4. reciprocal-rank or weighted score fusion;
5. prompt budget trimming by priority, relevance, recency, and confidence.

### 6.1 FTS5 Is The Required Baseline

The archive store already uses SQLite FTS5. The memory DB should do the same for
hook text. FTS5 gives useful routing with zero embeddings and no network calls.

### 6.2 Vectors Are Optional And Pure Go

Store normalized `float32` embeddings as compact BLOBs in SQLite. At query time,
load candidate active hooks and compute dot products in Go. Because vectors are
unit-normalized on write, dot product equals cosine similarity.

This is the recommended vector path:

- no CGO;
- no loadable SQLite extension;
- no second database file format;
- single-file SQLite backup and WAL durability;
- enough speed for distilled memory scale.

The previous reviewer comment about assembly-optimized dot products is a good
future optimization. Start with pure Go and benchmark. If CPU cost becomes
visible, add architecture-specific fast paths behind the same scorer interface
without changing storage or Compose.

### 6.3 Rejected Vector Alternatives

| Option | Decision |
|---|---|
| `sqlite-vec` loadable extension | Rejected for this phase. Official Go docs list CGO and ncruces/WASM integration paths, not this repo's `modernc.org/sqlite` driver. It is also a pre-v1 extension, which adds operational risk. |
| `mattn/go-sqlite3` plus `sqlite-vec` | Rejected. Reintroduces CGO. |
| ncruces SQLite WASM plus `sqlite-vec` | Rejected. It would replace the project's existing SQLite driver for this subsystem. |
| chromem-go or another embedded vector DB | Acceptable fallback, not preferred. It adds another persistence engine and backup path. |
| Pure-Go HNSW/ANN | Not needed until memory grows to hundreds of thousands or millions of active hooks. |
| SQLite BLOB plus Go dot product | Recommended. Simple, durable, and enough for the target scale. |

Reference: `sqlite-vec` Go integration documentation:
https://alexgarcia.xyz/sqlite-vec/go.html

### 6.4 Hot-Path Latency Rule

Compose must never block the user turn on a slow embedding request. If query
embedding is enabled, use:

- content-hash cache;
- short timeout, default 250 ms or lower;
- FTS-only fallback on timeout, rate limit, auth failure, or invalid dimension;
- no retry loop in the hot path.

Embedding hooks after memory writes is background work. Failed embeddings leave
FTS routing functional.

---

## 7. Observe: Reading All Messages Safely

The source feed is the existing per-session archive DB. Each archive has
sequence-numbered messages, payload JSON, text, created timestamps, and an FTS5
table. The cognitive engine should open archives read-only.

The worker scans:

`<workspace>/sessions/*.archive.db`

For each archive:

1. read current min/max sequence bounds;
2. look up `session_watermarks.consolidated_seq`;
3. read `(consolidated_seq + 1)..max_seq`;
4. pass complete messages, tool calls, tool outputs, role, source, and seq
   metadata into consolidation;
5. commit memory changes and the new watermark together.

### 7.1 Archive Retention Guard

The current archive layer has retention caps. Cognitive memory must not silently
miss messages that are pruned before consolidation. Add one of these guards
before rollout:

- preferred: archive pruning for cognitive agents may delete only messages at or
  below that session's consolidated watermark;
- acceptable: configure retention large enough that the sleep cycle always has
  time to process, and emit a high-severity `memory_gap` event if a watermark is
  behind the archive's minimum sequence.

If a gap is detected, the worker records it in `memory_events` and continues.
It must never pretend skipped messages were reviewed.

---

## 8. Consolidate: The Sleep Cycle

The sleep cycle is where the agent learns. It is a background worker, not part
of the user-turn hot path.

### 8.1 Triggers

Run consolidation when any trigger fires:

- **message threshold:** default every 40 meaningful new messages per agent;
- **idle trigger:** default after 5 minutes without a user message;
- **nightly sweep:** default once per night, with jitter;
- **manual trigger:** via a safe MCP tool that queues work, not a long blocking
  tool call.

The idle trigger incorporates the reviewer comment from v2. It avoids spending
rate limits while the user is actively chatting and updates memory as soon as a
session pauses.

### 8.2 Worker Lease

Each agent gets at most one active consolidation worker. Use a SQLite lease with
an expiry, owner ID, and heartbeat. This prevents:

- nightly and idle workers racing;
- an MCP-triggered consolidation clobbering a scheduled run;
- two process instances updating the same domain concurrently.

### 8.3 Consolidation Algorithm

For each agent:

1. acquire the agent worker lease;
2. find sessions with new archive messages beyond their watermark;
3. batch messages by session and sequence range;
4. load relevant existing domains and hooks;
5. call a configured high-reasoning model in JSON-object mode when supported;
6. validate the returned payload locally against JSON Schema;
7. apply deterministic conflict rules;
8. commit domain updates, hook changes, audit events, and watermarks in one
   transaction;
9. enqueue embedding jobs for changed hooks;
10. regenerate markdown mirrors if enabled.

Provider-side JSON mode is useful but not sufficient. Local schema validation is
required because not every configured provider supports strict schema output.
Invalid output aborts that unit and leaves the watermark unchanged for retry.

### 8.4 Conflict Rules

The consolidation prompt and deterministic post-processor must apply these
rules:

1. immutable system/developer/tool safety instructions outrank all memory;
2. explicit user instruction outranks assistant inference;
3. newer explicit instruction outranks older explicit instruction at the same
   scope;
4. domain-specific instruction outranks global preference for that domain;
5. high-confidence evidence outranks low-confidence inference;
6. contradictions retire or supersede old hooks instead of leaving both active;
7. uncertain conflicts move to `review` status and are omitted from prompts.

Every conflict resolution writes a `memory_events` row describing the old value,
new value, evidence, and reason.

### 8.5 Optimistic Concurrency

Domains have a `version`. The worker reads a domain version before calling the
LLM and includes that version in the transaction. If another writer updated the
domain first, the transaction fails cleanly and the worker reloads and retries.

This directly addresses the previous reviewer comment about preventing the sleep
cycle from clobbering live MCP tool writes.

### 8.6 Self-Improvement Signals

The sleep cycle should explicitly look for operational lessons that improve the
agent, not just factual memory. Good candidates include:

- repeated user corrections about tone, format, or workflow;
- tool failures followed by a successful alternate approach;
- repo-specific implementation patterns the agent had to rediscover;
- user-approved decisions that should shape future planning;
- recurring blockers and their known fixes;
- tasks where the agent used too many turns and later found a shorter path.

These become `lesson` or `workflow` hooks only when they are durable and scoped.
The worker should avoid overfitting one-off mistakes. Low-confidence lessons go
to `review` status and stay out of prompts until confirmed by later evidence or
an explicit `cogmem_remember` call.

---

## 9. Compose: Per-Turn Prompt Memory

Compose is the read path. It decides what learned memory enters the system
prompt for this turn.

### 9.1 Current Code Seam

The current code has two important seams:

- `pkg/agent/context.go` builds the static system prompt from identity,
  bootstrap files, skills, and file memory.
- `pkg/agent/context.go:BuildMessages(...)` adds dynamic context and the
  session summary each turn.
- `pkg/llmcontext/manager.go:Build(ctx)` fetches current history after the user
  message has already been saved.

Because cognitive memory is relevant to the current user turn, it should not be
called from the static `BuildSystemPromptWithCache()` path. It should be called
from the per-turn message build path, using the latest user message in current
history as the routing text.

Recommended seam:

```go
// package cogmem
type ComposeRequest struct {
    AgentID      string
    Workspace    string
    SessionKey   string
    Channel      string
    ChatID       string
    RouteText    string // latest user message or explicit currentMessage
    MaxChars     int
    IncludeDebug bool
}

type Composer interface {
    Compose(ctx context.Context, req ComposeRequest) (ComposeResult, error)
}

type ComposeResult struct {
    Text           string
    DomainVersions map[string]int64
    Degraded       bool
    Reason         string
}
```

Wire this through `llmcontext.Manager.Build(ctx)` or a small optional extension
to `MessageBuilder`. The static prompt cache remains for identity, bootstrap,
skills, and immutable sections. Learned context is a dynamic block with its own
cache keyed by route hash plus selected domain versions.

### 9.2 Compose Output Shape

The composed memory block should be concise and deterministic:

```markdown
# Learned Memory

## Stable Preferences
- ...

## Relevant Context
- ...

## Active Project State
Summary: ...
Blockers:
- ...
Next actions:
- ...
```

Do not include low-confidence or `review` hooks. Do not include raw source
transcripts. Source IDs may be included only in debug mode.

### 9.3 Prompt Budgeting

Configurable defaults:

- `top_k_domains: 3`
- `top_k_hooks_per_domain: 12`
- `max_chars: 4000`
- `min_confidence: 0.65`

Trimming order:

1. baseline critical preferences;
2. active domain state for exact matches;
3. high-priority rules;
4. high-scoring hooks;
5. recent project next actions;
6. lower priority facts.

Compose must return a valid string even on internal failure. On DB failure, it
returns either the migrated baseline or an empty learned-memory block and logs a
structured warning.

---

## 10. Agent MCP Tools

The LLM should have a small, high-level memory tool surface. It must not get raw
SQL, raw memory-file writes, or direct vector index operations.

Implement these as a transport-neutral provider under `pkg/tools/cogmem`, then
mount it in `internal/gateway/tool_providers.go`:

```go
tools.RegisterProvider(tools.NamespacedProvider("cogmem", cogmem.GlobalProvider))
```

Published tool names are therefore `cogmem_search`, `cogmem_remember`, etc.
The provider should build tools only for agents with
`memory.engine: "cognitive"`. All write tools should be session-scoped when
possible so the server can attach source evidence automatically.

### 10.1 Recommended Tool Set

| Tool | Mode | Default for cognitive agents | Purpose |
|---|---|---:|---|
| `cogmem_search` | read | yes | Search active learned memory by text, kind, and domain. |
| `cogmem_list_domains` | read | yes | List active/archived/review domains. |
| `cogmem_get_domain` | read | yes | Read one domain's summary, state, hooks, and versions. |
| `cogmem_explain` | read | yes | Explain why a hook or domain is active, with evidence and events. |
| `cogmem_remember` | write | yes | Add or update one durable hook from an explicit user instruction or strong evidence. |
| `cogmem_update_domain` | write | yes | Patch summary, blockers, next actions, or constraints with `expected_version`. |
| `cogmem_retire_hook` | write | yes | Retire a hook with a reason; old hook leaves prompts but remains audited. |
| `cogmem_create_domain` | write | yes | Create a project/workflow/profile/repo domain. |
| `cogmem_archive_domain` | write | yes | Archive a domain so it is no longer prompted by default. |
| `cogmem_forget` | write/privacy | yes | Remove active memory for a user-requested topic; optionally purge generated mirrors. |
| `cogmem_consolidate` | control | yes | Queue a consolidation run for current session or agent. Non-blocking. |
| `cogmem_status` | read | yes | Show DB health, last consolidation, queue depth, and degraded mode. |

### 10.2 Tool Contract Details

`cogmem_remember`

- Required: `kind`, `text`, `reason`.
- Optional: `domain_id`, `domain_hint`, `confidence`, `priority`, `ttl_days`.
- Server supplies: `agent_id`, `session_key`, source sequence window when
  available, actor `mcp_tool`.
- Validation: max text length, enum kind, no secrets/tokens, min confidence for
  active status.
- Behavior: upsert or create a hook, retire directly conflicting hooks, append
  audit events, enqueue embedding.

`cogmem_update_domain`

- Required: `domain_id`, `expected_version`, `patch`, `reason`.
- Patch is typed, not arbitrary SQL. Allowed top-level operations:
  `set_summary`, `set_blockers`, `set_next_actions`, `add_constraint`,
  `remove_constraint`, `set_field`.
- Behavior: validate schema, compare version, commit or return version conflict.

`cogmem_forget`

- Required: `query`, `reason`.
- Optional: `domain_id`, `mode`.
- Modes:
  - `active_only`: retire matching hooks and remove from prompts;
  - `mirror_only`: regenerate generated markdown without matching items;
  - `operator_purge_requested`: record an audit event for an operator/admin
    purge workflow. The LLM should not physically rewrite raw archives.

`cogmem_consolidate`

- Required: `scope` (`current_session` or `agent`).
- Optional: `max_messages`.
- Behavior: enqueue a job and return immediately. The worker lease handles
  concurrency.

### 10.3 Tools The LLM Must Not Have

Do not expose:

- raw SQL execution against memory DB;
- raw writes to `MEMORY.md`, `USER.md`, `AGENTS.md`, or generated mirrors;
- direct edit of `domains.state_json` without schema validation;
- direct vector insert/delete/reindex tools;
- direct archive purge tools;
- ACL or tool-allowlist mutation tools;
- any tool that can modify immutable system/developer policy.

This keeps self-improvement bounded to learned memory, not self-modification of
the runtime's security model.

---

## 11. Embeddings Subsystem

Embeddings are a separate package:

`pkg/cogmem/embed`

Define:

```go
type Embedder interface {
    Embed(ctx context.Context, text string) (Vector, error)
    Model() string
    Dim() int
}
```

Initial implementation:

- OpenAI-compatible `/embeddings` HTTP endpoint;
- configured separately from chat/completion providers;
- uses existing HTTP/proxy/credential patterns where practical;
- caches `hash(model, text) -> vector`;
- pins model and dimension per agent DB;
- queues full re-embed when model or dimension changes.

CLI providers do not expose embeddings. A local/offline embedder can be added
later behind the same interface.

---

## 12. Generated Markdown Mirrors

Markdown should remain for human inspection, but the DB is the source of truth.

Generated files can include:

- `memory/GENERATED_USER.md`
- `memory/GENERATED_PROJECTS.md`
- `memory/GENERATED_WORKFLOWS.md`
- `memory/GENERATED_LESSONS.md`

Every generated file must start with a large warning:

```markdown
<!--
DO NOT EDIT.
This file is autogenerated from the cognitive memory database.
Manual changes will be overwritten by the next memory sync.
Use the agent or cogmem tools to update memory.
-->
```

This incorporates the previous reviewer comment. The generated files are not
read back into the prompt when cognitive memory is enabled.

Migration can seed the DB from existing markdown once, then write generated
mirrors beside the legacy files. Do not delete legacy files during rollout.

---

## 13. Configuration Sketch

Additive and default-off:

```jsonc
{
  "agents": {
    "defaults": {
      "memory": {
        "engine": "files", // "files" | "cognitive"
        "prompt": {
          "top_k_domains": 3,
          "top_k_hooks_per_domain": 12,
          "max_chars": 4000,
          "min_confidence": 0.65,
          "include_debug_sources": false
        },
        "consolidation": {
          "enabled": true,
          "models": ["<high-reasoning model alias>"],
          "every_n_messages": 40,
          "idle_after_seconds": 300,
          "nightly": "03:20",
          "max_batch_messages": 200,
          "max_runtime_seconds": 120
        },
        "embeddings": {
          "enabled": false,
          "provider": "openai",
          "model": "text-embedding-3-small",
          "dim": 1536,
          "timeout_ms": 250
        },
        "mirrors": {
          "enabled": true
        }
      }
    },
    "list": [
      {
        "id": "default",
        "memory": {
          "engine": "cognitive"
        }
      }
    ]
  }
}
```

Add matching structs to `AgentDefaults` and `AgentConfig`, with per-agent values
overriding defaults. If an operator explicitly sets an agent `tools` allowlist,
they must include the desired `cogmem_*` tools just like any other tool.

---

## 14. Integration Plan

### 14.1 Packages

```text
pkg/cogmem/
  types.go             Domain, Hook, ComposeRequest, Composer interfaces
  composer.go          hybrid retrieval and prompt rendering
  policy.go            memory policy, conflict rules, prompt budget rules

pkg/cogmem/store/
  sqlite.go            modernc SQLite open, WAL, migrations
  domains.go           domain CRUD and optimistic concurrency
  hooks.go             hook lifecycle, FTS5, embedding BLOB IO
  watermarks.go        archive session watermarks
  events.go            audit ledger

pkg/cogmem/embed/
  embedder.go          interface, vector serialization, normalization
  http.go              OpenAI-compatible embeddings client
  cache.go             content-hash cache

pkg/cogmem/consolidate/
  worker.go            leases, triggers, batching
  prompt.go            consolidation prompt
  validate.go          schema validation and repair/retry policy

pkg/tools/cogmem/
  global_provider.go   transport-neutral tool provider
  tools.go             tool handlers and schemas
```

### 14.2 Verified Code Seams

| Concern | Current code | Proposed change |
|---|---|---|
| Static prompt | `pkg/agent/context.go:BuildSystemPromptWithCache` | Keep for identity/bootstrap/skills and file mode. |
| Per-turn prompt | `pkg/agent/context.go:BuildMessages` plus `pkg/llmcontext/manager.go:Build` | Call cognitive `Composer` here, using latest user message/history for route text. |
| File memory | `pkg/agent/memory.go:GetMemoryContext` | Used only when `memory.engine == "files"`. |
| Raw archive feed | `pkg/memory/archive.go`, `pkg/llmcontext/manager.go:getOrOpenArchive` | Open read-only from sleep cycle; no new hot-path writes. |
| Tools | `internal/gateway/tool_providers.go` | Register `pkg/tools/cogmem` provider under `cogmem` namespace. |
| MCP auth/session | `pkg/mcpserver/tools.go`, session token store | Memory write tools should be session-scoped to capture evidence. |
| Config | `pkg/config/config.go` | Add `MemoryConfig` to defaults and agent config. |
| Summarization | `pkg/llmcontext` | Keep unchanged; cognitive memory complements summaries. |

---

## 15. Reliability Requirements

The implementation is not acceptable unless these are true:

- SQLite WAL mode enabled for the memory DB.
- Memory DB file permissions are owner-only where the OS supports it.
- All multi-row updates commit in one transaction.
- Domain updates use optimistic concurrency.
- Worker leases prevent concurrent sleep cycles per agent.
- Invalid LLM consolidation output never advances a watermark.
- Embedding failures never block memory state updates.
- Compose never fails the LLM turn.
- Archive retention cannot silently prune unconsolidated messages.
- Every active hook has evidence or an explicit migration source.
- Every write emits an audit event.
- Generated markdown is clearly marked as generated.
- Tool schemas enforce max lengths, enums, and required reasons.
- Memory tools redact tokens and refuse obvious secrets.
- Tests cover crash/retry/idempotency behavior.

---

## 16. Security And Privacy

Cognitive memory increases the blast radius of bad writes, so the safety policy
must be explicit:

- Do not memorize credentials, tokens, private keys, callback URLs with tokens,
  session tokens, or API keys.
- Do not memorize sensitive personal data unless the user explicitly asks the
  agent to remember it and it is useful for future assistance.
- Do not let memory override higher-priority instructions or tool ACLs.
- Do not expose raw source transcripts in generated mirrors by default.
- Keep `cogmem_forget` available to cognitive agents so the user can remove
  active memory without waiting for a human to edit files.
- Keep raw archive purge as an operator/admin workflow, not an LLM tool.
- Log memory operations structurally, but avoid logging full sensitive content
  unless `log_message_content` is enabled.

---

## 17. Testing And Acceptance Criteria

### 17.1 Unit Tests

- store migrations from empty DB and older schema versions;
- domain version conflicts;
- hook create/update/retire lifecycle;
- FTS5 search and ranking;
- vector BLOB serialization, normalization, and dot product;
- content-hash embedding cache;
- worker watermarks and retry after crash;
- conflict rule determinism;
- generated markdown banner and content.

### 17.2 Tool Tests

Extend `tests/test_mcpserver.sh` and Go tool tests to cover:

- `cogmem_search` no results and positive results;
- `cogmem_remember` creates a hook with evidence;
- `cogmem_update_domain` rejects stale `expected_version`;
- `cogmem_retire_hook` removes hook from Compose;
- `cogmem_forget` retires matching active hooks;
- write tools reject missing session context when evidence is required;
- tools are unavailable when `memory.engine == "files"`;
- explicit agent tool allowlists are respected.

### 17.3 Integration Tests

- Enable cognitive memory for a temp agent and verify file mode remains
  unchanged for another agent.
- Seed memory from markdown and verify Compose replaces file memory only for the
  cognitive agent.
- Run a sleep cycle over a real archive DB and verify watermarks advance.
- Simulate an LLM invalid JSON response and verify no watermark advances.
- Simulate embedder outage and verify Compose uses FTS-only memory.
- Simulate archive retention and verify unconsolidated messages are protected or
  a `memory_gap` event is emitted.

### 17.4 Performance Targets

Initial targets on a typical developer machine:

- Compose FTS-only p95 under 25 ms for 10,000 active hooks.
- Compose hybrid p95 under 75 ms when query embedding is cached.
- Dot-product scoring benchmarked at 5,000, 50,000, and 250,000 hooks.
- Sleep cycle batches capped so user-turn latency is unaffected.

These are targets, not claims. Phase 0 must benchmark them in this repo.

---

## 18. Phased Implementation

### Phase 0 - Spike The Risky Seams

- Prove prompt integration at the per-turn seam without breaking static prompt
  caching.
- Benchmark BLOB vector decode plus Go dot product at target hook counts.
- Verify memory DB WAL/open/close behavior with `modernc.org/sqlite`.
- Decide exact `MemoryConfig` structs.

### Phase 1 - Store And FTS-Only Compose

- Implement `pkg/cogmem/store` schema, migrations, hooks, domains, events, and
  watermarks.
- Implement `Composer` with deterministic lookup and FTS5 routing only.
- Wire cognitive mode behind config; file mode unchanged.
- Add unit tests and one integration test.

### Phase 2 - MCP Tool Surface

- Implement `pkg/tools/cogmem` global provider.
- Add read/write tools with validation, evidence capture, and audit events.
- Register provider and extend MCP tests.
- Ensure tools are only built for cognitive agents.

### Phase 3 - Sleep Cycle

- Implement worker leases, message scanning, idle/message/nightly triggers,
  JSON validation, conflict rules, transactions, and generated mirrors.
- Add retention guard or gap detection.
- Add operational metrics and logs.

### Phase 4 - Embeddings

- Implement embedder interface, HTTP embedding client, content-hash cache, BLOB
  vectors, background embedding jobs, and hybrid rank fusion.
- Keep FTS-only fallback as the reliability baseline.

### Phase 5 - Migration, Docs, And Rollout

- One-shot migration from existing markdown into domains/hooks.
- Operator docs for enabling per agent, inspecting memory, and rolling back.
- A/B test cognitive vs file mode on a non-critical agent.
- Default remains file mode until confidence is high.

After Phase 1, prompts can be relevance-scoped. After Phase 3, agents learn
automatically from periodic review. After Phase 4, semantic recall improves
without becoming a reliability dependency.

---

## 19. Decisions To Confirm

Recommended defaults:

- per-agent memory DB, not global shared memory;
- FTS5 required, embeddings optional;
- BLOB vectors plus Go dot product, no CGO vector extension;
- sleep cycle on 40 meaningful messages, 5 idle minutes, and nightly sweep;
- generated markdown mirrors enabled;
- cognitive memory opt-in per agent.

Open decisions:

- Which high-reasoning model alias should run consolidation?
- Should embeddings be enabled in the first rollout or after FTS-only learning
  proves stable?
- What is the operator policy for raw archive purge requests after
  `cogmem_forget`?

---

## 20. Summary

The best implementation path is not a monolithic "agent brain" rewrite. It is a
small, reliable memory subsystem:

- reuse existing per-session archives as the source feed;
- add one structured per-agent memory DB;
- retrieve with FTS5 first and vectors second;
- compose memory per turn, not as a static prompt cache artifact;
- expose safe MCP tools, not raw storage access;
- consolidate in background with evidence, versions, and audit events.

This gives ClawEh agents durable learning and continuous improvement while
keeping the system debuggable, reversible, and implementable in the current Go
architecture.
