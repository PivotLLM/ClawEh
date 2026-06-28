# ClawEh: Yet another claw - Canadian style

**ClawEh is a small, fast, self-hosted runtime for personal AI assistants.** Written in Go, it can run one or more agents, each with its own workspace, tools, and persistent memory, and connect them to Telegram, Slack, Discord, or the built-in web interface.

Although the conversation context can be reset at any time, ClawEh is designed primarily for long-running assistants that maintain continuity over time. Its development emphasizes efficient context management, practical persistent memory, security, and a stable, dependable core.

> **Development status:** This application is under active development and I use it on a daily basis for personal and business tasks. It is, however, a work in progress.

> **Web interface & authentication:** Like many "claw"-style apps intended for single-user use on a personal machine, ClawEh serves its web interface on localhost (loopback) and does not currently require authentication. This causes security challange for those who wish to run ClawEh on a VM or other headless system. Migrating to HTTPS with authentication seems obvious, but many users would end up requiring a self-signed certicate and be plagued by browser warnings. We are evaluating appropriate approaches for a future version and welcome input.

**Feature overview:**

- **Multi-agent architecture** — Run multiple named agents, each with its own workspace, models, tools, system prompt, memory, and channel bindings.
- **Strong security posture** — Only essential features are enabled by default, with fine-grained access controls for tools, files, agents, and external services.
- **Broad LLM support** — Connect to OpenRouter, Anthropic, OpenAI, Google Gemini, AWS, x.ai, and others, or use CLI agents such as Claude Code, Codex, and Gemini CLI. Configurable fallback chains and cooldowns improve availability.
- **Messaging channels** — Connect agents to Telegram, Slack, Discord, or the built-in web interface, with configurable per-agent routing. Additional channels are under consideration.
- **Cognitive memory** — Each agent can maintain persistent memory that updates in the background, distilling conversations into structured, de-duplicated facts and automatically recalling relevant information for future prompts.
- **Smart context management** — Automatic summarization and compaction, combined with per-turn eviction of stale tool output, keep long-running conversations responsive and within model context limits.
- **Message history** — Configurable retention and a searchable archive of past messages, organized by session.
- **Directory mounts** — Give an agent read-only or read-write access to selected directories, with optional notifications when new files appear.
- **Scheduled jobs** — Run cron-based recurring tasks, scheduled jobs, and reminders.
- **Maestro built in** — Orchestrate complex, multi-step work using projects, playbooks, and resumable task lists.
- **MCP server and client** — ClawEh provides its internal tools directly to API-based LLMs and exposes them through MCP to CLI agents. It can also connect to upstream MCP servers over stdio or HTTP, with granular control over which tools each agent may use.
- **File tools** — Sandboxed tools for reading, searching, and editing files by line or byte, along with move and delete operations and an optional shared directory for exchanging files between agents.
- **Web UI** — Manage agents, providers, channels, MCP connections, memory, and configuration without editing JSON manually.
- **Secure and self-hosted** — Workspace sandboxing, per-agent tool allowlists, and loopback-bound services, delivered as MIT-licensed Go software that you run on your own infrastructure.

## Features

### Maestro task orchestration

Maestro lets an assistant plan, coordinate, and execute complex work rather than handling every step sequentially in a single conversation. It can break large or repeatable jobs into **projects**, reusable **playbooks**, and resumable **task lists**, then delegate individual tasks to fresh sub-agents running with the parent agent's models, tools, permissions, and workspace. Independent tasks can run in parallel, failed tasks can be retried, and completed work can be passed through automated QA and review steps before the results are combined into a final report. This makes it practical to automate multi-stage workflows such as repository analysis, research, testing, pre-audits, document generation, and recurring operational procedures. Maestro is enabled per agent with a single toggle, and all of its data remains within that agent's workspace. The upstream project is also available as a stand-alone stdio MCP service: https://github.com/PivotLLM/Maestro

### MCP servers
ClawEh is both an MCP **server** — exposing its tools to CLI-based agents — and an MCP **client** that connects to upstream servers over **stdio** or **HTTP**, providing their tools to your agents. Add/edit external servers in the WebUI. Access is **granular per agent**: each agent is granted upstream tools individually (by server or tool-name prefix), and a coarse per-endpoint visibility filter controls what the host advertises.

### Context management
Two mechanisms keep long sessions inside the model's window. **Eviction** is a per-turn, LLM-free sweep that collapses re-retrievable tool results (file reads, web fetches) to a short placeholder once the agent has moved on. By evicting stale data before every dispatch, summarization fires far less often. **Compression** summarizes older conversation when the window fills. It can be tailored per agent with a `COMPRESSION.md` in the workspace. See [docs/context-eviction.md](docs/context-eviction.md).

### Cognitive memory
Long-running agents need to get smarter over time instead of relying on hand-edited prompt files. Each session has a small SQLite memory database. Memory is organized as **domains** — named containers that are either **sticky** (always in the prompt) or routed topics — holding **memories**, each a `fact`, `preference`, or `rule`. A background "sleep cycle" reviews new conversation and distills it into structured, de-duplicated, contradiction-resolved memories, and the relevant pieces are composed into the prompt each turn. Consolidation reuses your configured **Memory models**, and its prompt lives in an editable `COGMEM.md` in the workspace. 

The seeded **`General`** sticky domain holds global rules and standing facts; memory domains are auto-load by relevance using **recency**, **lexical match** (salient words in the latest message), **tool triggers** (a domain loads when the agent uses a matching tool — e.g. an "email" domain on `google_gmail`), and **keyword triggers** (phrases in the incoming message. This significantly improves agent performance without relying on external embedding services or vector databases.

When the agent infers something uncertain, it stores it as a **pending** memory and
asks you to confirm in chat (reply "yes" to keep it, "no" to drop it). Use **`claw
memory purge`** to clear everything that isn't current active memory — a dry run by
default; add `--confirm` to delete and vacuum. Stop the gateway first so you're not
racing live agents:

```bash
claw memory purge             # dry run — review the counts
claw memory purge --confirm   # delete + vacuum
```

### Agents, workspaces, and files
By default an agent's file tools see two directories: **`<workspace>/files`** (read
**and** write — its working area, created automatically) and **`<workspace>/skills`**
(read-only). Everything else is invisible to the agent, including the human-authored
prompt files — `AGENTS.md`, `SOUL.md`, `IDENTITY.md`, `USER.md`, `MEMORY.md` — which
are combined into the system prompt every turn. Keep those **brief and general**; the
agent no longer edits them, they're authoritative, and shape what it learns. A shared
**common directory** lets agents that are given access to it exchange files.

**External mounts.** Mount any folder on the file system as a top-level name beside `files/` and
`skills/` — per agent, on the **Agents page** (a name, an absolute path, and an
optional **notify** toggle). A mount `notes` → `/home/ai/Documents/mynotes` is reachable
as `notes/.... read **and** write, sandboxed so the agent can't climb above it (`..`
is rejected). Unless write permissions are explicitly granted, the directory is read-only. With **notify** on, claw watches the tree and messages the agent on its default channel whenever a **new** file appears.

**File tools** address content explicitly by **lines** or **bytes**, so units never
mix: `file_read_lines`/`_bytes`, `file_search_lines`/`_bytes`, the positional
`file_edit_lines`/`insert`/`delete` (line and byte variants), plus `file_edit`
(exact-text replace), `file_write`, `file_append`, `file_copy`, `file_move` (works
across mounts), and `file_delete` (requires `sure=true`; refuses to delete backups).

## Why ClawEh exists

ClawEh began as a fork of [PicoClaw](https://github.com/sipeed/picoclaw), chosen for its performant, easy to deploy, and maintainable Go foundation. I loved the PicoClaw concept and originally focused on fixing issues and contributing to the project. However, a growing PR backlog, and the apparent prioritization of new features over core stability make it clear that PicoClaw was unlikely to meet my needs in the foreseeable future. This is not a criticism of the PicoClaw authors, it simply reflects different priorities: a smaller, focused codebase emphasizing core stability, reliability, security, and maintainability.

## Binary distribution

To assist users who are not interested in compiling it themselves, I will be uploading recommended builds to GitHub for a variety of platforms. If you'd like another 

## Prerequisites

- [Go](https://golang.org/dl/) 1.21 or later
- [Node.js](https://nodejs.org/) 20.19+ or 22.12+ (for building the web frontend)
- [pnpm](https://pnpm.io/installation) — install via `npm install -g pnpm`

**Do not install Node.js via `apt`** — the packaged version is too old. Use the [NodeSource repository](https://github.com/nodesource/distributions) for a system-wide install:

```bash
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt install -y nodejs
npm install -g pnpm
```

Alternatively, use [nvm](https://github.com/nvm-sh/nvm) for a per-user install (run as your regular user, not root):

```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash
# open a new shell, then:
nvm install 22
nvm alias default 22
npm install -g pnpm
```

## Building

Clone the repository and build the single `claw` binary (the WebUI is embedded):

```bash
git clone https://github.com/PivotLLM/ClawEh.git
cd ClawEh
make install
```

This builds `claw` (gateway + WebUI + session API in one binary) and installs
it to `~/.local/bin`. The frontend bundle in `web/frontend` is built and
embedded into the same binary, so a single `claw` invocation serves
everything — the chat agent, the WebSocket WebUI, and the JSON config API —
on `cfg.Gateway.Port` (default `18790`).

If you do not have Node.js and pnpm installed, you can still build the agent
side as long as `web/backend/dist/index.html` is already present (the
embedded asset directory is committed empty with a `.gitkeep`, so missing
frontend assets just mean the WebUI 404s, the gateway still runs).

**Available make targets:**

| Target | Description |
|---|---|
| `make build` | Build `claw` (with embedded WebUI) for the current platform |
| `make install` | Build and install `claw` to `~/.local/bin` |
| `make test` | Run tests |
| `make clean` | Remove build artifacts |

### Shared modules

Two pieces of ClawEh are factored into standalone, dependency-free Go modules so
they can be reused (e.g. by Maestro) and compiled into one binary without import
cycles:

- **[`toolspec`](https://github.com/PivotLLM/toolspec)** — the transport-neutral
  tool contract (`ToolProvider`/`ToolDefinition`/`Result`). Tool packages and
  external hosts implement this.
- **[`spawnllm`](https://github.com/PivotLLM/spawnllm)** — the LLM-dispatch core:
  the provider clients (OpenAI-chat/responses, Azure, Anthropic, and the
  claude/codex/gemini CLIs) plus the API tool-call loop. spawnllm imports only
  `toolspec` + stdlib; it never imports a host. Policy — model selection,
  fallback, cooldown, config, results handling — stays in ClawEh.

`pkg/global` and `pkg/providers` are thin alias shims re-exporting these modules,
so a normal `go build` fetches them by version tag; no extra setup is needed.

## Running

Start claw with the bare command (no subcommand):

```bash
claw
```

The WebUI is served on `http://localhost:18790` — open that URL in a browser
to reach the chat interface and configuration console. The port is
configurable via `gateway.port` in `~/.claw/config.json`.

## Terms of Use and Compliance
ClawEh supports a wide range of LLM providers. It is your responsibility to ensure that your use of any provider, API, service, or model is consistent with the applicable terms of service, acceptable use policies, contracts, and legal requirements. This includes use-case restrictions, data handling obligations, and any prohibition on accessing non-public or undocumented APIs. We have removed support for some providers where we determined the implementation could not reasonably be used without violating the provider's terms. We welcome feedback from any LLM providers on this topic.

## Assistant behaviour

`session_scope` (in the `session` config block) controls how an agent's memory is divided across users and platforms.

| Mode | Memory per | Description |
|---|---|---|
| `unified` | Agent | One shared memory for the entire agent, across all users, channels, and platforms. |
| `per-user` | Person | Each person gets their own private memory. Recognises the same person across platforms if `identity_links` are configured; otherwise each platform ID is a separate person |
| `per-platform` | Person × platform | Each person has a separate memory per platform. Slack and Telegram are independent conversations even for the same person |
| `per-account` | Person × platform × bot | Like `per-platform`, but also separates by bot account. Relevant only when multiple bots on the same platform are routed to the same agent |

The default is `unified`.

**Choosing a mode**

*Personal assistant, or a purpose-built specialist* — use `unified`. This is the right choice in two situations. For a personal assistant: one continuous memory across all your channels, it knows your preferences, remembers your projects, and picks up where you left off regardless of where you reach it. For a purpose-built assistant — if you create an agent named Alice who specialises in security, there is one Alice. Anyone who contacts her, through any channel you have configured, is talking to the same Alice with the same accumulated knowledge and context. She does not have separate memories for different users; she is one coherent assistant.

*Shared assistant for a team or family* — use `per-user`. Each person gets their own private relationship with the assistant — their own context, their own memory, no bleed between users. If the same person might contact the assistant from multiple platforms, configure `identity_links` to tell the system they are the same person (see below). However, before going this route, consider using `unified` mode and creating a separate agent for each user.

*Keeping contexts separate by platform* — use `per-platform`. Each person gets a separate session per platform, so a user's Slack and Telegram conversations are fully independent even when handled by the same agent.

*Multiple independent bots on the same platform* — use `per-account`. Each bot maintains its own memory per user even when multiple bots are handled by the same agent. Rarely needed — if you have multiple bots you most likely have multiple agents already.

**Linking a person across platforms**

In `per-user` mode, the same person on different platforms is only recognised as the same person if you configure `identity_links`:

```json
"session": {
  "session_scope": "per-user",
  "identity_links": {
    "alice": ["telegram:123456789", "U0SLACKUSERID"]
  }
}
```

Without this, a person's Telegram ID and Slack ID are treated as two separate people even in `per-user` mode.

**One-shot tasks without context**

In `unified` mode every conversation adds to the shared memory. If you want the agent to handle a task in isolation, without drawing on prior chat history and without polluting the main conversation, ask it to use the `spawn` tool. A spawned sub-agent is a **copy of the agent** (same workspace, tools, MCP, prompt, and a read-only snapshot of its memory) running on the given task in a separate session, optionally on a different model. It completes the work and reports back; nothing from that exchange appears in or affects the main conversation, and it cannot write the agent's memory, schedule jobs, or spawn further sub-agents. See [docs/subagents.md](docs/subagents.md).

**Security: access control**

Every channel has an `allow_from` list. An empty list means **nobody** can connect. Set it to your user IDs to restrict access, or `["*"]` to allow all users.

ClawEh is designed as a personal assistant framework. We strongly advise against allowing untrusted users to access your assistants. In `unified` mode in particular, every person who can reach the assistant is contributing to — and reading from — the same shared memory and context. An untrusted user can see the assistant's full history of what it knows and has been told. Only grant access to people you trust completely, and ensure you fully understand the security implications before opening any channel beyond your own use.

On platforms like Telegram where bots are publicly discoverable by username, this risk is especially acute. Always set `allow_from` explicitly.

## Security Considerations

ClawEh is intended to function as personal assistant that runs on a computer the user controls. It is not designed or intended to provide any kind of public service. The current web interface uses HTTP and has no authentication, and therefore should not be exposed to untrusted networks. We strongly recommend running it on `localhost` only. We are aware that many people wish to run a "claw" application on a headless computer and are considering the right path forward.

The web management API has no authentication layer. Any client that can reach the management port can add or modify model configurations (including API keys and endpoints), read session history, and start or stop the gateway process. Access control relies entirely on the listen address (localhost-only by default) and, when running in public mode, the IP allowlist. Do not run with `-public` and an empty `allowed_cidrs` list on any network where untrusted hosts could reach the port.

In general, Claw should be operated as a local, user-controlled tool, not as an internet-facing application.

This application assumes that messages sent to assistants come from an authorized user and are not malicious. While it may provide configuration options to restrict which users on services such as Slack or Telegram are allowed to communicate with an assistant, misconfiguration is possible, and flaws, bypasses, or unexpected behavior may exist. Exposing an assistant through external communication channels can be very convenient, but it also increases risk substantially: if an account is compromised, access controls are configured incorrectly, or a bug or unknown vulnerability is present, unauthorized parties may be able to interact with the assistant and trigger actions or consume paid API resources.

When enabling tools, including connecting MCP servers, users must carefully consider the security and privacy implications. Granting an assistant access to sensitive systems or data can have severe consequences if that assistant is exposed beyond the intended user. For example, giving an assistant access to email and then accidentally allowing anyone on Telegram to interact with it could have catastrophic privacy and security consequences. The same principle applies to file systems, shells, calendars, internal APIs, and any other connected capability.

ClawEh includes an HTTP external-message endpoint that is disabled by default. When enabled for an individual agent, an authenticated external process can deliver a message into the agent's active conversation (`POST /api/message/{token}`); the agent then responds on its standard communication channel. If this feature is used, care must be taken to avoid it being abused for malicious purposes. See [docs/external-messages.md](docs/external-messages.md).

Users should also understand that data made available to an assistant through connected tools and services may contain malicious or misleading content. Content from email, chat systems, documents, web pages, issue trackers, or other data sources could potentially be interpreted or acted on by the assistant as if it were an instruction. It is the user's responsibility to ensure that they fully understand the risks, that appropriate security controls are in place, and that, where necessary, appropriate testing has been conducted.

ClawEh does not attempt to determine whether your deployment is appropriately secured for your use case. While it is possible to configure external channels so that other individuals can access assistants, doing so is entirely the user's choice and entirely the user's responsibility. You are solely responsible for deciding whether ClawEh is suitable for your application, whether the security posture is acceptable, and for all consequences of how the software is configured and used.

Users should also carefully consider the financial implications of connecting applications to LLM APIs and other metered services. A bug, a bad configuration, or accidental exposure through channels like Telegram or Slack can potentially result in large numbers of unintended requests and, in turn, unexpectedly large bills. This risk becomes especially serious when assistants are connected to paid APIs, tools, or automated workflows.

To reduce the risk of financial surprises, we strongly recommend using prepaid APIs and/or subscription-based CLIs where possible. You should also ensure that appropriate cost monitoring, usage limits, budget alerts, rate limits, and other containment controls are in place. Choosing to run Claw, expose it through external services, and connect it to paid models or sensitive tools is your decision, and you bear full responsibility for the outcome.

## Running as a service (Linux)

`claw.service` in the project root is a systemd unit file for running ClawEh
as a background service on Linux. The merged binary handles the gateway, the
WebUI HTTP layer, and the session API in one process — there is no longer a
separate `claw-web` service. Replace `YOUR_USERNAME` with the user account
the service should run under, then install with:

```
sudo cp claw.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now claw
```

ClawEh writes logs to `~/.claw/logs/claw.log`. No log redirection is required
in the service file. See [Logging](#logging) for rotation and retention.

## Logging

ClawEh writes its own log files to `$CLAW_HOME/logs/` (default `~/.claw/logs/`),
so the systemd unit needs no `StandardOutput`/`StandardError` redirection:

- **`claw.log`** — the main log: everything at or above the configured `level`.
- **`error.log`** — a high-signal companion containing only `WARN` and above, so
  you can scan for problems quickly without wading through routine activity.
  Fatal errors (including config-file errors that abort startup) are written
  here as well as to `claw.log`.

### Daily rotation and retention

Both files roll once per day. At local midnight the active `claw.log` / `error.log`
are renamed to date-stamped archives — `YYYYMMDD-claw.log` and `YYYYMMDD-error.log`
— and fresh active files are opened. The date is taken from each file's
last-modified time, so if the gateway was not running at midnight the roll
happens as soon as it next starts, stamping the archive with the day the log
actually covers.

Retention is a single setting, `logging.retention_days`. After each roll,
date-stamped archives older than that many days are deleted; **`0` keeps logs
forever**. The default is `30`. It can also be set via the
`CLAW_LOGGING_RETENTION_DAYS` environment variable, and is editable in the WebUI
under **Config → Runtime → Log retention (days)** (changes apply on the next
gateway start). Only `YYYYMMDD-*.log` archives are pruned — the active
`claw.log` / `error.log` and the `dumps/` directory are never touched.

### Logging options

```json
{
  "logging": {
    "file": true,
    "console": true,
    "level": "info",
    "json": false,
    "retention_days": 30,
    "log_message_content": false,
    "dump_refusals": true,
    "dump_all": false,
    "dump_failed_compressions": false
  }
}
```

| Option | Default | Description |
|---|---|---|
| `file` | `true` | Write to `claw.log` (and `error.log`). |
| `console` | `true` | Also write to stdout/console. |
| `level` | `info` | Minimum level: `debug`, `info`, `warn`, `error`. |
| `json` | `false` | Emit JSON instead of the human-readable console format. |
| `retention_days` | `30` | Days of rolled daily logs to keep; `0` = keep forever. |
| `log_message_content` | `false` | Include message text and API request/response bodies in logs. Off by default for privacy. |
| `dump_refusals` | `true` | See [Diagnostic dumps](#diagnostic-dumps). |
| `dump_all` | `false` | See [Diagnostic dumps](#diagnostic-dumps). |
| `dump_failed_compressions` | `false` | Write a dump when a context-compaction (summarization) attempt fails. |

Each option also has a `CLAW_LOGGING_*` environment override (e.g.
`CLAW_LOGGING_LEVEL`, `CLAW_LOGGING_RETENTION_DAYS`).

## Configuration backup

ClawEh takes a nightly **configuration backup** — **on by default**. It snapshots `config.json` and the cron jobs file (`jobs.json`) into `$CLAW_HOME/backup/YYYYMMDD/`, with each file timestamped (e.g. `config.json.20260622-030000`) so repeated runs in a day don't overwrite. Day-folders older than the retention window are pruned.

This is **configuration only** — it does **not** include agent workspaces, session archives, cognitive-memory databases, or the `state/` token files. It's a safety net for your settings and schedules, not a full data backup.

Manage it in the web console under **Config → Configuration backup**, or in `config.json`:

```json
"backup": { "enabled": true, "at": "03:00", "retain_days": 30 }
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Set `false` to turn the nightly backup off. |
| `at` | `03:00` | Local time of day (`HH:MM`) to run. |
| `retain_days` | `30` | Delete backup folders older than this. |

The scheduler re-reads config every minute, so changes take effect without a restart. The **Back up now** button (or `POST /api/backup`) runs a backup immediately, regardless of the nightly toggle.

## Diagnostic dumps

ClawEh can write full LLM request/response snapshots to disk for debugging. Files are written to `$CLAW_HOME/logs/dumps/` (e.g. `~/.claw/logs/dumps/`). Each dump produces two files sharing a common base name (`YYYYMMDD-HHMMSS-<id>`):

- **`.json`** — structured JSON with three top-level keys (`metadata`, `input`, `output`) where `input` and `output` are proper nested JSON objects, suitable for programmatic parsing.
- **`.txt`** — human-readable version: metadata as key/value pairs, input and output as pretty-printed JSON with escaped `\n` sequences expanded to real newlines so message content is legible.

The warning log entry emits the base name for correlation to both files.

Two options are available in the `logging` config block:

```json
{
  "logging": {
    "dump_refusals": true,
    "dump_all": false
  }
}
```

| Option | Default | Description |
|---|---|---|
| `dump_refusals` | `true` | Write a dump whenever the LLM returns `finish_reason: "refusal"`. A `WARNING` log entry is emitted with the filename and correlation fields (agent, model, session key, channel). |
| `dump_all` | `false` | Write a dump for every LLM response regardless of finish reason. Logged at `DEBUG` level. Useful for deep debugging; generates significant disk output under normal load. |

When `dump_refusals` is `true` and the response is a refusal, only the refusal dump is written — `dump_all` does not also fire for the same response.

## External message endpoint

ClawEh provides an optional per-agent HTTP endpoint that lets external processes deliver a message into an agent's active conversation without a persistent channel connection. The agent receives the message on its last active channel and responds normally.

```
POST http://localhost:18790/api/message/{token}
```

The rotating per-agent token is configured under **Agents** (`message.window_minutes` / `window_count`). It is **not** injected into the agent's prompt — the endpoint is reserved for operator/integration use and a future "notify an agent" feature. See [docs/external-messages.md](docs/external-messages.md).

> **Security notice:** plain HTTP, bound to `127.0.0.1` only. Do not expose it externally.

## Service tokens (long-lived MCP credentials)

For an external MCP client that needs to drive an agent's tools — most notably **Maestro** — on a stable footing, mint a long-lived per-agent **service token**. Unlike the per-session token (which rotates, idles out after 2h, and dies on restart), a service token persists until you revoke it.

```
claw token issue  <agent>   # mint (or replace) and print the agent's service token
claw token rotate <agent>   # replace the existing token
claw token revoke <agent>   # remove it
claw token list             # list agents that have one (tokens are not shown)
```

Use the token as an `Authorization: Bearer` header on `/mcp`, or as the `session_token` parameter on `/internal` — both resolve identically. A service token is **headless and isolated**: it resolves to a dedicated `agent:<id>:service` session, so it cannot read the agent's real conversations, and a tool's user-facing output is dropped (only the model-facing result returns to the caller). Tokens are stored at `$CLAW_HOME/state/service-tokens.json` (`0o600`); a running gateway picks up the change automatically within a few seconds. See [docs/service-tokens.md](docs/service-tokens.md).

## Context management

Each agent session maintains a persistent SQLite archive of every message it sends and receives. When the context window fills, claw compresses older messages into a structured summary and moves them out of the active window, keeping the conversation continuous across long sessions.

### How it works

1. **Archive** — every message is written to a per-session SQLite database (WAL mode, FTS5 index) with a sequence number and timestamp. Tool result content larger than 4 096 bytes is truncated before archiving so that large file reads don't produce unbounded blobs.

2. **Compression trigger** — after each LLM turn, claw estimates the token usage of the current context window. Compression is triggered when usage crosses configurable thresholds (default: compress at 50 %, safety compress at 80 %). A minimum message count (default: 20) prevents compressing very short conversations.

3. **Summarisation** — the oldest messages above a retained tail are sent to a summarisation model (defaults to the agent's primary model). The summary is structured JSON covering topics, decisions, key moments, and a retrievable history index. The rendered summary is injected at the top of the context window on every subsequent turn, and begins with the date range and message numbers it covers so the LLM can orient itself after compaction.

4. **Retrieval** — the archive is available via the session tools (see [Session tools](#session-tools) below). The LLM can query any archived message by sequence number or full-text search, and browse its own past summaries, so nothing within the retention window is lost. Clearing a conversation (`/clear`) **preserves** this long-term memory — it resets only the active window, not the archive or summary log. A hard wipe is done by deleting the session's `.archive.db` manually.

5. **Retention** — by default the archive and summaries grow without bound (nothing is pruned). Optionally bound them by age or count (see [Configuration](#configuration)); pruning runs on archive open and after each compaction.

### Configuration

Context management options live in the agent config block (or `agents.defaults`):

| Field | Default | Description |
|---|---|---|
| `context_window` | `128000` | Model context window in tokens. Used to compute compression thresholds. |
| `compress_chars_per_token` | `4.0` | Characters-per-token divisor for the token estimate. Lower values estimate more tokens (more conservative); tune toward `3.5` for code/JSON-heavy sessions. |
| `compress_token_safety_margin` | `1.0` | Multiplier applied to every token estimate so it errs high, triggering compression earlier. `1.1` inflates the estimate by 10%. |
| `archive_message_count` | `0` (unlimited) | Keep at most this many recent messages per session — also the retrieval/citation window. Oldest beyond *n* are pruned. `0` = unlimited; falls back to `archive_days`. |
| `archive_days` | `0` (unlimited) | Permanently delete archived messages older than *n* days. `0` = no age limit. |
| `summary_max_count` | `0` (unlimited) | Keep at most this many recent context summaries. `0` = unlimited; falls back to `summary_retention_days`. |
| `summary_retention_days` | `0` (unlimited) | Permanently delete context summaries older than *n* days. `0` = no age limit. |
| `archive_content_max_bytes` | `4096` | Maximum per-message content bytes stored in the archive; longer content is truncated (the active context still saw the full text). |
| `compress_model` | agent's primary model | Model used for summarisation. Can be a cheaper or faster model. |

> The code default for all four retention knobs is `0` (unlimited). A **newly generated** config file ships `archive_days: 365` and `summary_retention_days: 3650`; existing configs are left untouched on upgrade. A count of `0` disables the count cap and defers to the day limit.

Example (per-agent override):

```json
{
  "id": "alice",
  "model": "claude-opus-4-7",
  "context_window": 200000,
  "archive_message_count": 2000,
  "compress_model": {
    "model": "claude-haiku-4-5",
    "provider": "anthropic"
  }
}
```

### Session tools

The LLM has access to six session management tools:

| Tool | Description |
|---|---|
| `session_info` | Returns session metadata: key, start time, channel, context message count, archive bounds, current summary coverage, and last compaction time. Use to orient after a context compression. |
| `session_compact` | Triggers an immediate context compaction. The LLM can call this after completing a major task to free context window space before starting the next one. |
| `session_messages` | Returns archived messages in a sequence range. Assistant messages include paired tool call inputs and results. Each message carries a timestamp and sequence number. |
| `session_search` | Full-text search over the archived message history. Returns matching messages with sequence numbers and timestamps. |
| `session_summary_list` | Lists recorded context-summary checkpoints (id, covered message range, dates, model) so the LLM can review what it previously summarised. |
| `session_summary_get` | Retrieves the full text of one context-summary checkpoint by id. |

These tools are available to the LLM in both access paths described in the next section.

---

## MCP server (claw as an MCP host)

ClawEh can expose a subset of its host-side tools to MCP-compatible clients over a Streamable HTTP transport. This is primarily intended for CLI providers (Claude Code, Codex CLI, Gemini CLI) so they can call claw's tools natively instead of printing tool-call JSON in their prose — which historically caused runaway outer loops, since those CLIs are themselves agentic and return a single final answer per invocation.

### Tool access paths

There are two ways an LLM running inside claw can call tools, depending on the provider type:

**Path 1 — LLM API tool calls (direct API providers)**

For providers that use the direct LLM API (`anthropic`, `openai`, `openai-compat`, `gemini`), tool calls are returned as structured blocks in the API response (`tool_use` / `function_call`). The agent loop intercepts these, executes the tool, and feeds the result back to the LLM. No `session_token` is required — the session context is implicit in the agent loop.

All tools are available on this path, including the session tools (`session_compact`, `session_info`, `session_messages`, `session_search`, `session_summary_list`, `session_summary_get`).

**Path 2 — MCP HTTP server (CLI providers)**

For CLI providers (`claude-cli`, `codex-cli`, `gemini-cli`), the CLI subprocess has no access to claw's internal session state. Tools are called via the MCP HTTP server at `http://127.0.0.1:5911/internal`. Every tool call on this path carries a `session_token` parameter — a short-lived `SST<64hex>` token injected into the agent's system prompt at session start. The MCP server resolves this token to the correct agent and session, then executes the tool.

For session-scoped tools, the MCP server uses the session token to inject the session key into the execution context. The session tools implement the `SessionScoped` interface, so the dispatcher injects the key automatically — no hardcoded list to maintain.

| | Direct API providers | CLI providers |
|---|---|---|
| Tool call mechanism | API response (`tool_use` blocks) | MCP HTTP at `:5911/internal` |
| Session context | Implicit (agent loop) | `session_token` parameter |
| Session tools available | All six | All six |

> **Important:** CLI providers (`claude-cli`, `codex-cli`, `gemini-cli`) no longer receive tool descriptions in their prompt. Each invocation runs as a single agentic turn, and the CLI reaches claw's tools only via MCP. **You must register claw as an MCP server in each CLI you intend to use** — see [Client configuration](#client-configuration) below. Without that step, the CLI will still answer prompts, but it will have no access to claw's filesystem, web, or other host-side tools.

The server auto-starts whenever any enabled model in `models` uses a `*-cli` protocol (`claude-cli`, `codex-cli`, `gemini-cli`), since those CLIs depend on MCP for native tool calls. Set `enabled: true` to force it on regardless, or `auto_enable: false` to opt out of the auto-start. Full config shape with defaults:

```json
{
  "mcp_host": {
    "enabled": false,
    "auto_enable": true,
    "listen": "127.0.0.1:5911",
    "endpoint_path": "/mcp",
    "tools": [
      "read_file",
      "write_file",
      "edit_file",
      "append_file",
      "list_dir",
      "web_fetch",
      "web_search",
      "send_file"
    ]
  }
}
```

The `tools` list is a single global allowlist applied to all MCP clients (not per-LLM). Supports `"*"` (all tools), prefix globs like `"read_*"`, and exact names. The agent's internal `message` tool is never exposed regardless of the allowlist. Tools inherit the default agent's workspace and sandboxing rules.

### Consuming external MCP servers (claw as an MCP client)

Claw can also connect **outward** to third-party (upstream) MCP servers and make their tools available to your agents. **Manage them in the WebUI (the MCP page)** — add each server with **Add server** (transport **stdio** or **http**; `sse` is a deprecated alias of http), and enable it. No JSON editing required; the underlying config is `tools.mcp.servers`. Claw connects to enabled servers on startup, lists each server's tools, and registers them — no extra switch to flip.

(`tools.mcp.enabled` and `tools.mcp.auto_enable` remain in the config file and **both default to on**, so defining and enabling a server is all that's needed.)

Both provider types get the external tools **through claw** — full feature parity, no per-CLI setup:

- **Direct API providers** (`anthropic`, `openai`, `openai-compat`, `gemini`): claw lists the upstream tools, presents them to the model alongside its own, and proxies each call.
- **CLI providers** (`claude-cli`, `codex-cli`, `gemini-cli`): claw aggregates the external tools into its own MCP host, so a CLI that already talks to claw (see [Client configuration](#client-configuration)) sees them too — claw proxies the calls. (If you instead want a CLI to reach an external server *directly*, configure it in that CLI's own MCP config.)

Per-agent tool allowlists apply: allow an external server's tools with the `mcp_<server>_*` pattern (or `*`).

### Client configuration

The server speaks the Streamable HTTP transport on the MCP host `listen` address (default `127.0.0.1:5911`, loopback only — do not expose it externally) and offers **two endpoints**:

- **`/internal`** — authenticated by a per-call `session_token` parameter. This is ClawEh's multi-assistant routing: one local CLI install can act as **several agents** by supplying the appropriate agent's `session_token` on each call. **Local CLIs that authenticate as multiple agents should use `/internal`.**
- **`/mcp`** — standard bearer auth (`Authorization: Bearer <token>`), one identity per connection, for external/generic MCP clients.

See [docs/mcp.md](docs/mcp.md) for the full design.

#### Quick setup — `set-mcp.sh`

The `set-mcp.sh` script in the repo root registers (or refreshes) claw in whichever of Gemini CLI, Codex CLI, and Claude Code are **installed on your PATH** — the rest are skipped. It removes and re-adds each CLI one at a time, pointing them at the `/internal` endpoint:

```bash
./set-mcp.sh
```

> **Port:** the script's URL must match the MCP host `listen` port in your config (`tools → mcp_host → listen`). **If you change that port, update the script** — edit `CLAW_MCP_URL` at the top, or run `CLAW_MCP_URL=http://127.0.0.1:<port>/internal ./set-mcp.sh`.

The per-CLI commands it runs are below if you prefer to do it by hand.

#### Claude Code

To register claw as an MCP server scoped to the user (all projects):

```bash
claude mcp add --transport http claw --scope user http://127.0.0.1:5911/internal
```

To list configured MCP servers:

```bash
claude mcp list
```

For further information:

```bash
claude mcp -h
```

#### Codex CLI

Register claw with the `codex mcp add` command:

```bash
codex mcp add claw --url http://127.0.0.1:5911/internal
```

This writes the entry to `~/.codex/config.toml`. You can also edit the file directly:

```toml
[mcp_servers.claw]
url = "http://127.0.0.1:5911/internal"
```

#### Gemini CLI

[Gemini CLI](https://github.com/google-gemini/gemini-cli) supports MCP servers via the `gemini mcp add` command or by editing `~/.gemini/settings.json` directly.

```bash
gemini mcp add claw http://127.0.0.1:5911/internal --scope user --transport http
```

Omit `--scope user` to configure claw at the project level instead.

> **Warning:** The `--trust` flag grants Gemini CLI unrestricted access to all MCP tools without prompting for permission. Only use `--trust` in controlled environments where you fully trust the MCP server and its tools.

To grant access to all tools without prompting (use with caution — see warning above):

```bash
gemini mcp add claw http://127.0.0.1:5911/internal --scope user --transport http --trust
```

Alternatively, add the following to `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "claw": {
      "url": "http://127.0.0.1:5911/internal",
      "type": "http"
    }
  }
}
```

#### Clients without HTTP transport support

For clients limited to stdio MCP transport (e.g., Claude Desktop), bridge to claw's network-based server using <https://github.com/PivotLLM/MCPRelay>.

### Testing

The MCP server integration tests are fully self-contained. `./test.sh -i` builds a fresh claw binary, starts an ephemeral gateway in a temporary `CLAW_HOME`, runs the probe-driven test suite, then tears everything down.

```bash
./test.sh -i          # unit tests + MCP integration (self-contained)
./test.sh -i -x       # same, but preserve artifacts for debugging
```

Requires [`probe`](https://github.com/PivotLLM/MCPProbe) on `PATH`.

The test suite runs in two tiers:

- **Tier 1** — always runs: tool catalogue checks (all expected tools present) and unauthenticated rejection (verifies `session_token` is enforced on every call).
- **Tier 2** — automatically enabled by `./test.sh -i`: file operation round-trips and session tool smoke tests using a per-run `CLAW_MCP_TEST_TOKEN` generated at startup and passed to the gateway via environment variable. The token is never written to a config file.

To run Tier 2 against an already-running claw instance, set `SESSION_TOKEN` to the `SST<64hex>` token from an active session's system prompt:

```bash
SESSION_TOKEN=SST... ./tests/test_mcpserver.sh
```

## Third-party integrations

ClawEh takes a deliberately narrow approach to third-party integrations. In keeping with its focus on security, privacy, and maintainability, a number of integrations present in the upstream project have been removed or disabled by default. This includes messaging platforms, external registries, and service integrations that were not aligned with the project's goals or present unjustifiable security risks. The integrations that remain are ones we consider broadly useful and consistent with the project's goals of a small footprint, reliability, and long-term maintainability.

Rather than directly integrating tools into the software, ClawEh focuses on solid MCP (Model Context Protocol) support, allowing users to connect the specific tools they want and trust. Direct tool integrations will only be added when there is a compelling reason that MCP cannot address.

## Relationship to PicoClaw

ClawEh began as a fork of PicoClaw and has evolved into a separate, independently maintained project.

Original code remains under the MIT License, and ClawEh continues under the MIT License as well. This project preserves the original copyright and license notices and includes additional copyright for new modifications in this fork.

Nothing about this project is intended to hold work back from the community. On the contrary, if others find parts of ClawEh useful, they are welcome to reuse, adapt, and build on them under the same MIT terms.

## Project history

Before starting this project, I contributed a number of fixes and improvements upstream. The list below is retained for historical context and to help explain some of the technical and maintenance goals behind ClawEh.

| PR                                                    | Change                                                       |
| ----------------------------------------------------- | ------------------------------------------------------------ |
| [#1460](https://github.com/sipeed/picoclaw/pull/1460) | fix(openai_compat): fix tool call serialization for strict OpenAI-compatible providers |
| [#1479](https://github.com/sipeed/picoclaw/pull/1479) | fix(claude_cli): surface stdout in error when CLI exits non-zero |
| [#1480](https://github.com/sipeed/picoclaw/pull/1480) | docs: document claude-cli and codex-cli providers in README  |
| [#1625](https://github.com/sipeed/picoclaw/pull/1625) | feat(channels): support multiple named Telegram bots         |
| [#1633](https://github.com/sipeed/picoclaw/pull/1633) | feat(providers): add gemini-cli provider                     |
| [#1637](https://github.com/sipeed/picoclaw/pull/1637) | fix(agent): dispatch per-candidate provider in fallback chain |
| [#1810](https://github.com/sipeed/picoclaw/pull/1810) | fix(launcher): recognise gemini-cli as a credential-free CLI provider |
| [#1811](https://github.com/sipeed/picoclaw/pull/1811) | fix(launcher): detect and display externally-managed gateway as running |
| [#1812](https://github.com/sipeed/picoclaw/pull/1812) | fix(claude-cli): pass system prompt via stdin instead of CLI argument |
| [#1813](https://github.com/sipeed/picoclaw/pull/1813) | fix(providers): robust CLI tool call extraction and mixed response handling |
| [#1814](https://github.com/sipeed/picoclaw/pull/1814) | fix(subagent): dispatch subagents through per-agent provider; enforce allowlist on self-spawn; attribute responses |
| [#1816](https://github.com/sipeed/picoclaw/pull/1816) | fix(cron): show all payload fields in cron list output       |
| [#1839](https://github.com/sipeed/picoclaw/pull/1839) | fix(cron): route cron jobs to correct agent and publish response to channel |
| [#1842](https://github.com/sipeed/picoclaw/pull/1842) | fix(cron): reload store on external file change; only save when state changes |
| [#1847](https://github.com/sipeed/picoclaw/pull/1847) | fix(providers): honour request_timeout for CLI providers with clear timeout errors and fallback |

## Copyright and license

Copyright (c) 2026 Tenebris Technologies Inc.
Somce code Copyright (c) 2026 PicoClaw contributors 

This software is licensed under the MIT License. Please see `LICENSE` for details.

## Trademarks

Any trademarks referenced are the property of their respective owners, used for identification only, and do not imply sponsorship, endorsement, or affiliation.

## No Warranty

**(zilch, none, void, nil, null, "", {}, 0x00, 0b00000000, EOF)**

THIS SOFTWARE IS PROVIDED “AS IS,” WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NON-INFRINGEMENT. IN NO EVENT SHALL THE COPYRIGHT HOLDERS OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Made in Canada with internationally sourced components.
