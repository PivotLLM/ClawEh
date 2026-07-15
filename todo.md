# TODO

## Status (branch feature/context-optimization)

**Done — committed, tested, pushed:**
- [x] `file_delete` + `file_move` (copy-then-delete) — `603ebc5`
- [x] MCP client auto-enable + allowlist name-mismatch fix — `c9402c5`
- [x] External mounts (fs sandbox + config + ValidateMountName) — `bba1964`
- [x] Mount notify watcher (.claw watermark, cron-style notice) — `6e82bc0`
- [x] WebUI: MCP **client** enable + auto-enable toggles — `8829232`

- [x] WebUI: structured external-MCP-server add/edit/delete — `29647f6`
- [x] WebUI: per-agent mounts add/edit/delete + notify toggle — `ea005c6`
- [x] Docs: README — MCP client, external mounts + notify, file tools

**All items complete.**

---


## MCP client: WebUI management + CLI passthrough (feature parity)

### Decisions
- **A — aggregate through claw's MCP host.** External MCP tools are served by claw to
  both API agents (directly) and CLI agents (via the host, proxied upstream) with **no
  extra per-CLI configuration**. claw stays in the loop (allowlist + audit).
- **Auto-enable** `tools.mcp.enabled` when ≥1 external server is enabled (mirrors
  `mcp_host.auto_enable`), removing the two-flag footgun. Still overridable via a toggle.
- **Transports: stdio + http only.** `http` = Streamable HTTP. `sse` is merely an alias
  to the same StreamableClientTransport (no real legacy SSE) and is deprecated — not
  surfaced in the UI; existing `sse` configs keep working as http.

### Background (current state)
- WebUI MCP page exists but its enable toggle controls `mcp_host.enabled` (claw **as a
  server** for CLIs), **not** `tools.mcp.enabled` (claw **as a client** to upstream
  servers). External servers are editable only as a **raw JSON** box.
- Config + save backend already support `tools.mcp.enabled` and full `tools.mcp.servers`
  CRUD (the patch path does null-to-delete).
- claw's MCP host catalogue is the **union of all agents' registries** (incl. external
  MCP tools) and proxies calls upstream — so CLI passthrough is mostly enablement +
  the allowlist fix, not a new proxy.

### Work items
1. **Surface the MCP client switch in the WebUI MCP page.**
   - Add a `tools.mcp.enabled` toggle ("Connect to external MCP servers"), distinct from
     the existing `mcp_host.enabled` (claw-as-server) toggle.
   - Auto-enable on save: if any server is enabled, default `tools.mcp.enabled` true;
     keep the toggle to override.
2. **Structured external-server editor (replace the raw JSON box).**
   - Per server: `name`, `enabled`, `type` (stdio | http), then
     - stdio: `command`, `args[]`, `env{}`, `env_file`
     - http: `url`, `headers{}`
   - Add / Delete buttons; validation (unique non-empty name; url for http; command for
     stdio). Backend patch already deletes via null on the servers map.
3. **Fix the allowlist name mismatch (prerequisite).**
   - Registration gate (`loop_mcp.go:121`) checks the **bare** upstream `tool.Name`;
     execution (`registry.ExecuteWithContext`) and the WebUI server pattern
     (`MCPServerPattern` → `mcp_<server>_*`) use the **prefixed** name. Align registration
     to check `mcpTool.Name()` so `mcp_<server>_*` works at both registration and
     execution, for API and CLI agents alike. (Today only `*` works end-to-end.)
4. **CLI passthrough verification (Decision A).**
   - Confirm CLIs receive external tools through claw's host once enabled + allowlisted;
     ensure `MCPHost.Tools` permits them (default/`*` or document).
   - Check the discovery-hidden interaction (when `mcp.discovery.enabled`, MCP tools are
     `RegisterHidden` — decide whether the host should expose hidden tools to CLIs).
5. **Docs.** README MCP section: document the client enable toggle + WebUI server
   management, and the parity note (CLIs get external tools via claw, no per-CLI setup).
   Reconcile the "configure external MCP directly in each CLI" guidance.
6. **Tests + gate.** Backend: config patch add/delete server, enable, auto-enable;
   allowlist-fix unit test (`mcp_<server>_*` matches at registration). Frontend build.
   MCP integration probe if feasible. Full `test.sh` green.

---

## File tools: delete + move

### `file_delete`
- Delete a whole file. Required safeguard arg **`sure` (must be `true`)** — absent or not
  true → refuse with a clear message. Confined to writable areas (write sandbox), same as
  edit/delete. (Distinct from `file_delete_lines`/`_bytes`, which remove content *within* a
  file.)
- **No automatic backup** (the `sure` flag is the safeguard).
- **Must NOT delete backup files** — refuse when the target matches the backup naming
  pattern (`<name>.NNNN`, the 4-digit suffix written by the `backup` option), so the agent
  can't wipe the safety net.

### `file_move`
- `file_move(source, destination)` — relocate without pulling content into context.
- **Under the covers: copy then delete source on success** (NOT rename) so it works across
  drive/mount boundaries (see mounts below). Files are small enough that copy+delete is
  fine. **Add a code comment documenting this as a conscious design decision.**
- Source must be readable, destination writable; both within allowed scope. No backup by
  default (relocation, not destruction).

---

## External mounts (top-level dirs beside `files/` and `skills/`)

### Mounting
- Let the user mount one or more external absolute paths as **top-level names** in the
  agent's space, peers of `files/` and `skills/` — e.g. mount `notes` →
  `/home/ai/Documents/mynotes`; a file `stuff.md` is reached as `notes/stuff.md` (same
  relative-path convention the existing file tools use).
- **Mount name rules:** a single path component, characters `[A-Za-z0-9-]` only (hyphen
  allowed, e.g. `notes-eric`); reject spaces, `/`, `.`, and anything else
  (`notes - eric` invalid). Must not collide with reserved roots (`files`, `skills`,
  `tasks`, `common`).
- **Whole tree mounted:** the entire tree from the specified directory down is accessible
  (subdirectories included). Sandbox confines to the mount: reject `..` so the agent can't
  climb above the mount point. The mount target must be an existing directory.
- Read **and** write (so `file_move`/`file_delete`/edit work into/within a mount — this is
  why move is copy+delete across boundaries).
- Implementation: mount-aware resolution in the fs sandbox (`buildFs`/`buildWriteFs`) — a
  `notes/...` path resolves to `<mountpath>/...`, not `<workspace>/notes/...`.
- **Per-agent**, just like `files/` and `skills/` (config block, e.g.
  `agents.list[].mounts` / `agents.defaults.mounts`).
- **WebUI: full CRUD** — add / remove / edit each mount, plus the monitor (notify) toggle.

### Mount "notify" (new-file → notify the agent, cron-style)
- Per mount: a **`notify`** (monitor) toggle.
- When on, poll the mount tree every **`MountNotifyIntervalSeconds`** (global constant,
  **default 10s**). On a **new file**, publish an inbound message to the agent's **default
  channel**, exactly like a cron job firing — it just tells the LLM a new file showed up and
  gives the **full path**, nothing else.
- Write an **INFO** log event with the filename when detected.
- **Restart-safe baselining via a `.claw` marker file** in the mount dir (NOT in-memory, so
  a file written while claw is stopped is not missed):
  - If `.claw` does **not** exist → baseline: `touch` it, fire nothing.
  - If `.claw` exists → any file with mtime **newer than `.claw`'s mtime** is new → fire for
    it, then `touch .claw` (advance the watermark) so it isn't re-fired.
  - Only touch `.claw` (a) when creating it (baseline) and (b) after firing — so almost no
    churn. Ignore `.claw` itself in the scan (and from agent-visible listings).
  - Note: mtime-watermark catches new *and* modified files; acceptable.
- Model on the cron inbound-publish path (same bus `InboundMessage` → agent processing).

---

## Outbound MCP client robustness — session drop / in-flight requests

### The issue
When an outbound MCP session drops while a request is in flight, the in-flight call
fails fast and the connection is **not** re-established until a config reload. There is
no liveness detection, so a silently-dropped session stays broken until the next call
attempt happens to fail.

### Current behaviour (as-is)
- **Fail-fast on drop.** The mcp-go reader goroutine hits EOF/read error, closes the
  transport `done`/response channels, and the pending `SendRequest` unblocks with
  `ErrTransportClosed` (`"transport closed"`) or, for SSE, `"connection has been closed"`.
  It drains the response channel first to catch a reply that raced in just before EOF.
- **Surfaced to the model, turn survives.** `Manager.CallTool` (`pkg/mcp/manager.go:526`)
  wraps it as `"failed to call tool: …"`; `MCPTool.Execute` (`pkg/tools/mcp_tool.go:203`)
  converts it to an `ErrorResult` the LLM sees. The turn does not crash.
- **No auto-reconnect / keepalive / health check.** Nothing in `pkg/mcp/` probes liveness.
  A dead connection is only noticed on the *next* `CallTool`. Reconnection happens **only**
  on config reload via `Manager.Sync` (`manager.go:471`) / `ReinitMCP` (`loop_mcp.go`).
- **Timeout is the caller's context, not the MCP client.** Per-tool-call timeout is set in
  the agent loop (`loop.go:2965`), default `ToolTimeout = 5m` (plus `TurnTimeout = 15m`).
  The stdio transport has no per-request timer of its own — it relies on `ctx.Done()` and
  the `done` channel. A path calling without a deadline could block indefinitely on a hung
  (not dropped) server.
- **Shutdown is clean.** `Manager.Close` (`manager.go:566`) flips a closed flag,
  `wg.Wait()`s for in-flight calls, then closes clients and tree-kills stdio process groups.

### Gaps / risks
1. **No liveness detection** — a silently-dropped stdio child or stalled SSE/HTTP stream
   stays broken until a call fails; the very next tool call is spent discovering the drop.
2. **No reconnect-on-drop** — recovery requires a config reload; a transient network blip
   or crashed-then-restartable stdio server leaves the connection dead indefinitely.
3. **No retry** of the failed in-flight call after a (re)connect — the model must decide to
   retry, spending a turn/round-trip.
4. **Timeout depends entirely on the caller** — if any call path omits a deadline, a hung
   server blocks with no MCP-client-level safety net.

### Decisions (locked 2026-07-15)
- **Retry scope:** connection-error only — retry only on transport-closed errors (drop
  before/around send), never a generic failure. Avoids double-executing a side-effecting
  tool the server may already have processed (MCP has no idempotency contract).
- **Transparency:** transparent retry — reconnect + retry inside `CallTool`; the model only
  sees an error if the retry also fails.
- **Reconnect cooldown:** 30s default after a failed reconnect before retrying that server.

### Implementation (done — `pkg/mcp/resilience.go` + `manager.go`, tests in `manager_test.go`)
- [x] **Reconnect-on-failure in `CallTool`.** On a *connection-level* error only
      (`isConnectionError`: `ErrTransportClosed`, EOF, connection refused/reset, broken pipe,
      "connection has been closed", etc.), `CallTool` transparently reconnects the server and
      retries once. Normal tool errors are returned as-is (no double-execute).
- [x] **Bounded reconnect with cooldown (30s default, `reconnect_cooldown_seconds`).** A
      failed reconnect sets a per-server cooldown; further reconnects for that server are
      short-circuited until it expires, so a dead upstream isn't hammered every call.
- [x] **Client-side deadline backstop (`call_timeout_seconds`, default 300s).** `CallTool`
      applies its own `context.WithTimeout` *only when the caller passed no deadline*, so a
      hung server can't block forever. Caller deadlines are honored as-is.
- [x] **Background liveness probe (opt-in, `liveness_probe_seconds`, default 0 = off).**
      Per-connected-server goroutine pings on an interval; a failed ping proactively
      reconnects (cooldown-gated). Lifecycle: started in `ConnectServer`, stopped in
      `disconnect`/`Close` (probes drained before teardown; `closed` checks prevent
      resurrection during shutdown).
- [x] **Observability.** INFO/WARN/ERROR logs on drop→retry, reconnect success, reconnect
      failure→cooldown, and probe-failure→reconnect (per logging preference).
- [x] **Tests.** classifier table; cooldown gating; tuning application; drop→reconnect→retry
      integration (two test servers); reconnect cooldown short-circuit; probe→reconnect.
      `make test` (`-race`) green.

### Follow-ups / not done
- [x] **WebUI:** the three tuning knobs are exposed in the MCP page as a "Client Resilience"
      section (`reconnect_cooldown_seconds` / `call_timeout_seconds` / `liveness_probe_seconds`),
      saved under `tools.mcp.*` via the debounced config patch (independent of server-row
      validity). `ResilienceSection` in `mcp-sections.tsx`; fields in `form-model.ts`.
- [x] **WebUI:** per-server *live* connection state (connected / reconnecting / cooldown,
      falling back to disconnected/disabled) shown as a status pill on each server row,
      polled every 5s. Backend: `GET /api/mcp/status` (`web/backend/api/mcp_status.go`) →
      `AgentLoop.MCPStatus()` → `Manager.Status()`; injected in `internal/gateway/helpers.go`.
      Frontend: `getMCPStatus` + `ServerStatusBadge`. Tests: `mcp_status_test.go`,
      `TestStatus_ReportsConnectedAndCooldown`.
- [ ] Probe-interval changes on a config reload only apply to servers that reconnect (Sync
      keeps unchanged servers' existing probe goroutines). Acceptable for an opt-in knob;
      revisit if it becomes surprising.

### Decisions applied (locked 2026-07-15)
- Retry scope: connection-error only. Transparency: transparent retry. Cooldown: 30s.
- Liveness probe: opt-in, default off (per follow-up request to implement #4).
