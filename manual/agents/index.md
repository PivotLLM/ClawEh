# Agents

An agent is one assistant: a name, the models it runs on, the tools and skills it may use, a folder of its own and one ongoing conversation.

You can run several agents side by side, for example Alice for daily planning and Bob for research. Each has its own settings, its own files and its own memory. Chats, devices and scheduled jobs reach an agent through its [bindings](../channels/routing.md); the agent that answers everything else is the default agent.

## What an agent starts with

A new agent can talk, but it can do nothing else until you allow it:

- **No tools.** It cannot read files, search the web or schedule anything until you grant those tools.
- **No skills.** It sees no skills until you pick them.
- **Cognitive memory on.** It remembers what matters across conversations unless you turn that off.

An agent with no tools at all is told so in its instructions, and asks the person to enable tools in the web interface when a request needs them.

## Pages in this chapter

| Page | What it covers |
|---|---|
| [Creating and editing agents](./creating.md) | The Agents page, ids and names, models, turning an agent off, the default agent, its folder |
| [Tools and skills](./tools-and-skills.md) | Granting tools and skills, shell commands, MCP and Fusion access, Maestro, forums, memory |
| [Web search and fetch](./web-search.md) | The search providers, how to set one up, and fetching pages |
| [Conversation and memory](./memory.md) | The one conversation per agent, how it is kept short, long-term memory, `/clear` and `/compact` |
| [Files and mounts](./files-and-mounts.md) | The agent's folders, what it may read and write, external folders, the common folder |
| [Messages between agents](./messaging.md) | `agent_message`, `/ask` and `/whisper` |
| [Human agents](./human-agents.md) | An agent that is a person, answering by hand |
| [Sub-agents](./subagents.md) | `agent_spawn`, the depth limit, temporary agents and clones |
| [Scheduled jobs](./scheduling.md) | Reminders, recurring jobs, watches and listeners |
