# Audit log

ClawEh records who did what in an append-only SQLite database at
`<CLAW_HOME>/audit.db` (WAL mode, file mode 0600). Rows are never edited; the
only deletion is the daily retention prune (90 days).

## What is recorded

| kind | when | actor / sender | fields |
|---|---|---|---|
| `tool_call` | every tool an agent runs (including `agent_spawn`) | agent id; chat sender id | session, channel, tool, outcome (`ok`/`error`), duration_ms, `turn_id`, details `{"chat_id", "args"}` |
| `config_write` | every `PUT`/`PATCH /api/config` that saves | WebUI username; client IP | summary = changed top-level keys, details `{"keys":[...]}` |
| `auth` | WebUI login, logout, lockout | username; client IP | summary = the action, outcome `ok`/`error` |

Tool arguments are stored only as a redacted digest, the same redaction the
INFO log uses: file contents, edit text and HTTP bodies become byte counts,
other arguments are JSON truncated to 200 characters. The details column is
capped at 2 KiB. Config writes record which sections changed, never a value.

## Turn ids

Each agent turn gets an 8-character random `turn_id`. It appears on the turn's
log lines in `claw.log` (`Processing message`, `Routed message`, `Tool call
dispatched`, `Published outbound response`) and on its `tool_call` audit rows,
so one id shows a turn end to end across both.

## Reading it

WebUI: Services → Audit. Filter by kind and agent; "Load more" pages back.

API: `GET /api/audit?since=&until=&kind=&agent=&session=&limit=&before_id=`
(RFC 3339 times; `limit` ≤ 1000, default 100; newest first; pass the response's
`next_before_id` as `before_id` for the next page). The response also carries
`dropped`: events discarded since start because the write queue was full.

## Guarantees and limits

Recording is asynchronous and never blocks a turn. The queue holds 1024 events;
overflow is dropped, counted and warned about once a minute. A non-zero
`dropped` means the trail has gaps. The store is opened at gateway start; if it
cannot be opened the gateway logs a warning and runs without an audit log.
