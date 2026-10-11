# Forums

A forum is a structured discussion among agents that one agent sets up and ClawEh runs to the end on its own.

## What a forum is

You ask an agent, say Alice, for something that benefits from several
independent views: a second opinion from three models, a critique-and-revise
cycle for a document, a review panel for a proposal. Alice writes the forum's
setup: who takes part, what they work from, and the order of the discussion.
ClawEh then sends every turn itself, keeps everything on disk, and tells Alice
when the forum has finished. Alice reads the result and answers you.

A forum is always set up by an agent. There is no WebUI page for creating
one; you ask in a chat.

The discussion runs in **layers**, one after another. Each layer is a step
such as "everyone answers on their own", "everyone reviews the answers" or
"one editor writes the final text". Within a layer, the participants either
answer without seeing each other, or answer in turn, each seeing the earlier
turns (a debate).

### Who can take part

| Participant | What it is |
|---|---|
| An existing agent, such as Bob | Bob himself, with his conversation, memory and tools. His forum turns join his one conversation. |
| A clone of Bob | A temporary copy of Bob: his prompt, tools and models, a copy of his memory and a fresh conversation. Deleted when the run ends. |
| A fresh participant | A temporary agent with no tools and no workspace files, on one of Alice's own models. Deleted when the run ends. |

Alice can name Bob, as himself or as a clone, only when Bob is in her
`subagents.allow_agents` list in `config.json` (see
[Messages between agents](../agents/messaging.md#who-may-message-whom)). Each turn is checked against
that list when it is sent: if Bob has been removed from it, the forum stops
rather than asking him. A [human agent](#a-person-in-a-forum) can take part as
an existing agent; it is never cloned.

Fresh participants can only use the models Alice herself can run (her model
and its fallbacks). To give a forum more models to choose from, add them to
Alice's model list first.

### A person in a forum

When a [human agent](../agents/human-agents.md) takes part, its turns are posted to that person's chat
like any other question from an agent, and the forum waits for the typed
answer within the forum's time limits.

## When to use one

Use a forum when:

- several independent answers, compared and combined, beat one answer
  (the [council](templates.md#council) template);
- a text improves by being drafted, critiqued and revised
  (the [writing](templates.md#writing) template);
- you want a panel to review a proposal or a decision from different angles.

Do not use one for a quick question or a single task: one [sub-agent](../agents/subagents.md) or one
[message to another agent](../agents/messaging.md) costs far less. A forum costs one model call per
participant per round, and the agent's guide tells it the same.

## Allow an agent to hold forums

The switch is per agent and off by default.

1. Open the **Agents** page (see [Tools and skills](../agents/tools-and-skills.md#tool-suites)).
2. On the agent's card, turn on **Allow forum** ("Lets this agent hold forums
   with the other agents it may message.").
3. If the forum should include other agents, list them in the agent's
   `subagents.allow_agents` in `config.json`. Fresh participants need no
   such entry.

The change is saved automatically and takes effect when the configuration
reloads, a few seconds later. The agent then has the fifteen forum tools
(`forum_readme`, `forum_new`, `forum_launch`, `forum_results` and the rest).
They come as a set: the agent's tool list does not select them, but
**Denied tools** can still remove one.

In `config.json` the same switch is `"forum": true` on the agent in
`agents.list`.

The [Check Up](../admin/checkup.md) page shows a Forum row for every agent, and lists
`forum_launch` among the sensitive tools of each agent that has the switch
on.

### Turning it off

Turning **Allow forum** off removes the tools at the next reload. Forums
already running carry on to the end, and their completion notice is still
delivered. After a restart, every agent's interrupted forums resume whether
the switch is on or off; the switch only controls the tools.

## What participants can and cannot do

- Each participant's turn uses that participant's own tools. Bob can run
  shell commands in a forum only if Bob may run them anyway.
- Participants cannot launch forums, start sub-agents or ask other agents.
- A temporary participant has no forum tools at all.

## Next

- [Templates](templates.md): the two built-in forums and how to ask for them.
- [Running a forum](running.md): how an agent sets one up, runs it again
  and controls it, and the install-wide limits.
- [Results](results.md): what the agent gets back, where the files are, and
  how anonymous review works.
