# Chat commands

This page lists every command a person can type in a chat with an agent, what it does and what it answers.

## How commands work

- A command is a message that starts with `/`, for example `/status`. `!` works the same way (`!status`), which helps where the chat app treats `/` itself, as Slack does.
- Command names are not case-sensitive. In Telegram, `/status@yourbot` also works, and the bot lists its commands in Telegram's command menu: type `/` to see them.
- A command is answered by ClawEh itself and is not sent to the agent's model. `/compact` uses the memory models to summarize, and `/ask` and `/retry` start a turn.
- A command acts on the agent the chat reaches (see [Routing and bindings](../channels/routing.md)). Because each agent has [one conversation](../agents/memory.md#one-conversation-per-agent), a command that changes the conversation changes it for every chat that talks to that agent.
- While the agent is working on a request, a command waits behind it. `/cancel` is the exception: it acts at once.
- An unknown command gets "Unknown command: /name. Type /help for available commands."

### Who may use them

Anyone whose messages reach the agent may use every command. There is no separate operator role in a chat. Who can reach the agent is set per channel by **Allow From** (see [Channels](../channels/index.md)).

Some commands act on the whole install, not just one agent: `/cooldowns clear` and `/retry` clear the model pauses for every agent. Keep that in mind before letting other people talk to your agents.

## Conversation

| Command | What it does |
|---|---|
| `/cancel` | Stops the request the agent is working on and drops the messages queued behind it. Replies, for example, "Cancelled the current request and 2 pending message(s)." or "No pending messages to cancel." |
| `/clear` | Starts the conversation afresh and cancels messages waiting behind it. Long-term memory and the archive are kept. Reply: "Context cleared, but long-term memory preserved." Alias: `/reset`. |
| `/compact` | Summarizes the conversation now, and replies with what was tried and the result. When there is nothing to summarize: "Already compact — nothing to summarize." |
| `/retry` | Clears every model pause ([cooldowns](../providers/concepts.md#cooldowns)) and sends your last message again. |
| `/reasoning [on\|off]` | Shows or sets whether the model's reasoning is sent to you. Off by default. Without an argument it shows the current setting. |
| `/tools [on\|off]` | Shows or sets whether you see a one-line note for each tool the agent uses. Off by default. |

`/reasoning` and `/tools` also accept `yes`/`no`, `true`/`false`, `enable`/`disable` and `1`/`0`. Both settings, and the model chosen with `/model`, are saved with the conversation and kept across restarts.

`/clear`, `/compact` and long-term memory are covered in [Conversation and memory](../agents/memory.md).

## Models

| Command | What it does |
|---|---|
| `/list models` | Lists the agent's models, numbered from 0, and marks the one in use with ▶. |
| `/model <n>` | Switches the conversation to model number `n`. Reply: "Model set to 1: …". Without a number it shows the usage and the list. |
| `/show model` | Shows the model in use and its provider. |
| `/cooldowns list` | Lists the models that are paused after failures, with the reason and when the pause ends. The list covers the whole install. |
| `/cooldowns clear` | Ends every pause. Reply: "Cleared all provider cooldowns." |
| `/cooldowns clear <provider>/<model>` | Ends the pause of one model, for example `/cooldowns clear openai/gpt-5`. |

An agent with no models of its own answers `/list models` and `/model` with "This agent has no configured models; it uses the global default model."

Example:

```
/list models
Configured Models:
▶ 0: claude-sonnet  (anthropic)
  1: gpt-5-mini  (openai)

Use /model <n> to switch.
/model 1
Model set to 1: gpt-5-mini
```

See [Providers, models and fallback](../providers/concepts.md).

## Memory

| Command | What it does |
|---|---|
| `/memory` | Shows the agent's long-term memory: domains, messages not yet consolidated, and the last consolidation. With **Cognitive memory** off: "Cognitive memory is not enabled for this agent." |

See [Conversation and memory](../agents/memory.md).

## Agents and messages

| Command | What it does |
|---|---|
| `/ask <agent> <message>` | Asks another agent and posts its reply in this chat as "Bob: …". The chat is not held while Bob works. |
| `/whisper <agent> <message>` | Leaves a private note that the other agent sees at the start of its next message. Reply: "Whispered to Bob." |
| `/list agents` | Lists the agent ids, for example "Registered agents: alice, bob". `/show agents` is the same. |

The agent can be named by id or name. `/ask` and `/whisper` work only for an agent this chat may already talk to: the chat routes to it, or a binding for this chat names it as its agent or in its agent mentions. Otherwise the reply is "You don't have permission to /ask Bob." Messages are limited to 8,000 characters.

```
/ask Bob what is on today's list?
/whisper Bob the report is in common/report.md
```

See [Messages between agents](../agents/messaging.md#ask-and-whisper-from-a-chat).

## Status and information

| Command | What it does |
|---|---|
| `/status` | Shows the version, uptime, memory use, the agent, its model and provider, the channel, the conversation size and how full the context window is, the archive, the agent's channels, and any paused models. |
| `/show channel` | Shows the channel this chat is on, for example "Current Channel: telegram-main". |
| `/list channels` | Lists the enabled channels. |
| `/check channel <name>` | Checks that a channel exists and is enabled. Reply: "Channel 'slack' is available and enabled", or the reason it is not. |
| `/quota` | Lists the agent's **Integration Tokens** with their rate limit and use this minute, and marks the blocked ones. `/quota list` is the same. |
| `/quota reset [<name>]` | Lifts the block on one token, or on all of the agent's tokens. |
| `/version` | Shows the name, version, tagline and copyright. |
| `/start` | Replies "Hello! I am Alice." Telegram sends it when someone first opens the bot. |
| `/help` | Lists every command with a one-line description. |

## In a human agent's chat

In the chat of a [human agent](../agents/human-agents.md), `/cancel` ends the question waiting for the person. Other commands work as usual.

## On a device

A device, such as a Rabbit R1, handles two commands itself. Every other command is passed on as above.

| Command | What it does |
|---|---|
| `/agent` | Lists the assistants and marks the current one. |
| `/agent <name>` | Switches this device to that agent, by name or id. Reply: "Switched to Bob. New messages will go to this assistant." |
| `/agent default` | Goes back to the default agent. `/agent reset` is the same. |
| `/help` | Lists the device commands. On a device it replaces the normal `/help`. |

See [Devices](../channels/devices.md#switching-agents-from-the-device).

## Related pages

- [Command line](./cli.md), for the `claw` commands run on the server.
- [WebUI chat](../channels/webui-chat.md)
