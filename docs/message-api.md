# Message API (named tokens)

The Message API lets an external app deliver a one-off message to a chosen
assistant over HTTP. Delivery behaves like a **scheduled event firing**: the
message is handed to the agent as an unsolicited event on its **default channel**,
not as part of an existing conversation. The agent processes it and replies on
that channel — no prior chat is required.

Typical callers: a GPS tracker posting a geofence alert, an alarm panel reporting
a trip, a monitoring/CI webhook reporting a failed check.

---

## Endpoint

```
POST http(s)://<host>:<port>/api/message/<token>
```

- The **token is a URL path segment**, not a query parameter.
- **Body:** plain text — the message delivered to the agent.
- **Response:** `202 Accepted` on success, `401 Unauthorized` for an unknown
  token, `400 Bad Request` for an empty body.

The full URL a caller uses is the endpoint base shown in the Web UI with the
token appended, e.g. `https://gateway.example.com:18790/api/message/ab12…`.

---

## Creating and revoking tokens

Named tokens are managed **per agent** in the Web UI:

1. Open **Agents** and select the assistant.
2. In the **Message API tokens** card, enter a name (e.g. `gps-tracker`) and
   click **Add token**.
3. Copy the token (or the full endpoint URL) into your external app.
4. Revoke a token any time with **Revoke** — it stops working immediately.

Each agent can have any number of named tokens; each is a long-lived secret that
does not expire until revoked. Tokens are stored in plaintext under the data
directory at `state/message-api-tokens.json` so they can be displayed for copying.

---

## Delivery

The message is delivered to the agent's **default channel** (the binding marked
default in **Agents → Channels**). If the agent has no default channel the request
fails, because there is nowhere to deliver the event.

The raw body is preserved but wrapped with a security notice so the model treats
external input as untrusted and does not blindly follow instructions inside it.

---

## Examples

GPS tracker geofence alert:

```bash
curl -X POST "https://gateway.example.com:18790/api/message/$TOKEN" \
  --data "Vehicle 7 left the depot geofence at 14:32 (45.42, -75.69)."
```

Alarm system trip:

```bash
curl -X POST "https://gateway.example.com:18790/api/message/$TOKEN" \
  --data "ALARM: zone 3 (garage) motion detected. System is armed-away."
```

Monitoring webhook (payload as the message body):

```bash
curl -X POST "https://gateway.example.com:18790/api/message/$TOKEN" \
  -H "Content-Type: text/plain" \
  --data "check=disk-space host=web01 status=CRITICAL used=96%"
```

---

## Reachability

By default the gateway HTTP server binds to loopback. To accept posts from other
hosts:

- Start the gateway with `--host 0.0.0.0` (or set `gateway.host`) so it listens on
  the LAN.
- Keep the network allowlist (`--allowed-cidrs`) tight — only the source ranges
  that must reach the endpoint.
- Prefer putting the gateway behind a TLS-terminating reverse proxy for any access
  beyond the local network; the token is bearer access control, not a substitute
  for transport security.

---

## Notes

- This named-token mechanism is separate from the older rotating per-agent
  message tokens (see [external-messages.md](external-messages.md)); both post to
  the same `/api/message/<token>` route.
- For an external **MCP** client that needs to drive an agent's tools on a stable
  credential, use a [service token](service-tokens.md) instead.
