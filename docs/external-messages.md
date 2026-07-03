# External message endpoint

The external message endpoint lets an outside process deliver a message into an
agent's active conversation without a persistent channel connection. The agent
receives the message on its last active channel and responds normally, as if the
user had sent it.

> **Status / scope.** This endpoint is **not advertised to the agent** — the
> rotating token is no longer injected into the system prompt. It is retained for
> operator/integration use and as the delivery path for a future "notify an
> agent" feature. For an external **MCP** client that needs to drive an agent's
> tools (e.g. Maestro) on a stable credential, use a **service token** instead —
> see [service-tokens.md](service-tokens.md).

---

## Endpoint

```
POST http://localhost:18790/api/message/{token}
```

- **Body:** plain text — the message to deliver to the agent.
- **Response:** `202 Accepted` on success, `401 Unauthorized` for an
  invalid/expired token, `400 Bad Request` for an empty body.

```bash
curl -X POST http://localhost:18790/api/message/YOUR_TOKEN \
  -H "Content-Type: text/plain" \
  --data "The background task has completed. Results: all checks passed."
```

**Requirement:** the agent must have a **default channel** configured (a default
binding). The message is delivered there as an unsolicited event, so no prior
conversation is needed; if there is no default channel there is nowhere to
deliver and the request fails.

---

## Configuration

Enabled per agent in `agents.list`:

```json
{
  "id": "alice",
  "message": {
    "window_minutes": 30,
    "window_count": 2
  }
}
```

| Field | Type | Description |
|---|---|---|
| `window_minutes` | int | Token rotation interval in minutes. `0` or omitted disables the endpoint for the agent. |
| `window_count` | int | Number of rotation windows to retain (a token stays valid for `window_minutes × window_count`). |

Also editable in the web console under **Agents**.

---

## Tokens

Per-agent rotating tokens, persisted at `<workspace>/state/message-tokens.json` and
minted by a background rotation goroutine:

- A new token is generated every `window_minutes`.
- The previous `window_count` tokens remain valid, so any token is accepted for
  up to `window_minutes × window_count` minutes.

Because the token is **no longer placed in the agent's prompt**, an operator or
integration that wants to use this endpoint today reads the current token from
the agent's `message-tokens.json`. (A stable, non-rotating credential for MCP tool
access is a [service token](service-tokens.md), not this.)

---

## Routing

The message is delivered to the agent's **default channel** (the binding marked
default in **Agents → Channels**), as an unsolicited event — the same delivery
path a scheduled/cron job uses. It does **not** continue an existing conversation,
so the agent no longer needs a prior chat for the endpoint to work. If the agent
has no default channel the request fails. Incoming content is prefixed with the
configured security marker and treated as untrusted (it must not be obeyed as
instructions).

For long-lived, named, per-agent tokens managed in the Web UI, see
[message-api.md](message-api.md).

---

## Security

- Served over **plain HTTP**, bound to **`127.0.0.1` only**. Do not expose it
  externally or place it behind a proxy that allows outside access.
- The rotating token is basic access control, not a substitute for
  network-level security.
- Token-shaped values (`AGT`/`SST`) are redacted from logs and tool output.

---

## Troubleshooting

- **`401 Unauthorized`** — token invalid or rotated beyond `window_count`
  windows. Re-read the current token from the agent's `message-tokens.json`.
- **`202` but no response** — the agent's last active channel is stale or on a
  different platform; send it a message on the intended channel first, then retry.
- **Response goes to the wrong agent** — tokens are per-agent and not
  interchangeable; verify you are using the right agent's token.
