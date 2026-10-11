# Glossary

This page explains the terms used in ClawEh and this manual.

**Admin account** — The one account that may sign in to the WebUI. Created by `claw install` or `claw admin`, stored in `credentials.json`. See [Signing in](../admin/login.md).

**Agent** — An assistant. It has an id, a display name, a list of models, the tools and skills it is granted, a workspace and a memory. See [Agents](../agents/index.md).

**Agent id** — The agent's permanent name in the configuration: 1 to 64 letters, digits, `-` and `_`, starting with a letter or digit. Case-insensitive: `Alice` and `alice` are the same agent.

**Allowed networks** — The networks from which other machines may reach the WebUI (`gateway.allowed_cidrs`). Empty means this machine only. See [Network access](../admin/network.md).

**Allowed senders** (`allow_from`) — The users who may message an agent through a channel. An empty list lets nobody in; `*` lets everyone in.

**Alert** — A notice about an outage or failure, such as a channel that cannot connect or a model whose key is rejected. Alerts are logged and can be sent to you. See [Alerts](../admin/alerts.md).

**Binding** — A rule that sends messages from a channel, chat or account to a particular agent. It also sets an agent's default channel. Set on the **Bindings** page.

**Channel** — A way to reach agents: the WebUI chat, Telegram, Slack, Discord, Signal, Matrix, LINE or the device listener. See [Channels](../channels/index.md).

**Check Up** — The WebUI page that lists the security-relevant settings of the install and marks those that need attention. See [Check Up and the full report](../admin/checkup.md).

**CLI provider** — A provider that runs a coding CLI installed on the same machine (Claude Code, Codex, Antigravity, Cursor) instead of calling an API.

**Clone** — A temporary copy of an agent that works on one task, for example a sub-agent. It shares the agent's workspace and tools and has its own conversation.

**Cognitive memory** (cogmem) — An agent's long-term memory. It is updated in the background from the conversation, organised into domains, and recalled into the prompt when relevant. On by default; switched per agent.

**Common directory** — A shared folder that agents with **Share common directory** on can use to exchange files.

**Conversation** — The message history of an agent. Each agent has exactly one, shared by every channel that reaches it.

**Cooldown** — A pause ClawEh puts on a model after it fails, so the agent uses its next model instead. Its length depends on the kind of failure.

**Data directory** — The folder where ClawEh keeps everything: `~/.claw`, or the folder named by `CLAW_HOME`.

**Default agent** — The agent that answers messages no binding sends elsewhere. One agent is marked default; without one, the first agent is used.

**Default channel** — Where an agent's own messages go when nobody asked, such as scheduled job output. Set under **Default Channel** on the agent's card.

**Default model** — The first model of `agents.defaults.models`, used by every agent whose own model list is empty. Set on the **Models** page, the **System** page, or with `claw model`.

**Default tools** — The button on the Agents page that writes the standard tool set into an agent's tool list.

**Device listener** — ClawEh's listener for paired hardware and voice clients, such as the Rabbit R1 and the Claw to Talk app. Port 18791 by default.

**Fallback** — Trying the next model in an agent's list when one fails.

**Forum** — A structured discussion among several agents that one agent launches and the others take part in. Allowed per agent with **Allow forum**. See [Forums](../forums/index.md).

**Fusion** — ClawEh's built-in connector for REST-API services such as Microsoft 365. Granted per agent and per service.

**Human agent** — An agent that stands for a person. Other agents can ask it questions; the person answers in their chat.

**Maestro** — A tool suite for planning and running multi-step work as projects, playbooks and task lists, with tasks handed to sub-agents. Switched per agent.

**MCP** — Model Context Protocol, a standard way to offer tools to a model. ClawEh connects to MCP servers to give agents their tools, and runs its own MCP server so CLI providers can call ClawEh's tools.

**Model** — A named language model reached through a provider. An agent tries its models in order.

**Mount** — An outside folder given to an agent under a name, read-only unless made writable. Set per agent on the Agents page.

**Provider** — Where models are reached: an API endpoint with its key, or a CLI. See [Providers and models](../providers/index.md).

**Service token** — A long-lived credential that lets an external MCP client use an agent's tools. Managed with `claw token`.

**Shell commands** — The `shell_exec` tool, which runs commands on the machine. Granted per agent with **Allow shell commands**; off by default.

**Skill** — A packaged set of instructions an agent can follow for a kind of task. An agent uses only the skills its list includes.

**Sub-agent** — A clone an agent starts to do a task separately and report back. Nesting is limited by `agents.defaults.max_subagent_depth` (default 3).

**Temporary agent** — An agent created while ClawEh runs, such as a clone or a forum participant. It is not in `config.json` and is removed when its work is done or after a day idle.

**Tools** — The actions an agent can take, such as reading files or searching the web. An agent has only the tools its configuration lists.

**WebUI** — The web interface for chatting with agents and configuring ClawEh. Served on port 18790 (HTTP) and 18443 (HTTPS) by default.

**Workspace** — An agent's folder, `agents/<id>/` in the data directory. It holds the agent's prompt files, its `files/` working area, its skills and its data.
