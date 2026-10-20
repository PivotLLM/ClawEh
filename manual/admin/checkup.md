# Check Up and the full report

The **Check Up** page lists the settings that most often need attention, and its **Full report** button downloads a PDF of everything the install can do.

Both are built from the configuration ClawEh has loaded. Neither ever shows a password, key or token: credentials appear only as set or not set.

## Read the Check Up page

1. Click **Check Up** in the sidebar.
2. The first line names the product, version, build and platform. Quote it when asking for help.
3. The table has three columns: **Action**, **Item**, **Status**.
4. A row with a warning icon in **Action** (and a highlighted background) is one where action is recommended. Rows without it are for your information.

Fix the marked rows first. Then re-open **Check Up**; it is rebuilt each time the page loads.

## Rows and what to do

### Listeners

One row each for **WebUI/API HTTP**, **WebUI/API HTTPS**, **Device HTTP**, **Device HTTPS** and **MCP host (local tools)**. The status says "Enabled for localhost", "Enabled for network access" or "Disabled.", which certificate is used, and which networks are allowed.

| Marked when | What to do |
|---|---|
| **WebUI/API HTTP** is "Enabled for network access (unencrypted)" | On the **Network** page set **HTTP** → **Scope** to **Localhost only** and use HTTPS from other machines. See [HTTPS and certificates](./https.md). |
| **WebUI/API HTTPS** is "Disabled, while HTTP is enabled for network access." | Turn HTTPS on (**Listen on** → **All interfaces**), and HTTP back to localhost. |
| **Device HTTP** is "Enabled for network access (unencrypted)" | Set the device listener's **Protocol** to **wss (HTTPS)**. See [Network access](./network.md#the-device-listener). |

A self-signed certificate is stated, never marked.

Behind a proxy, plain HTTP on these listeners is not marked, because the proxy does the encryption. A **Proxy** row names it: each **Trusted proxies** entry, and each https or wss external address that ClawEh's own HTTPS does not serve.

### Other rows

| Item | Marked when | What to do |
|---|---|---|
| **Reverse proxy** | Requests seem to arrive through a proxy on this machine that is not in **Trusted proxies**, so the allowed networks and the lockout don't apply. It shows once a request from localhost carried forwarding headers since ClawEh started. | Click the item to open **Trusted proxies** on the **Network** page and add the proxy's address, usually `127.0.0.1`. See [Remote access](./remote-access.md#what-to-set-in-claweh). |
| **Encrypted Matrix rooms** | The Matrix channel has seen encrypted rooms since ClawEh started; it can't read them. | Use a room with encryption off. Click the item to open [Matrix encryption](../channels/matrix.md#encryption). |
| **TLS certificate expiry** | Your own certificate expires within 14 days, or has expired. | Renew the certificate files. See [HTTPS and certificates](./https.md#renewals). |
| **Data directory permissions** | Files under the data directory are more open than the configured modes (`700` folders and `600` secrets unless `security.permissions` sets others). Unmarked, it lists the modes in force. | Restart ClawEh, which tightens them, or `chmod` each to the mode the row names. See [Logs](./logs.md#startup-permission-checks). |
| **WebUI chat token** | The WebUI channel is on without a token. | Set a token for the WebUI channel (`channels.webui.token`). It is for non-browser clients only. |
| **Device auto-approve** | New devices are paired without approval. | Turn off **Auto-approve pairings** on the **Network** page. See [Devices](../channels/devices.md). |
| **File confinement** | Agents may read (or read and write) outside their workspace. | On the **System** page, under **Agent**, turn on **Restrict to workspace**, unless you mean it. See [Files and mounts](../agents/files-and-mounts.md#install-wide-file-settings). |
| **Shell access** | Some agent may run shell commands and the deny patterns are off. | Check that each listed agent should have **Allow shell commands**. See [Tools and skills](../agents/tools-and-skills.md#shell-commands). |
| **Mount (name)** | A mount uses a reserved folder name and is ignored. | Rename the mount on the **Agents** page. See [Files and mounts](../agents/files-and-mounts.md#reserved-names). |
| **Fusion on with no service (agent)** | Fusion is on for an agent but no service is ticked, so it has no Fusion tools. | Tick its services under **Fusion services** on the **Agents** page. See [MCPFusion](../mcpfusion.md#give-an-agent-access). |
| **Person's chat (name)** | A person's chat is on a channel that is not set up. | Set up that channel, or bind the person to a channel that is. See [Human agents](../agents/human-agents.md). |
| **Channels accepting any sender** | A messaging channel allows anyone (`*`) to message it. | Limit its allowed senders (**Allow From**) on that channel's page. See [Channels](../channels/index.md). |
| **Message content in logs** | Message text or full prompts are written to the logs. | Turn off the logging options named in the row unless you need them. See [Logs](./logs.md#logging-options). |
| **Audit log** | The audit log file is missing. | Restart ClawEh; it creates the file at start. See [Audit log](./audit.md). |

These rows are information only:

- **Proxy**: the proxies ClawEh is reached through (see [Listeners](#listeners)).
- **Install-wide shell setting**: an old install-wide shell setting is in the config and has no effect. Shell commands are allowed per agent.
- **File confinement vs shell (agent)**: the agent's file tools are confined, but it can run shell commands, which are not.
- **Allow CLI to bypass restrictions (provider)** and **CLI bypass not allowed (provider)**: whether each CLI provider may skip its own permission prompts. See [CLI providers](../providers/cli-providers.md#allow-cli-to-bypass-restrictions).
- **No tools (agent)**: the agent has no tools. See [Tools and skills](../agents/tools-and-skills.md).
- **MCP servers running local programs**: [MCP servers](../mcp.md#external-mcp-servers) started as programs on this machine.
- **Sub-agent spawning**: how deep [sub-agents](../agents/subagents.md) may nest.

## Download the full report

1. On the **Check Up** page, click **Full report**.
2. The PDF opens in a new tab. Save or print it from there.

The PDF has these sections, in order:

| Section | What it holds |
|---|---|
| Identity | Product, version, build, host. |
| Security assessment | The same table as the Check Up page. A `*` in the first column means action is recommended. |
| Summary | The user ClawEh runs as, and what it can reach: files, shell, outbound and inbound network, messaging, devices, external programs, skills. |
| Network | Every listener, where it listens, and its allowed networks. |
| Providers and models | API and CLI providers, enabled models, and which model does what. |
| Credentials and tokens | Each credential as set or not set. |
| Channels | Enabled channels, who may message them, and which agents answer. |
| Agents | Per agent: tools, folder access, MCP access. |
| External services | MCP servers and skills. |
| Devices | Paired devices and pending pairings. |
| Data | Where the config, logs, conversations, memory and other data are stored. |
| Scheduled activity | Scheduled jobs, task orchestration, and background tasks. |

A part that cannot be read shows as "unavailable" with the reason; the rest of the report is still produced.
