# Troubleshooting

This page lists common problems, the message ClawEh shows or logs for each, and the fix.

## Where to look first

- **Check Up** in the WebUI marks configuration that needs attention. See [Check Up and the full report](../admin/checkup.md).
- **Logs** in the WebUI shows the log and, through its source selector, the alerts. See [Logs](../admin/logs.md) and [Alerts](../admin/alerts.md).
- On the server, the data directory holds `logs/claw.log` (everything), `logs/error.log` (warnings and errors only) and `logs/alerts.log`.
- `claw status` shows whether ClawEh is running and where to reach it.

## The service does not start

ClawEh writes the reason for a failed start to `logs/error.log` and to the service log. To read the service log on Linux:

```bash
journalctl --user -u claw -n 50     # user service
sudo journalctl -u claw -n 50       # system service
```

Find the line that starts with `Error:` or contains `gateway exited with error`.

| Message | Cause | Fix |
|---|---|---|
| `perms: <path>/config.json is readable by other users (mode 0644); it holds credentials, so refusing to start. Fix with: chmod 600 <path>/config.json` | The configuration file can be read by other users. | Run the `chmod 600` command the message gives, then start the service. |
| `error loading config: Agent id "Alice.Smith" may use only letters, digits, - and _; use "Alice-Smith".` | An agent id breaks the id rule. | Change the id to the suggested form in `agents.list` and everywhere it is named: `bindings`, `agent_mentions`, other agents' `allow_agents`. |
| `error loading config: Agent ids "Bob" and "bob" name the same agent; give each agent its own id.` | Two ids differ only by case. Ids are case-insensitive. | Rename one of the agents. |
| `error loading config: Agent id "alice" is used twice; give each agent its own id.` | Two agents share an id. | Rename one of the agents. |
| `error loading config: unexpected EOF`, or another JSON error | `config.json` is not valid JSON, usually after a hand edit. | Fix the file, or restore the copy from the latest backup. |
| `error loading config: gateway.tls.mode "…": must be "all", "localhost" or "off"` | A listener setting has an invalid value. Other listener errors name their key the same way. | Correct the named key. |
| `secret reference "file:…": … is readable by group or other …; run: chmod 600 …` | A secret file named in `config.json` is not private. | Run the `chmod 600` command the message gives. |
| `startup aborted: another instance is already running (lock held on "…")` | ClawEh is already running on this data directory, often a copy started by hand. | Stop the other copy, or use the running one. |

If ClawEh starts but the WebUI shows "No admin account. On the server run: claw admin", run `claw admin`. See [Signing in](../admin/login.md).

::: tip
An edit to `config.json` made while ClawEh runs is checked straight away. If it does not load, ClawEh keeps the running configuration and raises the alert "Config file invalid". Fix the file then, before a restart fails on it.
:::

## The WebUI cannot be reached

| What you see | Cause | Fix |
|---|---|---|
| "Forbidden" from another machine | Your network is not in the allowed networks. | `claw network 192.168.1.0/24` (your network), or **Allowed networks** on the **Network** page. |
| Connection refused from another machine | The listener serves this machine only. | `claw network --https all`, then restart the service. |
| `421 Misdirected Request` | You used a host name ClawEh does not know. | Set **Hostname / external URL** on the **Network** page to the address you use. |
| "Too many failed sign-ins" | Login lockout after failed attempts. | Wait, or run `claw admin`. |

See [Network access](../admin/network.md) and [Signing in](../admin/login.md).

## A model is unavailable

When a model fails, ClawEh tries the next model in the agent's list and pauses the failing one for a while (a [cooldown](../providers/concepts.md#cooldowns)). The reply then starts with a line such as:

> ⚠️ Claude Sonnet 5.5 unavailable — rate limited (retry in 9m). Using OpenAI GPT 5.5.

If every model fails, the reply is "All models failed", with the reason for each model.

What to do:

1. Test the model from the server: `claw test model <name>`. `claw test models` tests all enabled models.
2. Check the alerts on the **Logs** page. "Model authentication or billing failure" means a wrong or revoked API key, exhausted credit, or a CLI that is logged out. Fix the key on the **Providers** page or log the CLI in again.
3. In a chat with the agent, `/cooldowns list` shows the paused models and `/cooldowns clear` ends the pauses after you fixed the cause.
4. Give the agent a second model on the **Agents** page so it can fall back.

### A CLI model says it declined to use tools

The reply reads, for example:

> Alice: The Claude CLI declined to use tools (file_read_lines). Tick *Allow CLI to bypass restrictions* for this CLI on the Providers page, or allow the tools in the CLI's own settings.

A CLI provider runs under its own permission settings unless **Allow CLI to bypass restrictions** is ticked for it on the **Providers** page (see [CLI providers](../providers/cli-providers.md#allow-cli-to-bypass-restrictions)). Tick it only if the CLI may run commands and change files anywhere the service user can.

### An agent's model disappeared

The alert "Agent references a missing model" means a model named in an agent's list no longer exists. ClawEh removes the reference and the agent uses its next model. Pick an existing model on the **Agents** page.

## An agent has no tools

Symptom: the agent says it has not been granted any tools and asks you to enable them in the web user interface. Or it cannot do something it should.

An agent has only the tools its configuration lists. A new agent, and an agent upgraded from before 0.6.0 without a tools list, has none.

1. Open **Agents** and select the agent.
2. Under **Tools**, click **Default tools**, then untick what the agent should not have.
3. Under **Skills**, click **All skills** or pick the skills it needs.

**Check Up** shows a row **No tools (…)** for each enabled agent without tools.

If one particular tool is missing:

| Tool | Where it is granted |
|---|---|
| Shell commands (`shell_exec`) | **Allow shell commands** on the agent's card ([Shell commands](../agents/tools-and-skills.md#shell-commands)). Not included by **Default tools** or `"*"`. |
| Tools of an MCP server | **MCP access** under the agent's **Tools** ([MCP](../mcp.md#grant-a-server-to-an-agent)). |
| Fusion services | **Fusion (REST-API tools)** on, and the service ticked under **Fusion services**. Check Up marks **Fusion on with no service (…)** otherwise ([MCPFusion](../mcpfusion.md#give-an-agent-access)). |
| Maestro, forum, memory tools | **Maestro**, **Allow forum**, **Cognitive memory** on the agent's card ([Tool suites](../agents/tools-and-skills.md#tool-suites)). |
| Any tool | Not listed under **Denied tools**. |

See [Tools and skills](../agents/tools-and-skills.md).

## A channel does not deliver

### Messages to the agent get no answer

1. **Is the channel enabled and running?** Check its page under **Channels**, and the alerts on the **Logs** page: "Channel failed to start", "Telegram polling failed", "Channel credentials rejected" or "Telegram down" (and the same for other platforms) name the channel and the reason.
2. **Is the sender allowed?** Every channel has an allowed-senders list (`allow_from`). An empty list lets **nobody** in. Add your user id, or `*` for everyone.
3. **Does the message reach the right agent?** Messages go to the agent a binding on the **Bindings** page names, else to the default agent. See [Routing and bindings](../channels/routing.md).
4. **Does the agent work?** Try the same message in **Chat**. If it fails there too, see [A model is unavailable](#a-model-is-unavailable).

### Scheduled jobs and notifications are not delivered

Scheduled job output and other messages the agent starts by itself go to the agent's default channel. Set it under **Default Channel** on the agent's card (see [Creating and editing agents](../agents/creating.md#default-channel)). For a channel with no fixed chat, such as a Telegram bot, enter the chat id too. Without one, the alert "Scheduled job failed" says the agent has no default channel.

### Replies are not delivered

The alert "Channel send failed" names the channel and the platform's reason. A recipient that is offline or unknown (a device not connected, a Telegram user who blocked the bot) is logged as a warning, not alerted.

See [Channels](../channels/index.md).

## Check Up rows

Common marked rows and what they mean:

| Row | Meaning | Fix |
|---|---|---|
| Data directory permissions | Files in the data directory can be read by others. | Restart ClawEh, which tightens them, or `chmod 700`/`600` as listed. |
| File confinement | Agents may read or write outside their workspace. | Turn on **Restrict to workspace** on the **System** page, under **Agent**. |
| Shell access | Agents may run shell commands without the built-in deny patterns. | Turn deny patterns back on (`tools.exec.enable_deny_patterns`), or remove **Allow shell commands** from the agents listed. |
| Channels accepting any sender | A channel's allowed senders contain `*`. | List the allowed users instead. |
| Message content in logs | Logging options write message text to disk. | Turn off the listed `logging` options if you do not want that. |
| Mount (…) | A mount uses a reserved folder name and is ignored. | Rename the mount on the **Agents** page. |
| Fusion on with no service (…) | The agent has Fusion on but no service ticked. | Tick its services under **Fusion services**. |

Rows without a mark describe the install and need no action. See [Check Up and the full report](../admin/checkup.md) for every row.

## Related pages

- [Configuration file](./configuration.md)
- [Command line](./cli.md)
- [Alerts](../admin/alerts.md)
- [Logs](../admin/logs.md)
