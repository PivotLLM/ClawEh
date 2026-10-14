# Channels

A channel is a way for people to reach your agents: a chat app, the WebUI chat, or a device such as the Rabbit R1.

## How channels work

Each channel connects ClawEh to one chat service. A message that arrives on a
channel is handed to one agent, and the agent's reply goes back the same way.

- **Which agent answers** is decided by [bindings](./routing.md). A message that
  matches no binding goes to the
  [default agent](../agents/creating.md#the-default-agent-and-default-models).
- **One conversation per agent.** Every message to an agent, from any channel,
  joins that agent's single conversation, with one history and one memory. If
  Alice is reachable on Telegram, Slack and the WebUI chat, she remembers on
  Slack what she was told on Telegram. To keep two audiences apart, give each
  its own agent (see [Conversation and memory](../agents/memory.md)).
- **Who may write** is set per channel with **Allow From**. On every chat
  channel an empty list means nobody is allowed; add the user IDs that may
  write, or `*` for anyone.

## Supported channels

| Channel | Sidebar entry | Channel name used in bindings | Page |
|---|---|---|---|
| WebUI chat | **Chat** | `webui` | [WebUI chat](./webui-chat.md) |
| Telegram | **Channels → Telegram** | `telegram`, or `telegram-<id>` for each extra bot | [Telegram](./telegram.md) |
| Slack | **Channels → Slack** | `slack` | [Slack](./slack.md) |
| Discord | **Channels → Discord** | `discord` | [Discord](./discord.md) |
| Matrix | **Channels → Matrix** | `matrix` | [Matrix](./matrix.md) |
| SecMsg (Signal and similar, through a separate daemon) | **Channels → SecMsg** | `<daemon name>-<account>` | [SecMsg (Signal)](./secmsg.md) |
| Devices (Rabbit R1, voice apps) | **Services → Devices** | — (each device picks its agent) | [Devices](./devices.md) |

Channel settings save as you type. Saving reloads the configuration and
restarts the affected channel, so an open chat on that channel may be
interrupted briefly.

## When a message cannot be delivered

When ClawEh cannot deliver a message, it says why. The same words appear in
the log and in what the agent is told (for example, "Not sent:
device:… is offline."):

| Reason | Meaning | Alert? |
|---|---|---|
| **is offline** | A paired device that is not connected, or a WebUI chat with no browser open. | No, logged as a warning |
| **can't be reached** | The recipient does not exist or refuses the bot: an unknown or unpaired device, a Telegram chat that blocked the bot or removed it, a Slack conversation that is archived or does not include the app, an unknown Discord channel or user. | No, logged as a warning |
| **is not set up** | The message names a channel that is not configured. | No |
| **is unavailable** | The channel is configured but not running. | Yes |
| **couldn't reach** | The send failed and kept failing after its retries. | Yes |

Only real outages raise an alert: a channel that is not running, a send that
failed after its retries, a token the service rejects, a channel that has had
no working connection for ten minutes ("Telegram down: telegram-alice", followed by
"Telegram up: telegram-alice" when it recovers; the title names the channels), and a channel that keeps failing to start. An offline or
unreachable recipient is a normal situation and is not alerted or retried.
Alerts appear on the **Logs** page (choose the alerts source). See [Alerts](../admin/alerts.md).

## Related pages

- [Routing and bindings](./routing.md)
- [Devices](./devices.md)
- [Voice messages and transcription](./voice.md)
- [Message API](./message-api.md): send messages to an agent from other apps
