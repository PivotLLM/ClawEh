# Creating and editing agents

Agents are created, changed, turned off and deleted on the **Agents** page.

## Create an agent

1. Open **Agents** and click **Add Agent**.
2. Type the **Agent ID**, for example `alice`. The field marks an id it cannot accept as you type, with the reason.
3. Optionally type a display name, for example `Alice`. People and logs see the name; without one they see the id.
4. Under **Models (tried in order)**, add one or more models. Leave it empty to use the install's **Default models** (see [The default agent and default models](#the-default-agent-and-default-models)).
5. Optionally pick **Skills** and open **Internal tools: No tools** to grant tools. Both can be done later; see [Tools and skills](./tools-and-skills.md).
6. Click **Add**.

The new agent appears in the list on the left and is selected. Every change you make on its card afterwards is saved automatically; the card shows "Saving…", then "Saved ✓" (or "Save failed").

## Agent ids

The id is how ClawEh, bindings, other agents and scheduled jobs refer to the agent. It cannot be changed on the Agents page once the agent exists.

- 1 to 64 characters: letters, digits, `-` and `_`.
- It must start with a letter or a digit.
- Ids ignore case: `Bob`, `bob` and `BOB` are the same agent. The agent's folder, conversation, jobs and tokens use the lower-case form.
- No two agents may share an id. Adding `Bob` when `bob` exists is refused: `Agent ids "bob" and "Bob" name the same agent; give each agent its own id.`

An invalid id is refused with a suggestion, for example `Agent id "Alice.Smith" may use only letters, digits, - and _; use "Alice-Smith".` The same rules apply when `config.json` is edited by hand: a file with an invalid or duplicate id does not load.

## Models

**Models (tried in order)** is the agent's model chain. The first model answers; if it fails or is unavailable, the next one is tried. The models themselves are set up on the **Models** and **Providers** pages (see [Providers and models](../providers/index.md)).

If a model in the chain runs on a [command-line provider](../providers/cli-providers.md#allow-cli-to-bypass-restrictions) that is not allowed to bypass its restrictions, the card says `<provider> is not allowed to bypass its restrictions.` with an **Allow it** link to the Providers page.

Other model settings on the card:

- **Memory models (optional)**: models tried first when this agent's memory is consolidated or its conversation is summarized, ahead of the install's memory models. Use them when the default models refuse this agent's content.
- **Temperature**: 0 to 2; blank uses **Default temperature** on the System page, else the model's own default.

## Turn an agent off or delete it

- The switch at the top right of the card turns the agent off and on. A disabled agent does not run, but its settings, files and memory are kept. In the list on the left, a running agent has a green dot.
- The trash icon deletes the agent at once, without asking. It removes the agent from the configuration only: its folder, conversation and memory stay on disk.

## The default agent and default models

The default agent handles messages that do not match any binding. Set it on the **System** page, in **Agent defaults**, under **Default agent**. Without a choice, the first agent in the list is used. An agent that represents a person (a [human agent](./human-agents.md)) is never the default agent.

**Default models** in the same section is the model chain used by every agent whose own model list is empty.

## Default channel

**Default Channel** on the card lists the channels bound to the agent (bindings are made on the **Bindings** page; see [Routing and bindings](../channels/routing.md)) and lets you pick one. That chat is where the agent's [scheduled jobs](./scheduling.md) and other notices are delivered.

- For a binding to one specific chat (a Slack channel, say), that chat is used.
- For a channel with no specific chat (a Telegram bot), type the chat id to deliver to first. This affects delivery only, not who may write to the bot.
- The WebUI chat cannot be a default channel.

## Workspace

Each agent has a folder, its workspace. By default it is `agents/<id>` in the data directory (`~/.claw/agents/alice`). To move every agent's folder to another disk, set **Agent base directory** on the System page. To place one agent's folder somewhere specific, set `workspace` on that agent in `config.json`:

```json
{ "id": "alice", "workspace": "/srv/agents/alice" }
```

The workspace holds the agent's instructions, its files, its conversation and its memory. See [Files and mounts](./files-and-mounts.md).

### Instruction files

ClawEh creates these files in a new workspace. Edit them in a text editor to shape the agent; your edits are never overwritten.

| File | What it is for |
|---|---|
| `AGENTS.md` | Operating rules. Read into every prompt. |
| `SOUL.md` | Values and communication style. Read into every prompt. |
| `IDENTITY.md` | Name, role and style. Read into every prompt. |
| `USER.md` | Durable facts about the person the agent works for. Read into every prompt. |
| `MEMORY.md` | Notes you write by hand that should persist. Read into every prompt. |
| `COMPRESSION.md` | Extra instructions for summarizing this agent's conversation. Comment-only by default. |
| `COGMEM.md` | Extra instructions for this agent's memory consolidation. |

You may delete `MEMORY.md` and `COMPRESSION.md`; they are not recreated. A file named `channel-<channel>.md` (for example `channel-telegram.md`) adds instructions only for messages on that channel.
