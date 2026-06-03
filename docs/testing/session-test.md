# Session / Long-Lived Memory — Test Protocol (execute this)

You are an agent under test. Execute the steps below **in order**, using your
session tools, and report a concise **PASS / FAIL / N/A** with one line of
evidence per step. Do not skip steps. If a tool is unavailable, say so and
continue. The session tools are: `session_info`, `session_compact`,
`session_messages`, `session_search`, `session_summary_list`, `session_summary_get`.

**Step 0 — Orient.** Call `session_info`. Report: current context message count,
the archive sequence bounds (min–max), and the seq range covered by the current
summary (if any).

**Step 1 — Drop a marker.** State one short, unique fact to recall later, e.g.
`TEST MARKER: secret word = <pick a random uncommon word>, set on <today's date>.`
This is your recall target for Step 5.

**Step 2 — Compact on demand.** Call `session_compact`. Quote the compaction
notice **verbatim**, then confirm three things about its format:
(a) the date range includes the **year** (e.g. `May 30, 2026 – Jun 2, 2026`);
(b) sizes are in **parentheses with no `~`** (e.g. `18 messages (2 KB)`);
(c) which model produced the summary and whether it **succeeded, was rejected, or
errored** (and the reason, if shown).

**Step 3 — Browse the summary log.** Call `session_summary_list`. Report how many
summaries exist and the id / covered range / date / model of the most recent.
Then call `session_summary_get` with that id and confirm it returns a **full,
non-empty** summary.

**Step 4 — Retrieve old messages.** Using the archive bounds from Step 0, call
`session_messages` for a small range near the **oldest** seq and confirm real
content returns. Then call `session_search` for a distinctive word from earlier
in this conversation and confirm it is found, with a seq number.

**Step 5 — Clear preserves memory.** STOP and tell the operator you are ready for
them to type `/clear`. After the operator clears and prompts you again, recover
the **TEST MARKER** from Step 1 using ONLY your session tools
(`session_summary_get` / `session_messages` / `session_search`) — your active
context was cleared, so do not rely on memory of the conversation. Report whether
you recovered the secret word and which tool surfaced it. (This proves the
archive and summaries survived the clear.)

**Final report.** One line per step (PASS / FAIL / N/A + evidence), an overall
verdict, and anything surprising — especially any compaction failure and the
model involved.

---

## What "good" looks like
- **Step 2:** the report shows a year, parenthesised sizes, no `~`; status `ok`
  for a healthy model. A `rejected` / `error` here is the signal to inspect the
  compaction debug capture (`<workspace>/compact.jsonl` when
  `summarization.debug_capture` is on).
- **Step 3:** at least one summary listed; `get` returns structured content.
- **Step 4:** old messages and search both return real content with seq numbers.
- **Step 5 (the key one):** the secret word comes back **after** `/clear` via a
  session tool — memory was preserved, not wiped.
