# Spawn / Background Tasks Redesign

Status: **implemented** on branch `feature/spawn-tasks`

Replaces the current three-mode spawn (`detached` / `callback` / `wait`) with a
durable, observable, self-healing task model. Goal: an LLM (ours over the API
path, or an external MCP client such as the system replacing Maestro) can launch
background work and **always** find out what happened to it — no silent deaths.

---

## 1. Tool surface

Three tools, exposed identically on the internal (API-completion) path and over
MCP.

| Tool | Purpose |
|------|---------|
| `agent_spawn` | Launch a worker. Modes: `wait`, `callback`. |
| `agent_status` | Look up one task by **uuid** → status + pointer to results. |
| `agent_list` | List this agent's tasks → `[{uuid, name, status}]`. |

`detached` is **removed**. There is no mode that discards the handle: an LLM that
doesn't care about the result simply never reads the results file, but the task
is still tracked and statusable.

### `agent_spawn`

```jsonc
{
  "task":     "string (required) — the worker's instructions",
  "mode":     "wait | callback   (required)",
  "name":     "string — short human label; REQUIRED when mode=callback",
  "agent_id": "string — optional target agent to delegate to ('' = self)"
}
```

- **`wait`** — runs to completion, returns the worker's result inline. No task
  files are written (ephemeral; if the process dies mid-wait the blocking call
  dies with it — nothing to recover). `name` not required.
- **`callback`** — returns immediately with the task's `uuid`. The full result is
  written to a file; completion is reported via the compact notification below
  (API path) and/or polled via `agent_status` (MCP path). `name` required.

### `agent_status`

```jsonc
{ "uuid": "string (required)" }
```

Returns (mirrors the callback payload):

```jsonc
{
  "uuid": "…",
  "name": "goagy-transport",
  "status": "unknown | running | done | error",
  "results_path": "tasks/<uuid>-results.json",   // omitted when unknown
  "error": "",                                    // set when status=error
  "created_at": "…", "finished_at": "…"
}
```

`unknown` = no status file for that uuid (never existed, or pre-restart in-memory
loss with files already cleaned).

### `agent_list`

No args. Returns this agent's tasks (scanned from its `tasks/` dir):

```jsonc
{ "tasks": [ { "uuid": "…", "name": "…", "status": "running" }, … ] }
```

---

## 2. Task identity & file layout

On `callback` spawn we mint a filename-safe **UUID** (the durable key) and keep
the LLM-supplied **name** only as a label. Two tasks may share a name; uuids
never collide, and `agent_status` keys on uuid.

Files live in the **launcher's** workspace (confirmed decision): when Alice
spawns with `agent_id: Bob`, the worker runs with Bob's model candidates but the
task files land in **Alice's** `tasks/`, because Alice is who holds the uuid and
will `file_read` the pointer. (This also matches today's manager, where a
delegated spawn already executes in the owning manager's workspace/tools and only
swaps the model.)

```
<workspace>/tasks/
  <uuid>-status.json     # full task record — replayable
  <uuid>-results.json    # the worker's output payload
  <uuid>.run             # liveness marker; present ⇔ "should be running"
```

`<uuid>-status.json`:

```jsonc
{
  "uuid": "d01e2f5d-9ae9-4adf-9502-2818c8145618",
  "name": "goagy-transport",
  "owner_agent_id": "alice",     // launcher (whose workspace this is)
  "agent_id": "bob",             // target executor ('' = self)
  "mode": "callback",
  "task": "implement transport — handshake, …",   // full text, for replay
  "channel": "telegram-1", "chat_id": "123",       // for callback routing
  "status": "running",           // running | done | error
  "created_at": "2026-06-13T19:10:00Z",
  "started_at":  "2026-06-13T19:10:00Z",
  "finished_at": "",
  "restarts": 0,
  "retry_after": 1718312345,     // epoch secs; earliest a crashed task may relaunch
  "error": "",
  "results_path": "tasks/<uuid>-results.json"
}
```

`<uuid>-results.json`:

```jsonc
{
  "uuid": "…", "name": "…", "status": "done",
  "finished_at": "…", "iterations": 12,
  "content": "…full worker output…"
}
```

All writes are atomic (temp file + rename) so a crash never leaves a half-written
JSON.

---

## 3. Status lifecycle

```
            spawn (callback)
                 │  write -status.json (running) + .run
                 ▼
            ┌─────────┐   success   ┌──────┐
            │ running │────────────▶│ done │  write -results, status=done, rm .run
            └─────────┘             └──────┘
              │   │  error/panic/timeout
              │   └────────────────▶┌───────┐
              │                     │ error │  status=error, error set, rm .run
              │                     └───────┘
              │  process dies (.run left behind, no live goroutine)
              ▼
         picked up by the periodic supervisor (§5)
```

`unknown` is not stored — it's what `agent_status` returns for an absent uuid.

---

## 4. Callback delivery — both paths

Completion handling lives in the **Spawner/manager** (already injected via
`Deps.Spawn` for both transports), so it is transport-neutral:

1. Write `-results.json`, update `-status.json`, delete `.run`.
2. Emit a **compact pointer** notification — never the full payload (avoids
   context bloat). Shape modeled on the Maestro callback you provided:

```jsonc
{
  "event": "completed",
  "uuid": "d01e2f5d-…",
  "name": "goagy-transport",
  "status": "done",
  "result_file": "tasks/d01e2f5d-…-results.json",
  "retrieval_instruction": "Read tasks/d01e2f5d-…-results.json for the full result."
}
```

No numeric `id` — the uuid is the only identifier. The retrieval instruction
points **directly at the workspace file** (a path the launching agent can open
with `file_read`), not at another tool call.

- **API-completion path:** we own the agent loop, so the pointer is injected as a
  short system turn — the agent wakes, reads the file with its existing
  `file_read`, and continues. (This replaces today's behavior of injecting the
  full result via `ToolCall.Notify`.)
- **MCP path:** MCP cannot push a new tool result to a client after the call
  returned. So the client uses `agent_status` to poll and then reads the results
  file. Same files, same payload shape — only the delivery differs. A courtesy
  notification can still be posted to the human channel if one is associated.

---

## 5. Periodic supervisor (self-healing) — replaces startup scan

Instead of a one-shot scan at boot, a background goroutine reaps stuck tasks on a
fixed interval. This recovers tasks that die *mid-run* (panic that somehow skips
recovery, OOM kill) as well as across restarts, and the `retry_after` gate
prevents a start-crash-start-crash storm from burning all attempts instantly.

Every `TaskSupervisorInterval`, for each agent's `tasks/` dir, glob `*.run`:

```
for each <uuid>.run:
    if uuid is in the manager's in-memory LIVE set:   skip   # actually running now
    read <uuid>-status.json
    if status is terminal (done/error):               rm .run; continue   # stale marker
    if restarts >= TaskMaxRestarts:
        status = error; error = "gave up after N interrupted restarts"
        rm .run; continue
    if now < retry_after:                              skip   # cooling down
    # eligible: relaunch
    restarts++; retry_after = now + TaskRetryDelay
    prepend interruption note to the task text:
        "NOTE: this task was interrupted (process restart or crash) and is being
         resumed from the beginning. Verify what was already completed before
         repeating any side-effecting work."
    status = running; go runTask(...)
```

- The **live set** is the manager's in-memory map of currently-running uuids.
  After a process restart it's empty, so every `.run` looks reapable (subject to
  `retry_after`). While running normally, an in-flight task is skipped.
- `retry_after` is written at each launch as `now + TaskRetryDelay`, so a task
  that crashes immediately won't be retried until the delay elapses — the
  supervisor scans often but acts rarely.
- Resume is **always-on** (confirmed) and re-runs from scratch with the note;
  not checkpoint resume.

### Constants (in `pkg/global`, easy to tune)

```go
const (
    TaskMaxRestarts        = 3                // give up after this many interrupted restarts
    TaskRetryDelay         = 5 * time.Minute  // min wait before a crashed task is retried
    TaskSupervisorInterval = 1 * time.Minute  // how often the reaper scans .run markers
)
```

---

## 6. Crash / timeout handling inside a worker

- `runTask` gets a `recover()` so a panic → `status=error`, error recorded,
  `.run` deleted (clean terminal, not a leak).
- Per-task timeout from the model's `request_timeout` (already plumbed) → on
  expiry the task ends `error: "timeout"`. A truly hung worker therefore can't
  sit `running` forever in-process; and if the *process* is what's wedged, the
  supervisor handles it.

---

## 7. Code changes (no code yet — scope preview)

- **`pkg/global`**: add the three task constants; (callback payload struct
  optional).
- **`pkg/global/spawner.go`**: drop `SpawnDetached`; `SpawnMode` = `{wait,
  callback}`. `SpawnRequest` gains `Name`. Keep `OnResult` but it now carries the
  compact pointer result, not the full payload.
- **`pkg/tools/agents/subagent.go`** (`SubagentManager`):
  - task record → uuid-keyed; add `Name, OwnerAgentID, Restarts, RetryAfter,
    Status, ResultsPath, error`, persisted to `-status.json`.
  - write/update status + results + `.run` atomically; `recover()` in `runTask`;
    timeout.
  - in-memory live set; `Supervise()` reaper method; loader that reads a
    `-status.json` to relaunch.
  - results written to the **launcher's** workspace.
- **`pkg/tools/agents/spawner.go`**: two modes; build the compact notification;
  populate files.
- **`pkg/tools/agents/global_provider.go`**: schema → `mode {wait,callback}` +
  required-when-callback `name`; new `agent_status` / `agent_list` tool defs
  (bare `status` / `list` under the `agent` namespace → `agent_status`,
  `agent_list`). Update `resolveSpawnMode` (no detached; callback always tracked).
- **`internal/gateway`**: start the supervisor goroutine; give it access to the
  per-agent managers (or scan `<base_dir>/*/tasks`).
- **MCP (`pkg/mcpserver`)**: `agent_status`/`agent_list` flow through the existing
  per-agent registry path unchanged; no async push needed (poll model).
- **Cleanup**: remove the now-dead `SpawnTool` (`agent_spawn` AsyncExecutor) and
  `SubagentTool` (`subagent`) legacy tools if the global-provider path fully
  supersedes them (verify nothing else registers them). The `subagent`
  capability key stays as the gate.
- **Tests**: spawn wait/callback; status unknown/running/done/error; list;
  supervisor relaunch + give-up-after-N + retry_after cooldown; panic→error;
  delegated spawn writes to launcher workspace; MCP probes for the three tools in
  `tests/test_mcpserver.sh`.

---

## 8. Open assumptions (flagging, not blocking)

- `wait` writes **no** task files (ephemeral). If you want wait tasks to also
  appear in `agent_list`, say so.
- Supervisor discovers workspaces by iterating the agent registry's managers
  (preferred) rather than blindly globbing `<base_dir>/*/tasks` — keeps it to
  known agents. Confirm if you'd rather it scan the filesystem.
- `name` is required only for `callback`. `wait` ignores it.
