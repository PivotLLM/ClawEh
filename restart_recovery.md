# Restart Recovery: Interrupted LLM Turn Detection

## The Problem

When claw processes a message, the agent loop makes an LLM call that can run for a long time — especially during agentic turns with many tool calls. If the process is killed mid-turn (power loss, OOM, systemd restart, crash), the session is left in an inconsistent state:

- The user message is on disk (it was fsynced before the LLM call)
- No assistant response was saved
- The next time claw starts, it has no way to know a turn was interrupted

The user's message will simply never receive a reply unless they resend it manually.

## Write Timing

Every message write goes through `pkg/memory/jsonl.go` (`JSONLStore.addMsg`), which opens the `.jsonl` file with `O_APPEND`, writes the serialized line, calls `f.Sync()` (fsync), and closes it. Messages are therefore durable on physical storage as soon as `AddMessage`/`AddFullMessage` returns.

The sequence in `runAgentLoop` (`pkg/agent/loop.go`) is:

1. **User message fsynced** — `cm.AddUserMessage` writes and fsyncs before any LLM call
2. **`PendingTurnAt` set in `.meta.json`** — fsynced via `agent.Sessions.SetPendingTurn`
3. **LLM call** — may be long; tool calls during iteration are also fsynced as they happen
4. **Assistant response fsynced** — `cm.AddAssistantMessage` writes and fsyncs the final response
5. **`PendingTurnAt` cleared** — via deferred `agent.Sessions.ClearPendingTurn`, which runs after step 4

If the process dies anywhere between steps 2 and 5 (inclusive), the session meta file will have a non-zero `PendingTurnAt` on the next startup.

## What Has Been Implemented

### `PendingTurnAt` field — `pkg/memory/jsonl.go`

`sessionMeta` has a new field:

```go
PendingTurnAt time.Time `json:"pending_turn_at,omitempty"`
```

It is written to the `.meta.json` file alongside the session (e.g., `~/.claw/workspace/sessions/main.meta.json`). A zero value means the session is idle; a non-zero value means a turn was in flight when the field was last written.

### Store interface methods — `pkg/memory/store.go`

```go
SetPendingTurn(ctx context.Context, sessionKey string, at time.Time) error
ClearPendingTurn(ctx context.Context, sessionKey string) error
```

Implemented on `JSONLStore` (each reads meta, updates the field, atomically rewrites the meta file with fsync via `writeMeta`).

Also exposed through `session.SessionStore` / `JSONLBackend`. `SessionManager` (in-memory only, used in tests) has no-op stubs.

### Agent loop wiring — `pkg/agent/loop.go`, `runAgentLoop`

```go
// Before the LLM call:
agent.Sessions.SetPendingTurn(opts.SessionKey, time.Now())
defer agent.Sessions.ClearPendingTurn(opts.SessionKey)

// LLM call happens here (runLLMIteration)

// After response is fsynced:
cm.AddAssistantMessage(...)   // response on disk
agent.Sessions.Save(...)       // compaction
// → defer fires: ClearPendingTurn
```

The defer ensures the flag is cleared on all return paths: normal completion, system error, or early return from a build failure. Because `AddAssistantMessage` is called before the function returns, the response is always durable before `ClearPendingTurn` runs.

## What Still Needs to Be Built

### Startup scan

On startup (before the message bus starts accepting messages), walk the sessions directories for all agents and read each `.meta.json`. Any session with a non-zero `PendingTurnAt` was interrupted.

Suggested location: a new function called from `NewAgentLoop` (or a method `AgentLoop.checkInterruptedSessions`) that iterates `registry.ListAgents()`, reads session meta files from each agent's `sessionsDir`, and collects interrupted sessions.

The meta files are at `{agent.Workspace}/sessions/{sanitizedKey}.meta.json`. To list all sessions for an agent, list `{agent.Workspace}/sessions/*.meta.json`.

Relevant helpers already exist:
- `pkg/memory/jsonl.go`: `JSONLStore` can be used directly, or read the meta JSON manually
- `sanitizeFilename` in `pkg/session/manager.go` gives the filename form of a session key (reverse is not implemented — you'd need to scan filenames and match them against known session keys, or just report the raw filename)

### Recovery options

Once an interrupted session is detected, options include:

1. **Re-queue the user message** — read the last user message from the `.jsonl` (it will be the final entry, or close to it), inject it back into the agent loop as if it just arrived. This is the most complete recovery but requires knowing which channel/chatID to reply to (that data is not currently stored in the session).

2. **Log and notify** — log a warning at startup listing interrupted sessions. Optionally send a message to the session's last known channel (stored in agent state via `state.Manager.SetLastChannel`).

3. **Mark as failed** — write a synthetic assistant message like `"[System: previous response was interrupted by a restart]"` to close the turn cleanly, so the user can resend if needed.

### Channel/chatID recovery

The `pending_turn_at` timestamp tells you a turn was interrupted but not where to send the reply. The last known channel is stored separately in agent state (`{agent.Workspace}/state.json`, key `last_channel`, value `"channel:chatID"`). That can be used to route a recovery message.

### Suggested implementation order

1. Write a `ScanInterruptedSessions(agentsDir string) ([]InterruptedSession, error)` function in `pkg/memory` or `pkg/agent` that walks all `.meta.json` files and returns sessions with non-zero `PendingTurnAt`
2. Call it from `NewAgentLoop` after the registry is built
3. For each interrupted session, log a structured warning with agent ID, session key, and `PendingTurnAt`
4. Decide on recovery strategy (re-queue vs. notify vs. mark failed) and implement
