# A tour of the WebUI

This page walks through the WebUI sidebar from top to bottom: what each page is for, its main controls, and where the manual covers it in depth.

The groups in the sidebar (**Agents**, **Models**, **Channels**, **Services**) are closed when you sign in; click a group to open it. Most pages save as you change them.

## Chat

Talk to an agent from the browser. Type in the box and press Enter. **New Chat** clears the window but not the agent's conversation. The chat goes to the default agent unless a binding sends it elsewhere.

See [WebUI chat](../channels/webui-chat.md). The commands you can type are in [Chat commands](../reference/chat-commands.md).

## Agents

### Agents

Create, edit, turn off and delete agents. **New Agent** adds one. Each agent's card holds its name, models, tools and skills, tool suites, folder access and **Integration Tokens**.

See [Creating and editing agents](../agents/creating.md) and [Tools and skills](../agents/tools-and-skills.md).

### Bindings

Decide which agent answers which channel, chat or account, and set each agent's default channel. **Add Binding** adds one.

See [Routing and bindings](../channels/routing.md).

### Memory

View and correct each agent's long-term memory. Pick an agent's memory store to see its domains and memories.

- **Add domain** and **Add memory** add entries by hand.
- Each memory can be retired, restored or deleted; several selected memories can change type together.
- **Export** downloads the store; **Import** loads a file, either **Merge** (add what is missing) or **Replace** (erase everything first).

See [Conversation and memory](../agents/memory.md#long-term-memory-cognitive-memory).

### Skills

The shared skills every agent can be granted. **Import Skill** adds one, **View** shows its `SKILL.md`, **Delete** removes it. Which agent gets which skill is set on the Agents page.

See [Tools and skills](../agents/tools-and-skills.md#grant-skills).

### Tools

The install-wide switch for each built-in tool: **Enabled**, **Disabled** or **Blocked** (a prerequisite is missing). A Disabled tool is offered to no agent. Which agent gets which tool is set on the Agents page.

See [Tools and skills](../agents/tools-and-skills.md) and [Web search and fetch](../agents/web-search.md).

## Models

### Providers

Where models are reached: each provider's protocol, base URL and API key. **Add Provider** adds one. CLI providers carry **Allow CLI to bypass restrictions**.

See [Providers and models](../providers/index.md).

### Models

The models agents can use, each tied to a provider. **Add Model** adds one.

See [Providers, models and fallback](../providers/concepts.md).

## Channels

One page per chat channel, in alphabetical order: **Discord**, **LINE**, **Matrix**, **SecMsg**, **Slack**, **Telegram** and **Web** (the WebUI chat). Each page holds the channel's credentials and **Allow From**, the list of people who may write.

See [Channels](../channels/index.md), which links to the page for each channel.

## Services

### Devices

Pair a Rabbit R1 or a voice app, approve pending devices, choose which agent each device talks to, and remove devices. The cards are **Pair a device**, **Pending approvals** and **Paired devices**.

See [Devices](../channels/devices.md).

### Speech

Transcribe voice messages before they reach the agent. **Add backend** adds a transcription service (any OpenAI-compatible Whisper endpoint); the first enabled one is used and marked **Active**. With none, voice messages reach the agent untranscribed.

### Network

Who can reach the WebUI, and how: the **Addresses** card, the **HTTP** and **HTTPS** listeners, the **Certificate**, **Allowed networks**, the **Device listener** and the **MCP host**. Listener changes need a restart, which the page offers.

See [Network access](../admin/network.md) and [HTTPS and certificates](../admin/https.md).

### System

Install-wide settings in sections: **Agent**, **Agent defaults**, **Context management**, **Runtime**, **Backup**, **Forums** and **Devices**. **Raw config** opens the whole `config.json` for editing.

See [Configuration file](../reference/configuration.md), [Backup and restore](../admin/backup.md) and [Forums](../forums/index.md).

### MCP

Two pages:

- **Config**: ClawEh's own MCP server, which CLI providers and other MCP clients use to call ClawEh's tools.
- **Servers**: the external MCP servers ClawEh connects to. **Add server** adds one; **Apply now** saves and reconnects the servers at once.

See [MCP](../mcp.md).

### Logs

The latest lines of the ClawEh log. The **Log** selector switches to **Alerts**, **Show** sets how many lines, and **Refresh** fetches new ones.

See [Logs](../admin/logs.md) and [Alerts](../admin/alerts.md).

### Audit

Every tool call, configuration save, sign-in and restart, newest first. Filter by **Kind** and **Agent**; **Load more** goes further back.

See [Audit log](../admin/audit.md).

## Check Up

The settings that most often need attention, as a table with **Action**, **Item** and **Status**. **Full report** downloads a PDF of everything the install can do.

See [Check Up](../admin/checkup.md).

## Status

At the bottom of the sidebar. Shows that ClawEh is running and how: uptime, memory use, the number of assistants, channels, models and providers, the version and build, and whether the MCP host and CLI providers are in use. It refreshes every few seconds.

## Manual

Opens this manual in a new tab.

## Sign out

The version is shown at the bottom of the sidebar, next to **Sign out**. See [Signing in](../admin/login.md).
