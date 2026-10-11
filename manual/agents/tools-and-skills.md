# Tools and skills

An agent has only the tools and skills you grant it; this page shows how to grant them on the **Agents** page.

## Nothing is granted by default

- An agent with no tools list, or an empty one, has **no tools**. The card shows **Internal tools: No tools**, highlighted.
- An agent with no skills list, or an empty one, has **no skills**. The card shows "No skills".
- New agents start with neither.

An agent with no tools at all (no internal tools, no MCP or Fusion access, no tool suite) is told in its instructions that it has no tools, and asks the person to enable them in the web interface when a request needs one. **Check Up** lists every enabled agent with an empty tools list as **No tools (Alice)**.

A tool an agent is not granted is not offered to its model at all, so it cannot be called by any route: from a chat, from another agent, from a scheduled job or over MCP.

## Grant tools

The **Tools** box on the agent's card has these parts:

1. **MCP access**: one checkbox per configured MCP server. A checked server grants all of its tools. Nothing checked means no MCP tools.
2. **Fusion services**: one checkbox per Fusion service. A checked service grants its tools when the agent's **Fusion (REST-API tools)** switch is on. Nothing checked means no Fusion tools, even with the switch on.
3. **Internal tools**: the built-in tools.
   - **Default tools** writes the standard set into the list: everyday tools such as the file tools, web search and fetch, sending messages, the time and scheduling. You can then tick or untick single tools.
   - **Clear** empties the list.
   - Each checkbox grants one tool. Hover a tool for its description.
4. **Allow shell commands**: see [Shell commands](#shell-commands).
5. **Denied tools**: see [Deny a tool](#deny-a-tool).

Most tools also have an install-wide switch on the **Tools** page (Enabled, Disabled or Blocked). A tool that is Disabled there is offered to no agent, whatever the agent's list says. Some tools are Disabled there until you enable them, for example `agent_spawn` and `agent_message`.

### Grant tools in config.json

The same lists can be written by hand. Entries are tool names; a trailing `*` matches every tool starting with that text, and `"*"` alone matches every internal tool except `shell_exec`. MCP and Fusion tools are granted only through `mcp_tools`.

```json
{
  "id": "alice",
  "tools": ["file_*", "web_search", "web_fetch", "cron_schedule"],
  "mcp_tools": ["github"],
  "deny_tools": ["file_delete"]
}
```

`mcp_tools` names MCP servers or Fusion services (or a group within one, such as `microsoft365_calendar`): an entry grants every tool whose name starts with it.

## Shell commands

**Allow shell commands** lets the agent run commands on this machine with the `shell_exec` tool. It is off by default.

- It is the only way to grant `shell_exec`: `"*"` and `file_*`-style entries never include it, and it has no switch on the Tools page.
- It applies everywhere the agent acts: every chat, its sub-agents and clones, questions from other agents, forum turns and its MCP tokens.
- Shell commands are **not** confined to the agent's folder, even when file tools are.
- If `shell_exec` is also in **Denied tools**, the card shows "Ignored: blocked by Denied tools." and the agent cannot run commands.
- An agent allowed to message or spawn another agent that has shell commands can have that agent run them. **Check Up** lists both under **Shell access**.

In `config.json` the switch is `shell_exec` in the agent's `tools` list.

## Deny a tool

**Denied tools** lists tools the agent may never call, even when a list above grants them. Denial always wins. Use it to cut one tool out of a broad grant, for example `google_calendar_event_delete` under an MCP server that is otherwise allowed. Entries match the same way as the list the tool came from.

## Tool suites

Some tool sets are switched on and off as a whole, on the lower part of the card. Each switch grants the whole suite; **Denied tools** still removes single tools from it.

| Switch | Default | What it gives the agent |
|---|---|---|
| **Cognitive memory** | On | Long-term memory, recalled automatically, with the `cogmem_*` tools. See [Conversation and memory](./memory.md). |
| **Maestro** | Off | The Maestro task-orchestration tools (projects, playbooks, tasks). Its data is kept in the agent's `maestro/` folder. Tasks run as [sub-agents](./subagents.md). When on, it shows its own concurrency and rate-limit settings. |
| **Fusion (REST-API tools)** | Off | The Fusion services ticked under **Fusion services**, and nothing else. |
| **Allow forum** | Off | The `forum_*` tools, to hold structured discussions with the agents it may message (see [Messages between agents](./messaging.md#who-may-message-whom)). |
| **Share common directory** | On | Access to the shared common folder through the `common_*` tools, which must also be granted. See [Files and mounts](./files-and-mounts.md#the-common-folder). |
| **Global cron** | Off | Lets the agent create and manage scheduled jobs for other agents. See [Scheduled jobs](./scheduling.md). |

**Check Up** marks an agent with Fusion on and no service ticked: **Fusion on with no service (alice)**.

## Grant skills

Skills are instructions packaged as folders with a `SKILL.md`. Shared skills are managed on the **Skills** page; an agent can also have skills of its own in its `skills/` folder.

Under **Skills** on the agent's card:

- **All skills** grants every skill, including ones installed later. It is saved as `"skills": ["*"]`.
- Click a skill to grant or remove it. The first change after **All skills** writes out the installed skills one by one.
- **Clear** removes them all.

The line under the buttons reads "No skills", "All skills" or "N skills selected".

## What other agents get

- A [sub-agent or clone](./subagents.md) of Alice has exactly Alice's tools and skills, including shell commands if Alice has them.
- When Alice asks Bob something, Bob answers with **Bob's** tools, not Alice's.
- A temporary agent created from scratch (not a clone) has no tools.
- A [human agent](./human-agents.md) has no tools.
