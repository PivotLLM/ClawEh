# ACP over stdio (`claw acp`)

`claw acp` serves the **Agent Client Protocol (ACP)** over stdin/stdout and
**bridges each prompt to the already-running gateway over its localhost
WebSocket**. It exists for the Rabbit R1's new connection path: the device runs
`rabbit-agent`, which spawns a local ACP agent process and drives it over the
pipe (previously the R1 spoke the OpenClaw Gateway WebSocket protocol directly —
see `device-gateway-protocol.md`).

`rabbit-agent` is configured to spawn `claw acp` (in place of upstream
`openclaw acp`). There is **one** ClawEh instance: the running gateway. `claw acp`
is a thin, stateless translator holding no agent loop of its own.

```
rabbit-agent  ──ACP stdio──►  claw acp (bridge)  ──WS 127.0.0.1:<devicePort>──►  the ONE running gateway
```

This is the same shape as upstream `openclaw acp` (ACP stdio ↔ gateway WS), and
conceptually just *relocates* the R1's connection to localhost: instead of the
R1 reaching the device gateway over the network, `rabbit-agent` spawns the bridge
locally and the bridge reaches the device gateway over loopback — same
auth/pairing model, already proven with the R1.

## What ACP is

ACP is **JSON-RPC 2.0 over NDJSON** (newline-delimited JSON) on stdio. The client
spawns the agent, writes requests on the agent's **stdin**, and reads responses +
notifications on the agent's **stdout**. Stdin EOF = shutdown. Reference:
<https://agentclientprotocol.com>.

The wire layer is the MIT-licensed [`github.com/a3tai/openclaw-go`](https://pkg.go.dev/github.com/a3tai/openclaw-go)
module — its `acp` package (the ACP server) and its `gateway` package (the
gateway WebSocket client). ClawEh implements only the glue between them.

### Turn lifecycle

```
client → initialize     → agent: {protocolVersion, agentInfo, authMethods:[]}
client → session/new    → agent: {sessionId}
client → session/prompt {sessionId, prompt:[{type:"text",text}]}
              (blocks for the turn)
   bridge → gateway: chat.send {sessionKey:"main", message, idempotencyKey:runId}
   gateway → bridge: agent event (stream:"assistant") …          ← streamed
         agent → session/update {agent_message_chunk, content:{text}}   ← per delta
   gateway → bridge: agent lifecycle end  /  chat final
client ← session/prompt result  ← agent: {stopReason:"end_turn"}
```

`session/prompt` blocks until the gateway signals the turn is complete
(`agent` `stream:"lifecycle"` `phase:"end"`, or `chat` `state:"final"`).
Streamed assistant text (`agent` `stream:"assistant"`, `data.delta`) is forwarded
as additive ACP `agent_message_chunk` notifications. `chat` deltas are ignored to
avoid double text (the `agent` stream already carries it).

## Auth & pairing (the local hop)

The bridge authenticates to the gateway **as a paired device**, exactly like the
R1:

- **Ed25519 device identity**, generated and persisted under
  `$CLAW_HOME/state/acp-bridge/` (via the library's `identity.Store`). It survives
  the short-lived spawns `rabbit-agent` makes, so pairing happens only once.
- **Auth token**: the configured device-channel `token` (or `word_token`) is
  presented on connect; once the gateway issues a device token at connect
  (`hello-ok.auth.deviceToken`), it is persisted and used thereafter.
- **Pairing**: on first connect the device is unpaired → the gateway records a
  pending pairing and rejects the connect. Approve it once with `claw devices`
  (or set `channels.device.auto_approve` for a trusted LAN), then re-run. There is
  no ACP-layer auth — the stdio pipe is the trust boundary.

## Running it

```
claw acp                         # bridge to ws://127.0.0.1:<device-port>/
claw acp --url ws://127.0.0.1:8078/   # explicit gateway URL
claw acp --debug                 # debug logging (file only — stdout is the wire)
```

Because stdout carries the protocol, the console logger is disabled and all
logging goes to `$CLAW_HOME/logs/claw.log`.

### One-time pairing

1. Ensure the gateway is running with the device channel enabled.
2. Run `claw acp` once. It logs its `deviceId` and exits with a "pairing required"
   message.
3. Approve the pending device with `claw devices` (list → approve).
4. Re-run `claw acp` — it connects and serves.

## Content types

- **Text and images.** `text` blocks form the message; `image` blocks (base64,
  as the R1 sends photos) are forwarded to the gateway as `chat.send`
  `attachments`. The device server decodes them, saves each to the media store,
  and passes `media://` refs to the agent loop — which materializes them and, via
  the existing vision-describe path, sends them to a vision model (or describes
  them with the configured `vision_model` side-model if the agent's model is not
  vision-capable). `audio`/`resource` blocks are still ignored (the R1's voice
  already arrives as text via rabbit-agent's STT).

## Limitations (v1)

- **One conversation per bridge.** The bridge connects as a single node device, so
  all ACP sessions map to that device's `main` conversation (matching how the R1
  behaves today). Per-ACP-session isolation is not implemented.
- **Mid-turn cancel is limited.** The ACP library dispatches requests serially on
  its read loop, so a `session/cancel` sent while a `session/prompt` is in flight
  is not processed until the prompt returns. Clients abort via stdin EOF.
- **No `session/load`/`list`/`fork`/`resume`.** These return errors; `set_*`
  methods are accepted as no-ops.

## Code

- `internal/gateway/acp_command.go` — the `claw acp` subcommand: identity/token
  persistence, the gateway WebSocket connection + pairing, and the ACP server.
- `internal/gateway/acp_bridge.go` — `acpBridge` (the `acp.Handler`): forwards
  prompts via `chat.send` and translates gateway `agent`/`chat` events into ACP
  `session/update` notifications + turn completion.
- `internal/gateway/acp_bridge_test.go` — unit tests over the translation logic
  with a fake gateway (streamed, chat-final, stray-event, empty-prompt paths).

## Status

Compiles; unit-tested. **The live pairing handshake against the real device
server has not yet been exercised end-to-end** — connection/pairing, the `node`
role handshake, and the exact event shapes should be confirmed against a running
instance (see the one-time pairing steps above).
