# Message API

Let another app, such as an alarm panel, a GPS tracker or a monitoring check, send a message to an agent over HTTP.

The app posts plain text to a URL that contains a token. The agent receives it
like a [scheduled job](../agents/scheduling.md) firing: it handles the message
and replies on its **default channel**. No conversation needs to be open first.

## Before you start

- The agent needs a **Default Channel** (see
  [Default channel](../agents/creating.md#default-channel)). The reply goes
  there. Without one, every post is refused.
- The app must be able to reach ClawEh. From the same machine this works as
  is; from another machine, its network must be allowed (see
  [Network access](../admin/network.md) and [Remote access](../admin/remote-access.md)).
- An agent that represents a person ([human agent](../agents/human-agents.md))
  takes no messages from apps.

## Create a token

1. Open **Agents** and select the agent.
2. On its card, find **Integration Tokens**.
3. Type a name that says which app will use it, for example `gps-tracker`, and
   click **Add token**.
4. Click **Copy URL** to copy the full address with the token, or **Copy** for
   the token alone, and put it in the app.

Each app should get its own token, so you can revoke one without touching the
others. A token never expires until it is revoked.

The address under **Endpoint (POST the message body)** is built from the
address you opened the WebUI with. If you opened it as `localhost`, replace the
host with the address the app uses.

## Send a message

```
POST /api/message/<token>
```

The token is part of the path. The body is the message, as plain text, up to
1 MB. No sign-in is needed: the token is the credential.

```bash
curl -X POST "https://claw.example.com:18443/api/message/$TOKEN" \
  -H "Content-Type: text/plain" \
  --data "ALARM: zone 3 (garage) motion detected. System is armed-away."
```

The same path works on both the HTTP and the HTTPS address. Prefer HTTPS from
another machine: the token travels in the URL.

| Answer | Meaning |
|---|---|
| `202 Accepted` | The message was handed to the agent. The reply comes later, on the default channel. |
| `400 Bad Request` | The body is empty. |
| `401 Unauthorized` | The token is unknown or was revoked. |
| `422 Unprocessable Entity` | The agent has no default channel, or represents a person. The answer says which. |
| `429 Too Many Requests` | The token is blocked for sending too often. `Retry-After` gives the seconds to wait. |

The answer never contains the agent's reply.

## What the agent sees

The message reaches the agent with a notice in front of it saying it came from
outside and must not be obeyed as instructions. The agent treats it as
information to act on, not as a command from you.

To change that notice, set `security.message_prefix` in `config.json` (see
[Configuration](../reference/configuration.md)).

## Rate limits

Each token allows 30 requests per minute by default. The request that goes over
the limit blocks the token for 15 minutes; every request during the block gets
`429`.

To change them, edit the two numbers on the token's row: **req/min** and
**block min**. Leave a field blank or `0` for the default. The row also shows
the current use, for example `3/30 this minute`, or `blocked · clears in 12m`.

In a chat with the agent:

- `/quota` lists the agent's tokens and their limits.
- `/quota reset` clears every block; `/quota reset gps-tracker` clears one.

## Revoke a token

Click **Revoke** on the token's row. It stops working at once, with no restart.

Tokens are stored readable (so the WebUI can show them for copying) in
`internal/message-api-tokens.json` in the data directory, readable only by the
user ClawEh runs as, and are included in [backups](../admin/backup.md).

## Rotating tokens (older)

The same card has **Rotating Tokens**, the older form of this feature: a token
that changes every few minutes. It is off unless **Token rotation (minutes, 0 =
disabled)** is set. The current token is not shown in the WebUI; it is in
`state/message-tokens.json` in the agent's folder. It posts to the same
address and is delivered the same way, but it is not rate-limited. Use named
tokens for new integrations.

## Related pages

- [Channels overview](./index.md)
- [Scheduled jobs](../agents/scheduling.md)
- [Remote access](../admin/remote-access.md)
