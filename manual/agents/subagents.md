# Sub-agents

A sub-agent is a short-lived copy of an agent that works on one task and reports back, so the main conversation stays uncluttered.

## What a sub-agent is

When Alice spawns a sub-agent, ClawEh makes a temporary **clone** of Alice. The clone has:

- Alice's folder, instruction files, tools and skills, including shell commands if Alice has them;
- Alice's **sub-agent models**, or her own models when none are set (see [Sub-agent models](#sub-agent-models));
- a fresh conversation that starts with the task, without Alice's chat history;
- a copy of Alice's long-term memory. What it writes to memory stays in the copy and is thrown away; Alice's memory is never changed by it.

Nothing the clone says appears in Alice's conversation. Only its result comes back. The clone is deleted once its result is delivered.

Sub-agents are useful for self-contained work (research, a long file review) or to run a heavier model on one demanding step while the agent itself stays on a lighter one.

## Allow an agent to spawn

1. Tick `agent_spawn` under **Internal tools** on the agent's card. Tick `agent_status` and `agent_list` too if the agent should check on its background tasks.
2. To let Alice spawn a copy of **another** agent, tick that agent under **May message** on her card (`subagents.allow_agents`; see [Messages between agents](./messaging.md#who-may-message-whom)). An agent may always spawn copies of itself once it has the tool.

## How the agent uses it

With the `kind` argument, `agent_spawn` calls a task model instead of a sub-agent: see [Task models](../providers/task-models.md#agents-using-task-models).

The `agent_spawn` tool takes:

| Argument | Meaning |
|---|---|
| `task` | The instructions for the sub-agent (required). |
| `mode` | `callback` (default): run in the background and notify the agent when done. `wait`: run to the end and return the result directly. |
| `name` | A short label for the task; required in `callback` mode. |
| `agent_id` | Another agent to copy instead of itself; needs `allow_agents`. |
| `model` | One of the copied agent's sub-agent models to run instead of the first one. |
| `character_card` | A [character card](./character-cards.md) the sub-agent runs as, instead of the copied agent's own. The card must exist. |

In `callback` mode the result is written to the agent's `tasks/` folder and the agent is told where; `agent_status` and `agent_list` show progress.

[Maestro](../maestro.md) tasks also run as sub-agents of the agent that dispatches them.

## Sub-agent models

**Sub-Agent models (tried in order)** on the agent's card (`subagents.models`) is the model chain its sub-agents run on. A sub-agent starts on the first worker model and falls back along the list; a `model` given to `agent_spawn` must be one of them, and a sub-agent of another agent (`agent_id`) uses that agent's worker models. Maestro tasks follow the same rule.

Leave the list empty and sub-agents use the agent's own **Models** list, as the card says.

Set sub-agent models when the agent itself should answer on one kind of model and delegate on another. A manager on a fast API model, for example, can hand its work to command-line models that run in the source tree with the development tools, which an API model confined to the agent's folder cannot reach. A spawn is refused, and the agent told, when the sub-agent model list names no enabled model.

## The depth limit

A sub-agent can spawn sub-agents of its own, up to a limit, so a runaway chain cannot happen.

- A normal turn runs at depth 0, the sub-agents it spawns at depth 1, theirs at depth 2, and so on.
- A spawn is refused once the spawning agent is already at the limit: "maximum sub-agent depth (3) reached; …".
- `agent_spawn`, Maestro dispatch and asks to other agents (see [Messages between agents](./messaging.md)) all count against the same limit.
- Scheduled jobs start at depth 0.

Set the limit on the **System** page, in **Agent**, as **Max sub-agent depth** (blank or 0 means the default, 3). It applies to agents on command-line models too.

## Temporary agents

Sub-agents are one kind of **temporary agent**: an agent created while ClawEh runs, not listed in the configuration. [Forums](../forums/index.md#who-can-take-part) create temporary agents too. In plain terms:

- **Clones** copy a configured agent, as described above.
- **Fresh** temporary agents start empty: their own empty folder, no instruction files, no skills and no tools.

Temporary agents never appear on the Agents page, on **Check Up** or in a device's agent list, and no chat routes to them. Their conversation and memory copy are kept in `internal/temp/` in the data directory, which backups leave out. A sub-agent left behind by a crash is removed at the next start, and a temporary agent idle for 24 hours is deleted. In the logs a clone is named after its agent, for example `alice (clone 1a2b3c4d)`; [audit](../admin/audit.md) records list it under `alice`.

A [human agent](./human-agents.md) is never cloned or spawned.
