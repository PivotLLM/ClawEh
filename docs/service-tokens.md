# Service tokens — long-lived MCP credentials

Status: **implemented**.

## Why
The per-session MCP token (`SST…`) is bound to a conversation session: it rotates
on session activity, is revoked after the 2h idle eviction, and dies on restart
(the store is in-memory). That is correct for an agent calling claw back over MCP
within a conversation, but it does not fit an **external client that drives
Maestro (or other tools) over MCP** — that caller holds a token directly and is
never re-prompted, so any rotation/eviction/restart breaks it.

A **service token** is a long-lived, per-agent credential for exactly that:
another MCP client tying into an agent's tools (Maestro and similar advanced
uses) on a stable footing.

## Design
- **Per-agent, long-lived.** One token per agent, persisted, valid until
  explicitly revoked (no TTL — claw binds to `127.0.0.1`; a TTL can be added
  later).
- **Session scope follows the configured session mode.** A service token is a
  credential, not a separate assistant, so which session it drives is decided by
  `session.session_scope` like every other surface:
  - **`unified` (the default) → the agent's MAIN session**, `agent:<id>:main`.
    Unified means one agent with one conversation, one tool surface, and one
    memory, whatever is driving it. An integration writing a memory writes it
    where the agent will actually see it, and `session_messages` /
    `session_search` read the agent's real conversation. **A service token is
    therefore as privileged as the agent itself** — treat it as such.
  - **An isolating mode (`per-user`, …) → a dedicated headless session**,
    `agent:<id>:service`, which no conversation uses. Session-scoped tools then
    operate on that empty session, so the token cannot read the agent's
    conversations.
  - **Isolation is a property of the agent, not of the door.** If an integration
    must not reach your conversations or memory, give it **its own agent** (and
    `cogmem: false` if it should not accumulate memory). Do not rely on the
    session mode for that — under `unified` there is deliberately no carve-out.
- **Headless either way.** No bound user channel → a tool's `ForUser` output is
  dropped; only `ForLLM` returns to the caller. It is a **primary** session key
  (not `subagent:…`), so `PrimaryOnly` tools — notably the Maestro suite — run.
  Cross-agent rejection still applies: the token resolves to exactly one agent.
- **Immune to rotation and eviction by construction.** Service tokens are indexed
  by agent, separately from conversation tokens, so the two coexist even when
  they name the same session: issuing or rotating the agent's own session token
  never returns or revokes the service token (a standing bearer secret must never
  land in a system prompt), and evicting the conversation leaves the service
  token in place. It persists until revoked.
- **Both endpoints, same store.** Works as a bearer on `/mcp` and as the
  `session_token` parameter on `/internal`, identically — it is just another
  record in the shared session-token store.

## Persistence & activation
- Stored at `$CLAW_HOME/state/service-tokens.json` (`0o600`), as
  `{"<agentID>": "<SST token>"}`.
- Loaded into the token store in `startMCPServer` (boot + every config reload),
  **and** a file watcher on `service-tokens.json` re-syncs the live store within
  the poll interval — so `claw token issue|revoke` takes effect **without a
  restart**. The reconcile registers present tokens and revokes removed ones;
  conversation tokens are untouched. Writes are atomic, so no debounce is needed.

## Security
- The token is a standing bearer secret. The state file is `0o600`; the CLI
  prints the token once on mint. Redaction already covers `SST…` in logs/output.
- **Under `unified` a service token can read and write the agent's conversation
  and memory.** That is the point of unified — one agent, one session, whatever
  is driving it — but it means the token carries the agent's full privileges.
  Mint one only for integrations you trust as much as the agent itself; for
  anything else, create a separate agent and issue that agent's token instead.
- Endpoints remain `127.0.0.1`-bound. Exposing `/mcp` beyond localhost is a
  separate TLS decision (see [mcp.md](mcp.md)).

## CLI (advanced users)
```
claw token issue  <agent>   # mint (or replace) and print the agent's service token
claw token rotate <agent>   # alias for issue — replace the existing token
claw token revoke <agent>   # remove the agent's service token
claw token list             # list agents that have a service token (tokens NOT shown)
```
Changes are written to the state file; a running gateway picks them up
automatically within a few seconds (a file watcher re-syncs the live store).

## Implementation checklist
- [x] `servicetoken`: state-file format + `Load`/`Save`/`Generate`/`Path`,
      no mcp-go dependency (importable by both the gateway and the CLI).
- [x] `routing.BuildAgentServiceSessionKey(agentID)` → `agent:<id>:service`;
      confirm it is **not** classified as a subagent key.
- [x] `routing.ResolveServiceSessionKey(mode, agentID)` → the agent's main
      session under `unified`, the headless service session otherwise.
- [x] `mcpserver`: rename the `isTestToken` record flag to `pinned` and add
      `RegisterService(token, agentID, archiveDir)` that binds the service
      session key.
- [x] Boot wiring in `startMCPServer`: load `service-tokens.json` and
      `RegisterService` each (alongside the existing `CLAW_MCP_TEST_TOKEN` path).
- [x] `internal/token` cobra command (`issue`/`rotate`/`revoke`/`list`) wired
      into `NewClawCommand`.
- [x] README: document service tokens + the `claw token` commands.
- [x] `docs/mcp.md`: cross-reference service tokens as the long-lived credential
      for the `/mcp` bearer endpoint.

### Tests
- [x] `servicetoken`: `Load`/`Save` round-trip; `Generate` format (`SST`+64hex);
      `Load` of a missing file returns empty, not an error.
- [x] `routing`: `BuildAgentServiceSessionKey` is primary (not a subagent key).
- [x] `mcpserver`: `RegisterService` resolves to the main session under
      `unified` and to the headless service session under an isolating mode; a
      service token and the agent's conversation token coexist on one session
      without either revoking or leaking the other, and the service token
      survives conversation rotation, eviction, and re-sync; cross-agent
      rejection holds.
- [x] `internal/token`: `issue` then `list` shows the agent; `revoke` removes it;
      issuing twice replaces (one token per agent).
- [x] Integration (`tests/test_mcpserver.sh`): a registered service token drives a
      tool on both `/internal` and `/mcp`. (The "empty service session" assertion
      applies only under an isolating mode; under `unified` the token drives the
      agent's main session by design.)
