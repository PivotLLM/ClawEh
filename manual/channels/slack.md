# Slack

Connect a Slack app so people can talk to your agents in direct messages and Slack channels.

ClawEh uses Slack's Socket Mode, so you do not need a public address. You need
two tokens from Slack: a **Bot Token** (starts with `xoxb-`) and an **App
Token** (starts with `xapp-`).

## Create the Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps), click **Create New
   App**, choose **From scratch**, name the app (for example `Alice`) and pick
   your workspace.
2. **Socket Mode**: in the left sidebar click **Socket Mode** and turn on
   **Enable Socket Mode**. Create the app-level token it asks for with the
   scope `connections:write` and copy it. This is the **App Token**.
3. **OAuth & Permissions**: under **Bot Token Scopes** add:

   | Scope | Used for |
   |---|---|
   | `chat:write` | sending messages |
   | `reactions:write` | adding and removing reactions |
   | `files:read` | downloading files people share |
   | `channels:history` | reading public channels |
   | `groups:history` | reading private channels |
   | `im:history` | reading direct messages |
   | `mpim:history` | reading group direct messages |
   | `commands` | the `/claw` slash command |
   | `channels:read`, `users:read` | optional: looking up channels and people |

   Click **Install to Workspace**, approve, and copy the **Bot User OAuth
   Token**. This is the **Bot Token**.
4. **Event Subscriptions**: turn on **Enable Events** (no Request URL is
   needed) and under **Subscribe to bot events** add `message.channels`,
   `message.groups`, `message.im`, `message.mpim` and `app_mention`. Click
   **Save Changes**.
5. **App Home**: under **Show Tabs**, turn on **Messages Tab** and tick
   **Allow users to send Slash commands and messages from the messages tab**.
   Without this, people see "Sending messages to this app has been turned
   off".
6. **Slash Commands**: click **Create New Command**, enter `/claw` as the
   command, give it a short description (for example `Run a ClawEh command`)
   and click **Save**. No Request URL is needed in Socket Mode. Then
   reinstall the app to the workspace (Slack asks for the `commands` scope).

If you add a scope later, reinstall the app to the workspace and paste the new
Bot Token.

## Connect ClawEh

1. In the WebUI sidebar, open **Channels → Slack**.
2. Turn on **Enable channel**.
3. Paste the **Bot Token** and the **App Token**.
4. In **Allow From**, enter the Slack member IDs of the people who may write,
   separated by commas (for example `U01234ABCDE`). To find a member ID, open
   the person's profile in Slack, click **More** and choose **Copy member ID**.
   An empty list lets nobody in; `*` lets everyone in the workspace in.
5. Invite the app into each Slack channel where it should listen
   (`/invite @Alice` in that channel).
6. Test it: in Slack, open the app under **Apps** and send it a direct message.
   The agent bound to Slack (or the default agent) should answer.

Settings save as you type.

## Which agent answers

The channel name is `slack`. On the **Bindings** page, choosing the channel
`slack` shows **Slack routing**:

- **All messages**: every Slack message goes to the chosen agent.
- **Specific channel**: only messages in one Slack channel. Enter its channel
  ID (for example `C0123456789`; in Slack, open the channel details to find it).
- **Direct messages**: every direct message to the app.

A binding for a specific channel or for direct messages wins over one for all
messages, so you can send one channel to Bob and everything else to Alice. To
send one person's direct messages to a different agent, add a binding in
`config.json` with the peer kind `direct` and that person's member ID (see
[Routing and bindings](./routing.md#bind-one-chat-to-an-agent)); it wins over
the **Direct messages** binding.

## Channels and direct messages

In direct messages the app always answers. In Slack channels it answers every
message by default. To answer only when mentioned, or only messages that start
with a keyword, set `group_trigger` in `config.json`:

```json
{
  "channels": {
    "slack": {
      "group_trigger": { "mention_only": true }
    }
  }
}
```

or `"group_trigger": { "prefixes": ["!ask"] }`.

## Commands in Slack

ClawEh commands such as `/model` can be run these ways:

- **`/claw model`**: the `/claw` slash command runs the command after it;
  `/claw` alone shows the help. It needs step 6 above.
- **`/model 0`**: a Slack slash command with the same name as a ClawEh
  command runs that command. Add it the way step 6 adds `/claw`, once for each
  command you want (for example `/model` or `/cancel`).
- **` /model`** with a leading space: Slack only treats a message as a slash
  command when `/` is its first character, so the space sends it as a message.
- **`!model`**: `!` works like `/` and needs no space.

The answer appears where the command was typed. If the app is not in that
channel, only you see the answer to a slash command; invite the app to
answer there.

A slash command under any other name, such as `/claw`, treats its first word as
the command. If that word is not a ClawEh command, the text goes to the agent as
an ordinary message.

## When something goes wrong

- **No answer at all**: check that the app was invited to the channel, that
  the sender's member ID is in **Allow From**, and that Socket Mode is on.
- **A message to an archived channel, or to a channel the app is not in**:
  ClawEh records that the chat **can't be reached** and logs a warning; there
  is no alert and no retry.
- **Channel credentials rejected** ([alert](../admin/alerts.md)): Slack refused the token. Create a
  new token in the app settings and paste it in. An App Token must start with
  `xapp-`; if it starts with `xoxb-`, the two tokens are swapped.
- **Slack down** (alert): no working connection for ten minutes. ClawEh keeps
  reconnecting and raises **Slack up** when it is back.
- **Shared files cannot be read**, and the log mentions the Slack login page:
  the Bot Token lacks `files:read`. Add the scope, reinstall the app and paste
  the new token.

## Related pages

- [Routing and bindings](./routing.md)
- [Channels overview](./index.md)
