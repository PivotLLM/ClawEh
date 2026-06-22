# MCP server: dual-endpoint authentication

Status: **implemented**. This document specifies how ClawEh's MCP server accepts
**standard bearer-token** auth alongside the existing in-call session-token
model, so that standard MCP clients and a comprehensive `probe`-based test suite
(mirroring Maestro's) can exercise every tool, while preserving ClawEh's
multi-assistant routing. `/mcp` is the bearer endpoint; `/internal` carries the
session-token parameter. See `pkg/mcpserver/{mcpserver,tools,bearer}.go`.

## Why two models

The current `session_token` parameter (required on every tool call) does **two**
jobs at once:

1. **Authentication** — proves the caller is permitted.
2. **Routing** — `Resolve(token) → {agentID, sessionKey, channel, chatID}` selects
   *which agent/session* the call belongs to (for `ForUser` delivery, cogmem
   scoping, the per-agent ACL, and cross-agent rejection).

That dual role is why a bearer token alone never fit: a bearer is conventionally
**one identity per connection**, whereas ClawEh needs **per-call routing** so a
single endpoint can serve many assistants/sessions. The token is not a generic
API key — it is a session handle that also authenticates.

The two consumer classes genuinely want different transports:

| Consumer | Needs | Fits |
|---|---|---|
| **Standard / external** — `probe`, generic MCP CLIs, test harness | one identity per connection, `Authorization: Bearer`, clean tool schemas | header (bearer) |
| **Internal multi-assistant** — ClawEh's CLI providers calling back | one configured endpoint, a different token per call | in-call parameter |

Rather than overload one knob, expose **two endpoints**, each with exactly one
auth model. A token resolves identically regardless of transport (the token store
is transport-agnostic), so **the same token works on both**.

## The two endpoints

### `/mcp` — standard (bearer) endpoint  ← the public, universal surface

- Auth: `Authorization: Bearer <token>` (HTTP header). Read per request via the
  mcp-go server's `HTTPContextFunc(ctx, *http.Request)` and placed in the request
  context; dispatch resolves it through the shared token store.
- **Clean tool schemas** — tools do **not** carry a `session_token` parameter
  here. Standard MCP clients and `probe` see normal tools; auth lives at the HTTP
  layer where standard tooling expects it. This is the key property that makes a
  comprehensive `probe` suite practical.
- A missing/invalid bearer is rejected at the HTTP layer with a proper **`401`**
  (not a tool-result error), matching standard MCP client expectations.

This becomes the default, documented endpoint — what external integrators point
at.

### `/internal` — session-token-parameter endpoint  ← ClawEh's multi-assistant routing

- Auth + routing: the existing required **`session_token`** parameter
  (`SST<64 hex>`) on every tool call, exactly as today.
- Used by ClawEh's own CLI providers (and any caller that must drive multiple
  sessions over one configured endpoint, distinguishing them per call).
- Behavior is unchanged from the current `/mcp`; it simply moves to `/internal`
  so the path name advertises its purpose.

> **Migration:** the current in-call-token behavior **moves from `/mcp` to
> `/internal`**. Any existing MCP client config that points at `/mcp` with the
> `session_token` parameter (notably ClawEh's per-agent CLI provider MCP config)
> must be repointed to `/internal`. `/mcp` is repurposed as the bearer endpoint.

## Token model (shared by both endpoints)

- One `sessionTokenStore`. `Resolve(token)` returns the `{agentID, sessionKey,
  archiveDir, channel, chatID}` record regardless of how the token arrived
  (parameter on `/internal`, or `Authorization: Bearer` on `/mcp`).
- A bearer token **is** a session token, just transported in the header — not a
  new credential type. The same `SST<64 hex>` value is accepted both ways.
- Issuance: `Issue()` mints per-session tokens; `Register()` pre-mints a known
  token (used for tests); `RegisterService()` registers a long-lived per-agent
  **service token** for the `/mcp` bearer endpoint — the natural fit for "one
  identity per connection" — bound to a dedicated headless `agent:<id>:service`
  session. See [service-tokens.md](service-tokens.md). For a `probe` run, register
  a known token and hand it to `probe` as the bearer.
- Cross-agent protection still applies: a token that resolves to one agent cannot
  act as another, on either endpoint.

## Implementation shape (high level — no code here)

- Two `StreamableHTTPServer` instances over the **same** agent registries, ACL
  policy, and token store:
  - `/internal`: tools registered **with** the injected `session_token` param;
    dispatch reads the param (current path, verbatim).
  - `/mcp`: tools registered **without** the param; an `HTTPContextFunc` extracts
    the bearer into context; dispatch reads the token from context.
- The only difference between the two registrations is *inject-param* vs
  *read-header*, so duplication is minimal. Both mount on the existing shared mux.
- `WithStateLess(true)` is retained; the context func runs per request, so each
  request's bearer routes consistently (and a single client could even drive
  multiple sessions via per-request bearers, though that is unusual).

## Testing (`probe`)

The bearer endpoint is what enables a Maestro-style comprehensive suite: point
`probe` at `/mcp`, authenticate with a registered bearer token, and exercise
every tool against clean schemas — no `session_token` threading. The existing
`tests/test_mcpserver.sh` (which uses the parameter model) continues to target
`/internal` unchanged, and a new bearer-based suite targets `/mcp`.

A parity test should assert the two endpoints expose the **same tool set** and
ACL behavior, differing only by the `session_token` parameter.

## Security

- Both endpoints stay bound to `127.0.0.1` (same posture as today; see
  [callback.md](callback.md)). The bearer header is no weaker than the in-call
  parameter over the same transport.
- Exposing either endpoint beyond localhost is a separate TLS decision, out of
  scope here.

## Why not "one endpoint, optional parameter"

Making `session_token` optional and accepting either the parameter or a bearer on
a single endpoint was considered and rejected:

- It marks a parameter **optional that is semantically required for routing** — a
  client (or LLM) reasonably omits it and then fails at runtime, instead of being
  structurally prevented.
- It cannot present **both** a clean (no-param) schema to standard clients and the
  optional param — so external clients still see the non-standard parameter,
  losing the main benefit.
- It mixes transport-layer and parameter-layer auth in one branchy code path:
  more failure modes, ambiguous errors ("missing token" — which kind?), and
  harder exhaustive testing.
- Documentation collapses to "pass A or B, but you must pass one" — ambiguous for
  both humans and LLMs.

Two endpoints keep each model crisp and independently testable.
