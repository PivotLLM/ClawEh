# Context eviction

A per-turn, LLM-free sweep that keeps long sessions inside the model's context
window by collapsing **re-retrievable** tool results — file reads, web fetches —
to a short placeholder once the agent has moved on. It runs before every model
dispatch, so the window rarely fills enough to trigger the (expensive,
LLM-driven) summarization compaction at all.

It complements compaction; it does not replace it:

- **Eviction** sheds the heaviest, cheapest-to-recover content every turn for free.
- **Compaction** still summarizes genuine *conversational* growth as a backstop.

The order each turn is **sweep (cheap) → compress (only if still over threshold)**.

## Why it's safe

Eviction is **reversible**. Only tool results whose source can be fetched again
are touched (a file still on disk, a URL still reachable). The placeholder tells
the model how to recover the content:

```
[evicted: file_read files/novels/ch17.md (18432 bytes) — content evicted to save context; re-read if you need it again]
```

So the only cost of an over-eager eviction is a re-read, never lost work.
Conversational text, memory, and one-shot results are never evicted.

## Policy

Turn age is **1-based**: the newest turn group is age 1. For each re-retrievable
tool result, in priority order:

1. **Superseded** — a later read of the same resource, or a later write/edit to
   it — → evicted, *any size and any age, including inside the protect window*.
   A stale duplicate the agent has already re-read is pure bloat regardless of
   recency. The most-recent read of each resource is never superseded, so the
   agent always keeps its current view of a file.
2. **Age ≤ `protect_turns`** (and not superseded) → never evicted (the active
   working set).
3. **Age > `evict_turns`** → evicted, *any size* (old content is almost
   certainly captured in the summary by now).
4. **Budget valve** — if reader-result bytes still exceed `budget_bytes`, evict
   largest-first among anything older than `protect_turns` until under budget.
   Handles a *burst* of large reads that the age cutoff can't shed fast enough,
   and is the mechanism that sheds large reads under memory pressure.

Because supersession is checked first and ignores the protect window, the
`protect_turns` setting guards only the age and budget tiers — in practice
mainly the budget valve, which can otherwise fire at any age. Keeping the latest
read of every file is handled by supersession itself.

There is deliberately no "evict large reads on age alone" tier: shedding large
reads is the budget valve's job, and it does so only under real memory pressure.
Evicting a big read on age while the window has room would just force a needless
re-read.

Supersession, the age cutoff, and the budget valve all evict regardless of size,
so small content is still cleaned up when it's stale, superseded, or under memory
pressure — there is no separate size-based tier to tune.

## Reader and writer tools

| Role   | Tools                                                   | Resource key       |
|--------|---------------------------------------------------------|--------------------|
| Reader | `file_read`, `file_list`, `web_fetch`                   | `path` / `url`     |
| Writer | `file_write`, `file_edit`, `file_append`, `file_copy`   | `path` / `destination_path` |

A writer supersedes any earlier read of the same path. MCP-published names
(`mcp__server__file_read`) are normalized to their bare form before matching.

## Configuration

Set under `agents.defaults.context_eviction` (the default for all agents) and/or
per agent under `agents.list[].context_eviction` (overrides the defaults field by
field). Every field is optional; unset fields fall back to the built-in defaults.

```json
{
  "agents": {
    "defaults": {
      "context_eviction": {
        "enabled": true,
        "protect_turns": 3,
        "evict_turns": 10,
        "budget_bytes": 0,
        "notify_user": false
      }
    }
  }
}
```

| Field          | Default | Meaning                                                      |
|----------------|---------|-------------------------------------------------------------|
| `enabled`      | `true`  | Master switch.                                              |
| `protect_turns`| `3`     | Newest N turns are never evicted (except superseded duplicates). |
| `evict_turns`  | `10`    | Any read older than this is evicted regardless of size.   |
| `budget_bytes` | `0`     | Reader-byte cap for the burst valve; `0` ⇒ ~40% of window. |
| `notify_user`  | `false` | Surface a one-line notice in the conversation per eviction. |

## Observability

Every eviction is written to the log at **DEBUG**, regardless of `notify_user`:

```
llmcontext  evicted tool result  session_key=... seq=... tool=file_read
            resource=files/ch17.md bytes=18432 age_turns=6 reason=large
```

When `notify_user` is on, the same eviction is also surfaced in the conversation
as a one-line notice (long URLs are capped):

```
[Evicted 18432 bytes at 6 turns (large): file_read files/novels/ch17.md]
```

The `reason` tag (`superseded` | `stale` | `large` | `budget`) makes it clear
*why* each result was dropped.
