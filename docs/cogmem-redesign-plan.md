# Cogmem redesign — plan for approval

One migration, done once. The current scheme works well in practice; this
removes the parts that cost bytes without changing behaviour, and adds the one
distinction that does real work.

**Findings this is based on**

- `source` drives nothing. Its only mechanical use is the rule-5 check that the
  review gate needs; everything else prints it. It is not in the prompt — the
  `[source: …]` tag the assistant sees is rendering `origin`.
- `type` drives nothing either, and is not in the prompt. The assistant cannot
  currently tell a rule from a stale observation when reading its own memory.
- The review gate has never once been used. 244 memories are pending, oldest 77
  days, zero confirmations across 1,359 memory events. 236 of them are
  unreachable by any path — not in context, not searchable, not in the WebUI.
- `priority` is stored and returned by the API and never read. `StatusRejected`
  is declared and never used.
- `OriginUser` is declared, documented as "a human created it directly (WebUI /
  import)", and has a dedicated branch in the prompt renderer — but nothing can
  write it. The write side was never built.

---

## 1. Data model

1. Delete the `source` column, the `Source` type, and its four constants.
2. Delete the `priority` column.
3. Delete `StatusRejected`.
4. `status` becomes `active` | `retired`. `review` is removed.
5. `type` becomes `fact` | `preference` | `rule` | `event` | `operational`.
6. `origin` is unchanged: `chat` | `consolidation` | `user`.

## 2. What the two new types mean

7. **`event`** — something observed at a point in time. Trip logs, delivery
   statuses, "oversight run at 07:40". Goes stale, accumulates without bound,
   and has no business being prompt-resident.
8. **`operational`** — the assistant's own housekeeping: file pointers, process
   notes, rules it sets for its own working method. Loaded normally.
9. The discriminator for the overlap case: `rule` governs output and behaviour
   toward the user; `operational` is the assistant's own bookkeeping. "Don't use
   the word thuddy" is a rule. "Don't store outline beats in cogmem" is
   operational.

## 3. Behaviour changes

10. `event` memories are **never** loaded into context automatically.
11. Each domain block gains a one-line count when it has events, e.g.
    `(42 event memories in this domain — search to retrieve)`. The count only,
    never the items.
12. `SearchMemories` currently filters to `status='active'` and must be opened
    up so events are reachable. Retrieval gains an `include_events` flag,
    default false.
13. Memory lines in the prompt gain their type: `- (rule) Do not use the word
    "thuddy."` Today type is invisible to the assistant; this is what makes
    `operational` worth storing at all.
14. The prompt tag `[source: …]` is relabelled `[origin: …]`. It has always been
    rendering origin, and with no `source` field left the old label is actively
    misleading.

## 4. Model contract

15. The model states `type` only. It no longer sets `status` (there is nothing
    to choose) and no longer sets `source` (gone).
16. Empty/missing fields are rejected rather than silently defaulted. Today an
    op omitting both `status` and `source` passes every guard and is then
    defaulted to a combination the rules forbid.
17. `Normalize()`'s auto-repair for inferred-active disappears with the rule it
    repairs — and with it the "auto-repaired: …" notes on the memory page.

## 5. Removals — these are breaking

18. Remove the `cogmem_memory_confirm` tool. Nothing to confirm.
19. Remove the pending-confirmation digest, its per-session throttle, and
    `ListPending`.
20. Remove the config keys `memory.prompt.pending_surface` and
    `memory.prompt.pending_max`.
21. Bump `version` in `app/app.go` to `0.5.0` and open a new `## [0.5.0]`
    CHANGELOG section, with its link ref at the bottom of the file. Each of
    18–20 needs an entry there marked **BREAKING** with the migration line.

## 6. WebUI — memory becomes a curation surface

22. Edit a memory's **type** from a dropdown in the row.
23. Toggle a memory's **status** between active and retired.
24. Show retired memories, behind a "show retired" control. Required by 23 — the
    list currently filters to active, so there is no way to see one to restore
    it.
25. **Bulk select** with a checkbox column and apply-to-selected for retype,
    retire and delete. Penny alone has 279 near-identical cron entries; one row
    at a time is not a workflow.
26. **Add a memory**, with `origin=user`, `status=active`, confidence `1.0`, and
    type chosen from the same dropdown.
27. **Add a domain**, since an added memory needs somewhere to live.
28. Remove the now-dead `prio` and `source` fields from the row display.

Item 26 is worth more than it looks: dropping `source` gives up the "did Eric
say this or did it infer it" signal, but that was only ever the model's
self-report. `origin=user` is the same distinction, verifiable, and already
flagged to the assistant in the prompt.

## 7. Migration

29. Make `schemaVersion` real. It is currently written to `schema_migrations`
    and never read back; the actual work is done by idempotent column-probing.
    Bump it and drive the migration off the recorded version so it runs once.
30. `review` → `active` for all 244.
31. Drop the `source` and `priority` columns.
32. **No automatic type reclassification.** Existing rows keep the type they
    have; you retype them in the WebUI. Guessing "does this text start with a
    date" would be wrong often enough to be worse than doing nothing.
33. Consequence to expect: the 244 formerly-invisible memories become active and
    enter context on the first run after upgrade, including some junk. That is
    the point of removing the gate, and item 25 is how you clean it up.

## 8. Instructions and docs

34. Update the memory guidance in `agent/context.go` — the new types, when to
    write an `event`, and that events come back only via search.
35. Update the `cogmem_memory_create` tool description to match.
36. Update the consolidation prompt `default_prompt.md` **and**
    `templates/COGMEM.md`, which must stay in sync.
37. Note in the docs that the per-agent `COGMEM.md` override exists. All seven
    agents have one and all seven are still byte-identical to the template.

## 9. Verification

38. Unit tests for the new types, the event exclusion, the search flag, the
    rejected-empty-field cases, and the migration (including that it runs once).
39. Update `tests/test_mcpserver.sh` — it probes every provider tool, and
    `cogmem_memory_confirm` is going away.
40. Run the WebUI regression suite (`docs/webui-test-plan.md`,
    `tests/frontend-e2e.mjs`) against dev; add steps for the new memory-page
    controls.
41. Verify the automatic pre-migration snapshot (item 42) lands for all seven
    production databases on the first upgraded start.

## 10. Backup and portability

42. **Automatic pre-migration snapshot.** Before applying a version-bumping
    migration, copy the database file to `<name>.pre-v<N>.db` beside it. Runs
    inside `Open`, needs no user action, and happens at the only moment that is
    guaranteed correct. Because item 29 makes `schemaVersion` real, this covers
    every future migration, not just this one.
43. **YAML export.** A full round-trip dump: domains, memories, every field, and
    a format version. YAML rather than JSON because memory text is long prose —
    block scalars keep it editable, where JSON collapses each memory to one
    `\n`-escaped line. `gopkg.in/yaml.v3` is already a direct dependency.
44. **YAML import.** Restore a dump into a store, in one of two modes chosen at
    import time: **merge** (add what is not there, leave existing rows alone) or
    **replace** (wipe and load, making a dump a true restore point). The WebUI
    asks which when you pick the file.
44a. Import re-mints domain and memory IDs on the way in, so a dump can be
    loaded into a *different* agent — seeding a new writing assistant from
    Wendy's craft domains, say. Nothing references memory IDs across runs, so
    restoring to the same agent under fresh IDs breaks nothing.
45. **WebUI**: export downloads a file, import accepts one, both on the memory
    page alongside the curation controls from §6.
46. The export carries its format version so a dump taken today can still be
    read after a later schema change. The snapshot in item 42 is the belt;
    this is the braces.
47. **Retire the Markdown export.** Repoint the `cogmem_export` tool at the new
    round-trip format, writing `files/MEMORY_EXPORT.yaml`. The assistant keeps
    the capability, and there is one format everywhere instead of a readable one
    and an importable one. User-visible path and format change — CHANGELOG entry
    required.
48. Delete `cogmem/consolidate/export.go`. `WriteExport` has no callers anywhere
    in the tree; it is dead code, not a second export path. Invisible from
    outside the repo, so no CHANGELOG entry.

---

## Decisions

- **Version — `0.5.0`.** The breaking removals get their own minor version
  rather than accumulating under `0.4.72` alongside the frontend and
  build-number work. See item 21.
- **Type names — `event` and `operational`,** as written throughout.
- **Sequencing — one release.** The migration and the WebUI curation tools ship
  together. The 244 enter context on first start and are cleaned up afterwards
  with the tools arriving in the same deploy.

Two consequences of shipping together, both handled in the plan: the automatic
pre-migration snapshot (item 42) is the safety net rather than a precaution, and
item 25 (bulk select) is on the critical path rather than a convenience —
without it there is no practical way to clear Penny's 279 cron entries once they
are prompt-resident.

Also settled: both the automatic snapshot and export/import ship (items 42–48);
import supports **both merge and replace**, chosen at import time, and re-mints
IDs so a dump can be loaded into any agent (items 44 and 44a); the dump is
**YAML**, and it replaces the Markdown export rather than joining it.
