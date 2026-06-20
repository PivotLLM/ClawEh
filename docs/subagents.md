# Sub-agents (spawn)

A **sub-agent** is a copy of an agent run on a focused task in an **isolated
session**. It is the same agent — same workspace, tools, MCP access, and curated
identity/prompt — differing only in:

- a **fresh context** (its conversation starts from the given task, with none of
  the primary's chat history), and
- an **optional model** (run the worker on a different one of the agent's
  configured models).

Use it to do a self-contained piece of work without polluting the main
conversation, or to run a heavier model for a demanding subtask while the primary
stays responsive. Nothing the sub-agent says or does appears in the primary's
conversation; only its final result is reported back.

## The `agent_spawn` tool

| Parameter | Meaning |
|---|---|
| `task` | The instructions for the worker (required). |
| `mode` | `callback` (default) — run in the background, notify when done with a result-file pointer; or `wait` — run to completion and return the result synchronously. |
| `name` | Short identifier (required for `callback` mode). |
| `agent` | Optional target agent id. Defaults to **yourself** (a self-spawn). Targeting another agent requires authorization. |
| `model` | Optional model (by name) for the worker — must be one of the executing agent's configured models. Omit to use the agent's default. |

Results: in `callback` mode the worker's output is written to a result file in
the workspace and a pointer is delivered back (also pollable via `agent_status` /
listable via `agent_list`); in `wait` mode the result is returned directly. Either
way the result includes the worker's **iteration count**.

## What a sub-agent has access to

A sub-agent runs through the **full agent pipeline**, so it inherits:

- **Tools** — the agent's full tool registry, **minus primary-only tools** (see
  below). Includes file tools, web, MCP/fusion tools, etc.
- **Files** — read-write `files/` and read-only `skills/`, the same as the
  primary.
- **Identity / prompt** — the same curated `SOUL.md` / `AGENTS.md` /
  `IDENTITY.md` / `USER.md` / `MEMORY.md` context.
- **Memory (read-only)** — at spawn time the agent's cognitive memory
  (`cogmem`) is **snapshotted** onto the sub-agent's own private database, so the
  worker has the agent's domains, preferences, and project state as background. It
  can **read** memory (`cogmem_memory_search`, `cogmem_domain_get`, …) but
  **cannot write** it — the write tools are primary-only. The snapshot is a copy,
  so the worker never affects the primary's memory, and it is deleted when the
  worker finishes. Workers report findings back in their result; the **primary**
  decides what to persist to memory.

## What a sub-agent cannot do (primary-only)

These tools are marked `PrimaryOnly` and are **excluded from sub-agents** —
enforced both for API-model agents (the tool is not offered and is rejected if
attempted) and for CLI-model agents that reach tools over MCP (the MCP dispatch
rejects them):

- **`agent_spawn`** — a sub-agent cannot spawn further sub-agents (no recursion).
- **`cron_schedule`** — a transient worker cannot create/manage scheduled jobs.
- **cognitive-memory write tools** — `cogmem_memory_create`,
  `cogmem_domain_update`, `cogmem_domain_create`, `cogmem_domain_archive`,
  `cogmem_domain_migrate`, `cogmem_memory_retire`, `cogmem_memory_confirm`,
  `cogmem_memory_forget`, `cogmem_consolidate` (read tools remain available).

(See `docs/tool-providers.md` for how to mark a new tool primary-only.)

## Sessions, cleanup, and isolation

- Each sub-agent runs in its own session, keyed `agent:<id>:subagent:<uuid>`,
  with its own conversation history and its own (snapshotted) memory DB. None of
  it touches the primary's `main` session.
- The session and its snapshot DB are deleted when the worker finishes.
- If the process crashes mid-run, leftover sub-agent session files are reclaimed
  at the next startup, but only once they are **older than 24h** — so a crashed
  worker's artefacts can be inspected first. (`files/` outputs are never deleted.)
- Consolidation never runs on sub-agent sessions (they are ephemeral snapshots).

## Notes

- A sub-agent is only available to agents granted the `subagent` capability /
  `agent_spawn` tool.
- Targeted spawns (spawning *another* agent) are gated by the spawn allowlist;
  self-spawns are always permitted for an agent that holds the tool.
