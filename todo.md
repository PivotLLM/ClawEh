# TODO

## Status (branch feature/context-optimization)

**Done — committed, tested, pushed:**
- [x] `file_delete` + `file_move` (copy-then-delete) — `603ebc5`
- [x] MCP client auto-enable + allowlist name-mismatch fix — `c9402c5`
- [x] External mounts (fs sandbox + config + ValidateMountName) — `bba1964`
- [x] Mount notify watcher (.claw watermark, cron-style notice) — `6e82bc0`
- [x] WebUI: MCP **client** enable + auto-enable toggles — `8829232`

**Remaining (backend ready; UI/docs only):**
- [ ] WebUI: structured external-MCP-server **add/edit/delete** (replace the raw
  JSON `ClientServersSection`; transports stdio + http). Backend save path already
  supports null-to-delete.
- [ ] WebUI: per-agent **mounts** add/remove/edit + notify toggle (on the Agents
  page). Backend config (`MountConfig`, `ValidateMountName`) is in place.
- [ ] Docs: README — MCP client section (enable + server management + CLI parity),
  external mounts + notify, and the new file tools (`file_delete`/`file_move` and
  the earlier edit/split tools).

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
