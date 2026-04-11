# Callback endpoint

The callback endpoint lets external processes — MCP servers, scripts, spawned subprocesses — deliver a message to an agent without a persistent channel connection. The agent receives the message on its last active channel and responds normally, just as if the user had sent it.

---

## How it works

1. Callback is enabled per agent with a rotating token.
2. The current token is injected into the agent's system prompt (marked confidential) so the agent can pass it to any subprocess or MCP server that needs to call back.
3. The external process posts a plain-text message body to the endpoint.
4. ClawEh validates the token, delivers the message to the agent, and the agent responds on its last active channel (Telegram, Slack, etc.).

**Requirement:** The agent must have had at least one conversation before the callback will work. The system routes the response to the agent's most recent channel — if the agent has never spoken on any channel, there is nowhere to send the reply.

---

## Endpoint

```
POST http://localhost:18790/api/reply/{token}
```

- **Body:** plain text — the message to deliver to the agent
- **Response:** `202 Accepted` on success, `401 Unauthorized` for an invalid/expired token, `400 Bad Request` for an empty body

**Example:**

```bash
curl -X POST http://localhost:18790/api/reply/YOUR_TOKEN \
  -H "Content-Type: text/plain" \
  --data "The background task has completed. Results: all checks passed."
```

---

## Configuration

Set per agent in `agents.list`:

```json
{
  "id": "alice",
  "callback": {
    "window_minutes": 30,
    "window_count": 2
  }
}
```

| Field | Type | Description |
|---|---|---|
| `window_minutes` | int | Token rotation interval in minutes. Set to `0` or omit to disable. |
| `window_count` | int | Number of rotation windows to retain. |

Callback configuration can also be managed through the web console under **Agents**.

---

## Token rotation

Tokens rotate on a fixed schedule to limit the window of exposure if a token is leaked.

- A new token is generated every `window_minutes` minutes.
- The previous `window_count` tokens remain valid, so a token is accepted for `window_minutes × window_count` minutes total.
- On rotation, tokens older than `window_count` windows are discarded.

**Example:** `window_minutes: 30`, `window_count: 2` — each token is valid for up to 60 minutes, and a new one is issued every 30 minutes.

The token is injected into the agent's system prompt at each conversation turn. The agent knows the current token and can give it to a subprocess or MCP server. If the token has rotated by the time the subprocess calls back, the previous token is still accepted (within `window_count` windows).

---

## Routing

When the callback message arrives, ClawEh delivers it to the agent's **last active channel** — the most recent channel and peer the agent had a real conversation on (Telegram, Slack, etc.). The agent replies there, just as it would for any other message.

This means:

- The agent must have had at least one prior conversation on a real channel before callbacks will work.
- If the agent's last conversation was on a different channel than expected, replies will go there. Send the agent a message on the intended channel first to update its routing state.
- The callback message does not appear in the channel — only the agent's response does.

---

## Security

- The endpoint is served over **plain HTTP** on the gateway port (`127.0.0.1:18790` by default).
- It is bound to **localhost only** and must not be exposed to external networks.
- Do not place this endpoint behind a reverse proxy that allows external access without additional authentication.
- The rotating token provides a basic layer of access control but is not a substitute for network-level security.
- The token is marked confidential in the system prompt. Claw instructs the agent not to share it with users, but a redaction pass (see [TODO.md](../TODO.md)) is planned as an additional safeguard.

---

## Troubleshooting

**`401 Unauthorized`** — the token is invalid or has expired beyond `window_count` windows. Check that you are using the token from the agent's system prompt and that it has not rotated past the retention window.

**`202` but no response on the channel** — the agent's last active channel is stale or points to the wrong platform. Send the agent a message on the intended channel first, then retry the callback.

**Response goes to the wrong agent** — each agent has its own token. Verify you are using the token for the correct agent. Tokens are per-agent and are not interchangeable.
