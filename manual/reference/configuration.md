# Configuration file

This page describes `config.json`: where it is, how changes are applied, and what each top-level section controls.

Most settings have a place in the WebUI, and that is the safer way to change them. This page is a map, not a list of every key.

## Where it is

`config.json` is in the data directory: `~/.claw/config.json`, or `$CLAW_HOME/config.json` when `CLAW_HOME` is set.

ClawEh writes a complete file on first start, with every section present and the known providers and models listed. That file is the reference for what can be set.

The file holds API keys and tokens, so it must be readable only by its owner. ClawEh refuses to start if group or others can read it. The fix is `chmod 600 config.json`.

## How changes are applied

- **From the WebUI.** Every page saves to `config.json`. A save that breaks a rule is refused, and the page lists every problem at once.
- **By hand.** Edit the file, or use **Raw config** on the **System** page. ClawEh checks the file for changes every 5 seconds (`config_reload_interval_seconds`) and reloads it.
- **Invalid edits.** If the file on disk no longer loads, ClawEh keeps running on the previous configuration and raises the [alert](../admin/alerts.md) "Config file invalid". The next restart would fail on that file, so fix it right away.
- **Restart needed.** Listener settings (ports, where HTTP and HTTPS listen, the certificate files) take effect only after a restart. The **Network** page offers **Restart now**.
- **Unknown keys.** A key ClawEh does not know is ignored, with the warning `unknown config key: <key>` in the log.

## Keeping secrets out of the file

Any credential field (an `api_key`, a `token`, a `…_secret` or `…password` field, MCP server `env` and `headers` values) may hold a reference instead of the value:

```json
"api_key": "env:OPENAI_API_KEY"
```

```json
"token": "file:/etc/claw/telegram.token"
```

- `env:NAME` reads the environment variable when ClawEh starts.
- `file:/path` reads the file. The path must be absolute and the file readable only by its owner.

The reference is written back unchanged when the configuration is saved. A reference that cannot be resolved stops ClawEh from starting, with a message naming the key.

## Top-level sections

| Section | What it controls | Where to change it |
|---|---|---|
| `agents` | The agents (`agents.list`): their ids, names, models, tools, skills, switches and mounts. `agents.defaults` holds values every agent inherits, such as the default model list, timeouts and memory settings. `agents.base_dir` and `agents.common_dir` move the agent folders and the shared folder. | **Agents** page; **System** page, sections **Agent** and **Agent defaults**. See [Agents](../agents/index.md). |
| `bindings` | Which agent answers which channel, chat or account, and each agent's default channel. Unbound messages go to the default agent. | **Bindings** page. See [Channels](../channels/index.md). |
| `agent_mentions` | `triggers`: the characters that introduce an agent's name in a message (default `@`, `/` and `.`). The names each binding answers to are set on the binding. | `config.json` only; the names per binding on the **Bindings** page. |
| `session` | `retention_days`: deletes idle conversation archives older than this. An agent's main conversation is never deleted. Absent means keep everything. | `config.json` only. |
| `channels` | Telegram bots, Signal (`secmsg`), Discord, Slack, Matrix, the WebUI chat (`webui`) and the device listener (`device`). Each channel has `allow_from`: an empty list lets **nobody** in, `["*"]` lets everyone in. | The pages under **Channels**; **Network** (**Device listener**) for the device listener. See [Channels](../channels/index.md) and [Devices](../channels/devices.md). |
| `providers` | Where models are reached: each provider's name, protocol, base URL and API key. CLI providers carry `bypass_restrictions` (**Allow CLI to bypass restrictions**). | **Providers** page. See [Providers and models](../providers/index.md). |
| `models` | The models agents can use: a name, the provider, the provider's model id, and limits such as `request_timeout` and `context_window`. | **Models** page. See [Providers and models](../providers/index.md). |
| `summarization` | The models used for memory consolidation and conversation summaries, tried before an agent's own models. | **System** page, **Agent defaults**. See [Memory models](../providers/concepts.md#memory-models). |
| `gateway` | The WebUI listeners: HTTP host and port, HTTPS port and `tls` mode and certificate, `external_url`, `allowed_cidrs`, `lockout_exempt` and `trusted_proxies`. | **Network** page, or `claw network`. See [Network access](../admin/network.md) and [HTTPS and certificates](../admin/https.md). |
| `tools` | Install-wide tool settings: web search providers, limits for shell commands, scheduled jobs, skills registries, the MCP servers ClawEh connects to (`tools.mcp.servers`). Which tools each agent gets is set on the agent. | **MCP** › **Servers** for MCP servers, **MCP** › **Config** for tool discovery, `config.json` for the rest. See [Tools and skills](../agents/tools-and-skills.md) and [MCP](../mcp.md). |
| `devices` | Hardware device events, such as USB plug and unplug. Not the paired devices. | **System** page, **Devices**. |
| `voice` | Speech-to-text for voice messages. | **Speech** page. |
| `logging` | Log level, log files, retention, and whether message content is logged. | **System** page, **Runtime** (retention); `config.json`. See [Logs](../admin/logs.md). |
| `security` | `message_prefix`: text put in front of messages that arrive through an agent's external message endpoint. | `config.json` only. |
| `mcp_host` | ClawEh's own MCP server, which the CLI providers use to call ClawEh's tools. It must listen on a loopback address. | **MCP** › **Config**. See [MCP](../mcp.md). |
| `cooldown` | How long a failing model is paused ([Cooldowns](../providers/concepts.md#cooldowns)), in minutes, per kind of failure: `billing_auth_minutes` (default 30), `rate_limit_minutes` (10), `client_error_minutes` (10), `server_error_minutes` (10), `bad_request_minutes` (0). | `config.json` only. |
| `backup` | The nightly backup: `enabled` (default on), `at` (default `03:00`), `retain_days` (default 30) and `dest` (default `backup/` in the data directory). | **System** page, **Backup**. See [Backup and restore](../admin/backup.md). |
| `forum` | `forum.limits`: the install-wide maximums for forums. | **System** page, **Forums**. See [Forums](../forums/index.md). |
| `default_config` | Marks a configuration that has never been saved. The setup wizard uses it. Leave it alone. | Set by ClawEh. |
| `config_reload_interval_seconds` | How often ClawEh checks `config.json` for changes (default 5). | `config.json` only. |

## Environment variables

`CLAW_HOME` sets the data directory.

Many settings can also be set with a `CLAW_…` environment variable, for example `CLAW_GATEWAY_PORT`. Such a variable overrides the value in `config.json`. Set it in the service definition, not in a shell, so the service sees it.

## Agent ids

An agent id is 1 to 64 characters of letters, digits, `-` and `_`, and starts with a letter or digit. Ids are case-insensitive: `Alice` and `alice` are the same agent. See [Agent ids](../agents/creating.md#agent-ids). The same rule applies wherever an agent is named: `bindings`, `agent_mentions` and other agents' `allow_agents`.

## Related pages

- [Command line](./cli.md)
- [Troubleshooting](./troubleshooting.md)
