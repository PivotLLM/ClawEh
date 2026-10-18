# Character cards

A character card gives an agent a character to play: a name, a description, a personality, the situation it is in and examples of how it speaks. ClawEh keeps cards in one library and renders a card into the agent's instructions the same way every time, so the same card gives the same character on every agent that uses it.

Cards use the Character Card V3 format shared by SillyTavern, RisuAI and other tools, so a card made elsewhere can be imported and a card made here can be exported.

## Where cards live

The library is the `cards/` folder of the data directory, one `<id>.json` file per card. It is included in the [nightly backup](../admin/backup.md). No agent can read it through its file tools; agents reach it only with the `ccv3` tool (below), and only when you allow that.

A card's id is like an agent id: 1 to 64 letters, digits, `-` and `_`, starting with a letter or digit. Case does not matter.

## The Cards page

**Agents** group → **Character cards** lists every card: its name, id, tags, when it last changed and which agents use it.

- **Import**: pick a `.json` or `.png` card file, give it an id, and import it. V2 and V3 cards are accepted; a V1 card must be converted first. Tick **Replace existing** to overwrite a card with the same id. A card's author instructions (`system_prompt`) are used as written, so import only cards you trust on agents that have tools.
- **Download** saves the card as JSON.
- **Delete** removes it. A card an agent uses cannot be deleted; detach it on the Agents page first.

Imports and deletions from this page are recorded in the [audit log](../admin/audit.md).

## Give an agent a character

On the **Agents** page, pick the card under **Character card**. The agent reads it on its next message; an edit to the card reaches the agent the same way, without a restart.

The card is placed after the agent's standing rules and before its `SOUL.md`, `USER.md` and `IDENTITY.md`, which still apply. To run a pure character, leave those files empty; to give an assistant a personality on top of its job, keep them.

A card that no longer exists in the library is shown as **(missing)** on the agent's card and in the full report, and the agent runs without it. An agent that [represents a person](./human-agents.md) cannot have a card.

Sub-agents and clones inherit the agent's card. A sub-agent can be started as a different character with `agent_spawn`'s `character_card` argument (see [Sub-agents](./subagents.md)), and a [forum](../forums/index.md) participant can be given one with its `character_card` field.

## What the agent sees

The card becomes one section of the agent's instructions:

```
# Character

<the card's system_prompt, as written>

You are <nickname, or name>.

## Description
## Personality
## Scenario
## How <name> speaks      (the example dialogue)
## Background             (lorebook entries marked constant)
```

<div v-pre>

Empty fields are left out. `{{char}}` becomes the character's name, `{{user}}` becomes "the user" and `{{// notes}}` are removed.

</div>

Greetings, `post_history_instructions`, keyword-triggered lorebook entries, creator notes, tags and images are kept in the card and exported with it, but never shown to the agent.

## Let an agent manage cards

Turn on **Allow character card tools** on the agent's card. The agent gets one tool, `ccv3`, with the actions `list`, `get`, `create`, `update`, `rename`, `copy` and `delete`. Every write is checked against the card format, so a card an agent writes is always valid. A card an agent uses cannot be renamed or deleted by the tool; detach it first.

Use this for an agent that writes or refines characters, for example from a forum's results. Agents without the switch cannot see the library at all.

## In config.json

```json
{
  "agents": {
    "list": [
      { "id": "alice", "character_card": "sherlock", "ccv3": true },
      { "id": "bob", "character_card": "watson" }
    ]
  }
}
```

`character_card` names a card in the library; `ccv3` grants the tool (off by default).
