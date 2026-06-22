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
- **Headless + isolated.** A service token resolves to a dedicated session key
  `agent:<id>:service` that no conversation ever uses. Consequences:
  - Session-scoped tools (`session_messages`, `session_search`, …) operate on
    that empty service session, so the token **cannot read the agent's real
    conversations**. Isolation comes from the session key, not the workspace.
  - No bound user channel → a tool's `ForUser` output is dropped; only `ForLLM`
    returns to the caller. A clean, headless MCP surface.
  - It is a **primary** session key (not `subagent:…`), so `PrimaryOnly` tools —
    notably the Maestro suite — run.
  - Cross-agent rejection still applies: the token resolves to exactly one agent.
- **Immune to rotation and eviction by construction.** `Issue()` only rotates by
  conversation session key and the eviction pass only walks live
  ContextManagers; neither ever touches `agent:<id>:service`. No special-casing
  in the hot paths — the token simply persists until revoked.
- **Both endpoints, same store.** Works as a bearer on `/mcp` and as the
  `session_token` parameter on `/internal`, identically — it is just another
  record in the shared session-token store.

## Persistence & activation
- Stored at `$CLAW_HOME/state/service-tokens.json` (`0o600`), as
  `{"<agentID>": "<SST token>"}`.
- Loaded into the token store in `startMCPServer`, which runs at boot **and** on
  every config reload (a reload rebuilds the MCP server). So a freshly-minted
  token activates on the next gateway **restart** or **config reload**.
  - Live file-watch activation (pick up `service-tokens.json` changes without a
    reload) is a deliberate follow-up, not v1.

## Security
- The token is a standing bearer secret. The state file is `0o600`; the CLI
  prints the token once on mint. Redaction already covers `SST…` in logs/output.
- Endpoints remain `127.0.0.1`-bound. Exposing `/mcp` beyond localhost is a
  separate TLS decision (see [mcp.md](mcp.md)).

## CLI (advanced users)
```
claw token issue  <agent>   # mint (or replace) and print the agent's service token
claw token rotate <agent>   # alias for issue — replace the existing token
claw token revoke <agent>   # remove the agent's service token
claw token list             # list agents that have a service token (tokens NOT shown)
```
Changes are written to the state file; restart the gateway (or trigger a config
reload) to activate.

## Implementation checklist
- [x] `pkg/servicetoken`: state-file format + `Load`/`Save`/`Generate`/`Path`,
      no mcp-go dependency (importable by both the gateway and the CLI).
- [x] `routing.BuildAgentServiceSessionKey(agentID)` → `agent:<id>:service`;
      confirm it is **not** classified as a subagent key.
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
- [x] `pkg/servicetoken`: `Load`/`Save` round-trip; `Generate` format (`SST`+64hex);
      `Load` of a missing file returns empty, not an error.
- [x] `routing`: `BuildAgentServiceSessionKey` is primary (not a subagent key).
- [x] `mcpserver`: `RegisterService` resolves to the service session key; a
      service token is NOT rotated by `Issue()` on a different (conversation) key;
      cross-agent rejection holds.
- [x] `internal/token`: `issue` then `list` shows the agent; `revoke` removes it;
      issuing twice replaces (one token per agent).
- [x] Integration (`tests/test_mcpserver.sh`): a registered service token drives a
      tool on both `/internal` and `/mcp`, and a session-scoped tool returns the
      empty service session (no access to a real conversation).
