# Routing and bindings

Bindings decide which agent answers which chat; anything no binding covers goes to the default agent.

## How a message finds its agent

When a message arrives, ClawEh looks for the most specific binding that
matches it, in this order:

1. a binding for this exact chat (a Telegram group, a Slack channel, one
   person's direct messages);
2. a binding for the Telegram forum topic the message belongs to;
3. a binding for every direct message on the channel (Slack's **Direct
   messages**);
4. a binding for the whole Discord server;
5. a binding for the whole Slack workspace;
6. a binding for the whole channel (for example `telegram-alice` or `slack`);
7. otherwise, the [default agent](#the-default-agent).

Whichever agent is chosen, the message joins that agent's one conversation.
An agent has a single conversation and memory across all channels, so a
binding chooses *who* answers, not a separate history.

Devices are not routed by bindings: each device has its own agent setting
(see [Devices](./devices.md#which-agent-a-device-talks-to)).

## Add a binding

1. Open **Agents → Bindings**.
2. Click **Add Binding**.
3. Choose the **Channel**, for example `telegram-alice`. The list shows the
   channels that are turned on.
4. Choose the **Agent**.
5. For Slack, choose the **Slack routing**: **All messages**, **Specific
   channel** with the Slack channel ID, or **Direct messages** for every
   direct message to the bot. A binding for one specific channel or person
   still wins over **Direct messages**.
6. Optional: fill in **Agent mentions** (see [Mentions](#mentions)).
7. Click **Add**.

The table lists every binding with its **Channel**, **Peer** (the chat it is
limited to, if any), **Agent** and **Agent Mentions**. Hover over the
mentions cell and click the pencil to change them, or click the trash button
to delete a binding.

With no bindings at all, the page says "No bindings configured — all messages
route to the default agent."

## Bind one chat to an agent

To send one Telegram group, one Discord channel or one person's direct
messages to a different agent than the rest of the channel, add a binding with
a `peer` in `config.json`. The peer kinds are:

| Kind | Channels | ID |
|---|---|---|
| `direct` | Telegram, Slack, Discord, Matrix, LINE, SecMsg | the person's user ID on that service |
| `group` | Telegram, Matrix, LINE, SecMsg | the group's chat or room ID |
| `channel` | Slack, Discord | the channel ID |

Example: the Telegram bot `telegram-alice` answers as Alice, except in one
group, where Bob answers:

```json
{
  "bindings": [
    {
      "agent_id": "alice",
      "match": { "channel": "telegram-alice" }
    },
    {
      "agent_id": "bob",
      "match": {
        "channel": "telegram-alice",
        "peer": { "kind": "group", "id": "-1001234567890" }
      }
    }
  ]
}
```

A peer with a `kind` and no `id` matches every chat of that kind on the
channel: `"peer": { "kind": "direct" }` sends every direct message to the
binding's agent, while groups and channels go on to the other bindings.

A Discord binding can name a whole server with `"guild_id"` in `match`, and a
Slack binding a whole workspace with `"team_id"`.

A binding that names an agent that does not exist sends its messages to the
default agent.

## The default channel

An agent's default channel is where its [scheduled](../agents/scheduling.md) (cron) output and other
messages it starts on its own are delivered. Each agent has at most one.

1. Open **Agents → Agents** and select the agent.
2. In the **Default Channel** section, pick one of the channels bound to the
   agent.
3. If that binding covers a whole channel (for example a Telegram bot), enter
   the chat ID to deliver to. This chat ID is used only for delivery; it does
   not change who may write to the bot.

The WebUI chat cannot be a default channel, because it has no fixed address.
An agent without a default channel cannot deliver scheduled output.

## Mentions

A binding's **Agent mentions** let people in that chat address another agent
by starting the message with its ID:

```
@bob what is the weather tomorrow?
```

If the binding lists `bob` in its agent mentions (or `*` for every agent), the
message goes to Bob instead of the binding's agent, without the `@bob`. If Bob
is not listed, the binding's agent answers.

- The message must start with the mention, followed by a space.
- Besides `@`, the prefixes `/` and `.` work too (`/bob …`, `.bob …`). To
  change them, set `agent_mentions.triggers` in `config.json`, for example
  `"agent_mentions": { "triggers": ["@"] }`.
- Mentions only work in chats that match a binding; a message that falls
  through to the default agent cannot be redirected this way.
- A binding that names Bob as its agent or in its agent mentions also lets
  people in that chat use `/ask Bob …` and `/whisper Bob …` (see
  [Messages between agents](../agents/messaging.md#ask-and-whisper-from-a-chat)).

## The default agent

The default agent answers every message that matches no binding, and the
WebUI chat when it has no binding.

1. Open **Services → System**.
2. In **Agent defaults**, choose the **Default agent**.

If none is chosen, the first agent in the list is the default. An agent that
represents a person (a [human agent](../agents/human-agents.md)) is never the default.

## Related pages

- [Channels overview](./index.md)
- [Telegram](./telegram.md), [Slack](./slack.md), [Discord](./discord.md), [Other chat channels](./other.md)
- [Devices](./devices.md)
