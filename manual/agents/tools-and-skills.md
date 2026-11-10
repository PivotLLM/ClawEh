# Tools and skills

An agent has only the tools and skills you grant it; this page shows how to grant them on the **Agents** page.

## Nothing is granted by default

- An agent with no tools list, or an empty one, has **no tools**. The card shows **Internal tools: No tools**, highlighted.
- An agent with no skills list, or an empty one, has **no skills**. The card shows "No skills".
- New agents start with neither.

An agent with no tools at all (no internal tools, no MCP or Fusion access, no tool suite) is told in its instructions that it has no tools, and asks the person to enable them in the web interface when a request needs one. [Check Up](../admin/checkup.md) lists every enabled agent with an empty tools list as **No tools (Alice)**.

A tool an agent is not granted is not offered to its model at all, so it cannot be called by any route: from a chat, from another agent, from a scheduled job or over MCP.

## Grant tools

The **Tools** box on the agent's card has these parts:

1. **MCP access**: one checkbox per configured [MCP server](../mcp.md). A checked server grants all of its tools. Nothing checked means no MCP tools.
2. **Fusion services**: one checkbox per [Fusion service](../mcpfusion.md). A checked service grants its tools when the agent's **Fusion (REST-API tools)** switch is on. Nothing checked means no Fusion tools, even with the switch on.
3. **Internal tools**: the built-in tools.
   - **Default tools** writes the standard set into the list: everyday tools such as the file tools, web search and fetch, sending messages, the time and scheduling. It leaves out tools few agents need, such as `agent_spawn`, `agent_message`, `session_clear` and the byte-level file tools. You can then tick or untick single tools.
   - **Clear** empties the list.
   - Each checkbox grants one tool. Hover a tool for its description.
4. **May message**: the other agents this agent may message with `agent_message`, start as sub-agents or invite to forums. See [Messages between agents](./messaging.md#who-may-message-whom).
5. **Allow coding tools (includes shell commands, full account access)**: see [Coding tools](#coding-tools).
6. **Allow shell commands (full account access)**: see [Shell commands](#shell-commands).
7. **Denied tools**: see [Deny a tool](#deny-a-tool).

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

## Coding tools

**Allow coding tools (includes shell commands, full account access)** grants the tools an agent needs to work on code, together:

| Tool | Use it to |
|---|---|
| `file_list` | See what is in a folder |
| `file_glob` | Find files by name pattern, such as `**/*.go` |
| `file_search_lines` | Find text in files |
| `file_read_lines` | Read the lines found |
| `file_edit`, `file_edit_lines`, `file_insert_lines`, `file_delete_lines` | Change text |
| `file_write` | Create a file or replace a whole file |
| `shell_exec` | Run git, builds, tests and other programs (see [Shell commands](#shell-commands)) |

- Ticking the box adds every one the agent lacks to its tools list; unticking it removes them all. Nothing else changes, and there is no separate setting: the box only edits the list.
- The box is ticked when all ten are granted and shows a mixed state when only some are. The single tool boxes and **Allow shell commands** stay and follow it.
- When **Denied tools** blocks some of them, the box says which, for example "Ignored for file_edit: blocked by Denied tools."
- `file_glob` is not in **Default tools**. It returns matching paths newest first (at most 200) and skips folders whose name starts with a dot. A pattern may be up to 512 characters with up to 10 `{…}` groups.
- `file_search_lines` takes `include` (a file pattern such as `*.go`), `context` (up to 10 lines around each match; a result then shows at most 2,000 lines) and `output` (`lines`, `files` or `count`), and skips what the `.gitignore` files in the searched folder ignore (a `.gitignore` that is a link, not a regular file, or over 64 KiB is not read).
- Both reach only what the other file tools reach: see [Files and allowed folders](./allowed-folders.md).

## Shell commands

**Allow shell commands (full account access)** lets the agent run commands on this machine with the `shell_exec` tool. It is off by default.

- It is the only way to grant `shell_exec`: `"*"` and `file_*`-style entries never include it.
- It applies everywhere the agent acts: every chat, its sub-agents and clones, questions from other agents, forum turns and its MCP tokens.
- Shell commands run with the full access of the account ClawEh runs as. They are **not** confined to the agent's folder or its allowed folders, even when file tools are, and **Restrict to workspace** does not apply to them. The deny patterns still block dangerous commands.
- `working_dir` may be any existing folder; the default is the agent's workspace.
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
| **Maestro** | Off | The [Maestro](../maestro.md) task-orchestration tools (projects, playbooks, tasks). Its data is kept in the agent's `maestro/` folder. Tasks run as [sub-agents](./subagents.md). When on, it shows its own concurrency and rate-limit settings. |
| **Allow task models** | Off | One box per kind (**Text**, **Images**, **Speech (TTS)**, **Transcription (STT)**): which [task models](../providers/task-models.md#agents-using-task-models) it may call through `agent_spawn`. |
| **Allow classifiers** | Off | The `classifier_*` tools, to sort the items its tools can list ([Classifiers](./classifiers.md)). When on, it shows the **Classifier model** selector. |
| **Fusion (REST-API tools)** | Off | The [Fusion](../mcpfusion.md) services ticked under **Fusion services**, and nothing else. |
| **Allow forum** | Off | The `forum_*` tools, to hold structured discussions ([Forums](../forums/index.md)) with the agents it may message (see [Messages between agents](./messaging.md#who-may-message-whom)). |
| **Allow character card tools** | Off | The `ccv3` tool, to create, edit and delete [character cards](./character-cards.md) in the library. |
| **Share common directory** | On | Access to the shared common folder through the `common_*` tools, which must also be granted. See [Files and allowed folders](./allowed-folders.md#the-common-folder). |
| **Global cron** | Off | Lets the agent create and manage scheduled jobs for other agents. See [Scheduled jobs](./scheduling.md). |

**Check Up** marks an agent with Fusion on and no service ticked: **Fusion on with no service (alice)**.

## Grant skills

Skills are instructions packaged as folders with a `SKILL.md`. Shared skills are managed on the **Skills** page (or with `claw skills`; see [Command line](../reference/cli.md#skills)); an agent can also have skills of its own in its `skills/` folder.

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
