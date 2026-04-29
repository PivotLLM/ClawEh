# Slack Integration Setup

ClawEh connects to Slack using **Socket Mode**, which means no public webhook URL is required. The bot receives events over a persistent WebSocket connection.

You need two tokens:
- **Bot Token** (`xoxb-...`) — grants the bot permission to read and send messages
- **App Token** (`xapp-...`) — enables the Socket Mode connection

---

## 1. Create a Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App**.
2. Choose **From scratch**.
3. Give the app a name (e.g. `ClawEh`) and select your workspace.
4. Click **Create App**.

---

## 2. Enable Socket Mode

1. In the left sidebar, click **Socket Mode**.
2. Toggle **Enable Socket Mode** to on.
3. When prompted, create an App-Level Token:
   - Name: `socket-token` (or any name)
   - Scope: `connections:write`
4. Click **Generate** and copy the token — this is your **App Token** (`xapp-...`).

---

## 3. Configure OAuth Scopes (Bot Token)

1. In the left sidebar, click **OAuth & Permissions**.
2. Scroll to **Bot Token Scopes** and add the following:

| Scope | Purpose |
|---|---|
| `chat:write` | Send messages |
| `reactions:write` | Add/remove emoji reactions |
| `files:read` | Download files shared by users |
| `channels:history` | Read messages in public channels |
| `groups:history` | Read messages in private channels |
| `im:history` | Read direct messages |
| `mpim:history` | Read group direct messages |
| `channels:read` | List channels (optional) |
| `users:read` | Resolve user info (optional) |

3. Click **Install to Workspace** at the top of the page.
4. Authorize the app.
5. Copy the **Bot User OAuth Token** — this is your **Bot Token** (`xoxb-...`).

---

## 4. Subscribe to Events

1. In the left sidebar, click **Event Subscriptions**.
2. Toggle **Enable Events** to on.
   - No Request URL is needed — Socket Mode handles delivery.
3. Expand **Subscribe to bot events** and add:

| Event | Purpose |
|---|---|
| `message.channels` | Messages in public channels |
| `message.groups` | Messages in private channels |
| `message.im` | Direct messages |
| `message.mpim` | Group direct messages |
| `app_mention` | @mentions of the bot |

4. Click **Save Changes**.

---

## 5. (Optional) Add a Slash Command

If you want users to trigger the bot with a `/` command:

1. In the left sidebar, click **Slash Commands**.
2. Click **Create New Command**.
3. Set:
   - **Command**: e.g. `/ask`
   - **Request URL**: enter any placeholder URL (e.g. `https://example.com`) — Socket Mode ignores it
   - **Short Description**: e.g. `Ask ClawEh a question`
4. Click **Save**.

Slash command text is routed through the same message pipeline as regular messages.

---

## 6. Configure ClawEh

Add the following to your ClawEh configuration:

```json
{
  "channels": {
    "slack": {
      "enabled": true,
      "bot_token": "xoxb-...",
      "app_token": "xapp-...",
      "allow_from": ["U01234ABCDE"]
    }
  }
}
```

Or using environment variables:

```
CLAW_CHANNELS_SLACK_ENABLED=true
CLAW_CHANNELS_SLACK_BOT_TOKEN=xoxb-...
CLAW_CHANNELS_SLACK_APP_TOKEN=xapp-...
```

**`allow_from`** is a list of Slack user IDs (e.g. `U01234ABCDE`). Only these users will be able to interact with the bot. To find a user ID in Slack, click their profile → **More** → **Copy member ID**.

---

## 7. Group Channel Behaviour

By default in public/private channels (non-DM), the bot responds to all messages. To restrict it to @mentions or keyword prefixes only, set `group_trigger` in your config:

```json
"group_trigger": {
  "mention_only": true
}
```

Or with keyword prefixes:

```json
"group_trigger": {
  "prefixes": ["!ask", "hey bot"]
}
```

In direct messages (DMs), the bot always responds without any trigger requirement.

---

## Troubleshooting

**Bot does not respond**
- Confirm the bot has been invited to the channel (`/invite @ClawEh`).
- Check that the sending user's ID is in `allow_from`.
- Verify both tokens are correct and Socket Mode is enabled.

**`invalid_auth` error on startup**
- The Bot Token or App Token is wrong or has been revoked. Regenerate from the Slack app settings.

**`missing_scope` error**
- A required OAuth scope is missing. Return to **OAuth & Permissions** and add the missing scope, then reinstall the app to the workspace.

**App Token is missing or wrong**
- The App Token must start with `xapp-`. If it starts with `xoxb-` you have the Bot Token in the wrong field.
