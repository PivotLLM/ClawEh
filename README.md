# ClawEh: Yet another claw - Canadian style

**ClawEh is a self-hosted application for long-running AI assistants: several agents, each with its own models, tools, memory and channels, managed from one web interface.**

Written in Go, the code compiles into a single binary that you can download from Github releases, or you can compile it yourself.

## Features

- **Multiple agents** with their own workspaces, models, tools, memory and communication channels.
- **API and CLI models**: cloud APIs and CLI agents are supported with fallback chains and cooldowns
- **Coding harness tools**: find, search, read and edit code, and run commands.
- **Maestro workflow orchestration**: reusable playbooks, projects, task lists run by worker sub-agents, and reports
- **Forums**: structured discussions or role play among agents, run to a final report
- **MCPFusion hub and configuration-defined MCP servers**: REST APIs described in a file become tools, connection to upstream MCP servers, and internal tools.
- **Speech and image conversion**: transcription, text-to-speech, image generation, and image descriptions through task models
- **IM integration**: Telegram, Slack, Discord, Matrix, Signal and a built-in web chat
- **Devices and the message API**: the Rabbit R1, voice apps, and other applications that send messages to an agent via an OpenClaw-compatible WebSocket.
- **Cognitive memory and context management** that keep long-running agents useful and improving over time.
- **Sub-agents** that take on work for an agent, and messages between agents
- **Human agents**: a person who answers questions from agents in their own chat
- **Classifiers with MCP integration**: sort email, files or tickets from any tool or MCP server an agent can use, and act on each category
- **Character cards** in the Character Card V3 format
- **Scheduled jobs** and reminders
- **Security Check Up and configuration report**, plus HTTPS, sign-in, per-agent grants, an audit log, alerts and nightly backups
- **Web UI and user manual** for managing everything without editing JSON

### Multiple agents

Run several named agents side by side. Each has its own workspace, models, tools, system prompt, memory and channel bindings, and only the tools you grant it. One agent can be your personal assistant on Telegram while another does security reviews from Slack. See [Creating and editing agents](manual/agents/creating.md).

### API and CLI models

Use cloud APIs (Anthropic, OpenAI, Google, x.ai and others), [OpenRouter](manual/providers/openrouter.md), [Amazon Bedrock](manual/providers/bedrock.md) or [local models](manual/providers/local-models.md) through their APIs, or run [CLI agents](manual/providers/cli-providers.md) such as Claude Code, Codex, Antigravity and Cursor as an agent's model. Each agent has an ordered list of models. When one fails or runs into rate limits, ClawEh falls back to the next and keeps the failed one in cooldown. See [Providers, models and fallback](manual/providers/concepts.md).

### Coding harness tools

Agents on API models get the tools a modern coding harness provides: `file_glob` to find files by name, `file_search_lines` to search code (regular expressions, file filters, context lines, `.gitignore` respected), line-based reading and editing, and `shell_exec` for git, builds and tests. **Allow coding tools** on the Agents page grants the whole set. [Allowed folders](manual/agents/allowed-folders.md) give an agent's file tools a source tree outside its workspace by its real path, read-only when the file permissions say so. See [Tools and skills](manual/agents/tools-and-skills.md).

### Maestro workflow orchestration

[Maestro](manual/maestro.md) gives an agent a project manager's toolkit for work that outgrows one conversation: projects, item lists, task sets run in parallel by worker sub-agents with retries and quality checks, reusable playbooks, and generated reports. Progress is kept on disk, so a long job can be resumed.

### Forums

A [forum](manual/forums/index.md) is a structured discussion among agents that one agent sets up and ClawEh runs to the end: independent opinions from several models, a critique-and-revise cycle for a document, or a review panel for a proposal. Built-in templates cover common setups, and responses can be anonymous so that participants judge the content, not the author.

### MCPFusion hub and configuration-defined MCP servers

[MCPFusion](manual/mcpfusion.md) turns a REST API described in a JSON file into tools, with each agent signing in to the service on its own: Microsoft 365, Google Workspace, Trello, weather services and similar APIs. ClawEh also speaks [MCP](manual/mcp.md) in both directions: it serves its tools to CLI agents and other MCP clients, and connects to external MCP servers whose tools you grant agent by agent.

### Speech and image conversion

[Task models](manual/providers/task-models.md) handle single conversions: transcribing incoming [voice messages](manual/channels/voice.md), turning text into speech, generating images, and describing images for models that cannot see them. You pick the models and their fallback order for each use, and agents can call them too.

### IM integration

Connect agents to [Telegram](manual/channels/telegram.md) (several bots), [Slack](manual/channels/slack.md), [Discord](manual/channels/discord.md), [Matrix](manual/channels/matrix.md), Signal through [SecMsg](manual/channels/secmsg.md), and the built-in [web chat](manual/channels/webui-chat.md). [Routing and bindings](manual/channels/routing.md) decide which agent answers in which chat.

### Devices and the message API

A built-in [device listener](manual/channels/devices.md) lets voice apps such as Claw to Talk or a Folotoy AI Passport talk to your agents. The [message API](manual/channels/message-api.md) lets other applications send a message to an agent over HTTP. And, an OpenClaw-compatible command line supports the Rabbit R1 agent.

### Cognitive memory and context management

Each agent has one conversation shared by every chat that reaches it, and a long-term [memory](manual/agents/memory.md) that distils conversations into de-duplicated facts and recalls the relevant ones for each new message. Summarisation, compaction and the eviction of stale tool output keep long-running conversations within the model's context.

### Sub-agents

An agent can hand a task to a [sub-agent](manual/agents/subagents.md), a temporary copy of itself or of another agent that works on it in the background and reports back. Agents can also send [messages to each other](manual/agents/messaging.md) and wait for the answer.

### Human agents

A [human agent](manual/agents/human-agents.md) lets a person take part as an agent. When an agent asks it something, the question is posted to the person's chat, and their reply is the answer. No model runs for it.

### Classifiers with MCP integration

A [classifier](manual/agents/classifiers.md) sorts the items any of an agent's tools can list, including MCP servers and MCPFusion services such as Microsoft 365 mail, files or a ticket system. It reads each new item, asks a model which category it belongs to, and runs the action set for that category. The model that classifies never has tools.

### Character cards

[Character cards](manual/agents/character-cards.md) give an agent, a sub-agent or a forum participant a character to play, from a library of Character Card V3 files that can be imported from and exported to other tools.

### Scheduled jobs

Agents can set [reminders and recurring jobs](manual/agents/scheduling.md), and handle each one as a message when it fires.

### Security Check Up and configuration report

The [Check Up](manual/admin/checkup.md) page lists the settings that most need attention, and its **Full report** button downloads a PDF of everything the install can do: listeners, providers, channels, each agent's tools and folder access, external services and devices. Neither ever shows a credential. HTTPS and [sign-in](manual/admin/login.md) are on by default, every tool is granted per agent, and ClawEh keeps an [audit log](manual/admin/audit.md), sends [alerts](manual/admin/alerts.md) on failures and makes nightly [backups](manual/admin/backup.md).

### Web UI and user manual

Manage agents, providers, models, channels, MCP connections, memory and settings from the web interface, without editing JSON. The user manual is built in: open **Manual** in the sidebar.

## Documentation

The **user manual** covers installing, providers and models, agents, channels, forums, Maestro, MCP, security and administration, and a configuration and command reference:

- in a running ClawEh, open **Manual** in the WebUI (served at `/manual/`);
- in this repository, the Markdown source is under [`manual/`](manual/index.md).

Changes in each release are listed in [CHANGELOG.md](CHANGELOG.md).

## Development Status

Version 0.6.0 represents a significant evolution toward a more secure business-class product. HTTPS is now enabled by default, and authentication is required to access the web interface. An overview of the more important security-related configuration items is displayed on the **Check Up** page and a detailed PDF report can be downloaded for review. Remaining legacy code from the original fork has also been removed and rewritten.

## Installation

The one-line installer installs ClawEh as a background service, asks for the admin account, and opens the setup wizard:

```bash
curl -fsSL https://raw.githubusercontent.com/PivotLLM/ClawEh/main/claw-online-install.sh | bash
```

Or download an archive from [GitHub Releases](https://github.com/PivotLLM/ClawEh/releases) (Linux and macOS, amd64 and arm64) and run `./claw install` (`sudo ./claw install` for a service that starts at boot). Then open **http://localhost:18790** and sign in.

Other options (a bare binary with `claweh.sh`, an unattended install, a headless or remote host) are in the manual: [Installing ClawEh](manual/getting-started/install.md), then [Quick start](manual/getting-started/quickstart.md).

## File layout

Everything claw keeps is in one data directory, `~/.claw` unless `CLAW_HOME` names another. "You" marks what you may edit by hand; "claw" marks what claw manages and you should leave alone.

| Path | Who edits it | What it is |
|---|---|---|
| `config.json` | You | The configuration, which the web UI also edits and claw reloads when it changes. |
| `credentials.json` | claw | The web UI admin account, changed with `claw admin`. |
| `agents/<id>/` | — | One folder per agent, its workspace (`agents.base_dir` moves them all). |
| `agents/<id>/AGENTS.md`, `SOUL.md`, `USER.md`, `IDENTITY.md`, `MEMORY.md` | You | The agent's instructions, combined into its prompt on every turn. |
| `agents/<id>/files/` | You and the agent | The agent's working area, which it can read and write. |
| `agents/<id>/skills/` | You | Skills for this agent only. |
| `agents/<id>/sessions/` | claw | The agent's conversation history. |
| `agents/<id>/cogmem/` | claw | The agent's cognitive memory. |
| `agents/<id>/state/` | claw | The agent's own state, such as interrupted turns and its message tokens. |
| `agents/<id>/tmp/`, `tasks/` | claw | Scratch space and background task records for the agent. |
| `agents/<id>/forums/` | claw | The agent's forums, which it can read but not change. |
| `agents/<id>/maestro/` | claw | The agent's Maestro projects and playbooks. |
| `common/` | You and the agents | A shared folder agents with access can exchange files through (`agents.common_dir` moves it). |
| `skills/` | You | Shared skills; an agent uses only the ones it is granted. |
| `cards/` | You and the agents you allow | Character cards; an agent uses only the one attached to it. |
| `cli/` | claw | The working directory for CLI providers whose model sets no workspace. |
| `bin/` | You | Your scripts and programs; files here keep their execute bits, which every other folder's files lose at start. |
| `internal/` | claw | claw's own state: service and message tokens, the device pairing database and Fusion's OAuth tokens. |
| `internal/audit.db` | claw | The audit log of tool calls, configuration changes and logins. |
| `internal/claw.pid`, `internal/claw.lock` | claw | Mark the running instance, so a second one refuses to start. |
| `internal/temp/<uuid>/`, `internal/temp_agents.json` | claw | Temporary agents (sub-agent runs): each one's conversation and memory, and the list of those kept across restarts. Deleted when the run ends or after a day idle; not backed up. |
| `fusion/` | You | Fusion's REST-API service definitions, its env file and `fusion.log`. |
| `tls/` | claw | The self-signed HTTPS certificate and key (`gateway.tls.cert_file` uses your own instead). |
| `logs/` | claw | `claw.log`, `error.log`, `alerts.log`, and `dumps/` when diagnostic dumps are on. |
| `media/` | claw | A temporary cache of files sent and received on channels. |
| `cron/` | claw | `jobs.json`, the scheduled jobs. |
| `backup/` | claw | The nightly backup archives. |

## Building from source

Prerequisites:

- [Go](https://go.dev/dl/) 1.27 or later (the version in `go.mod`)
- [Node.js](https://nodejs.org/) 20.19+ or 22.12+, for the WebUI and the manual
- [pnpm](https://pnpm.io/installation) (`npm install -g pnpm`)

Do not install Node.js with `apt`; the packaged version is too old. Use the [NodeSource repository](https://github.com/nodesource/distributions) or [nvm](https://github.com/nvm-sh/nvm).

```bash
git clone https://github.com/PivotLLM/ClawEh.git
cd ClawEh
make install
```

The WebUI (`web/frontend`) and the user manual (`manual/`, VitePress) are built and embedded in the one `claw` binary, which serves the agent runtime, the WebUI, its API and the manual from one process.

| Target | Description |
|---|---|
| `make build` | Build `claw` for the current platform |
| `make install` | Build, then run `claw install` |
| `make test` | The full gate: format check, vet, lint, govulncheck, the Go suite, the frontend checks, the manual build and the MCP integration tests |
| `make manual` | Build the user manual |
| `make check-webui` | The browser regression suite against a running dev instance |
| `make clean` | Remove build artifacts |

See [docs/test.md](docs/test.md) for how the suite is organised. Developer notes on protocols and internals are in [`docs/`](docs/).

### Shared modules

Several parts of ClawEh are standalone Go modules so they can be reused (e.g. by Maestro) and compiled into one binary without import cycles:

- **[`toolspec`](https://github.com/PivotLLM/toolspec)** — the transport-neutral tool contract.
- **[`spawnllm`](https://github.com/PivotLLM/spawnllm)** — the LLM-dispatch core: provider clients and the tool-call loop.
- **[`ctxengine`](https://github.com/PivotLLM/ctxengine)** — the context engine: transcript, archive, compaction and the session tools.
- **[`cogmem`](https://github.com/PivotLLM/cogmem)** — cognitive memory.

Policy (model selection, fallback, cooldown, configuration) remains in ClawEh.

## Why ClawEh exists

ClawEh began as a fork of [PicoClaw](https://github.com/sipeed/picoclaw), whose small, easy-to-deploy Go foundation inspired the project and provided a starting point.

It has since evolved into a separate project with different priorities: a smaller, focused codebase that puts core stability, reliability, security, and maintainability first. Over time, the implementation was progressively rewritten, and the original PicoClaw code has now been completely replaced.

ClawEh remains grateful to PicoClaw for the original idea and inspiration.

## Terms of Use and Compliance
ClawEh supports a wide range of LLM providers. It is your responsibility to ensure that your use of any provider, API, service, or model is consistent with the applicable terms of service, acceptable use policies, contracts, and legal requirements. This includes use-case restrictions, data handling obligations, and any prohibition on accessing non-public or undocumented APIs. We welcome feedback from any LLM providers on this topic.

## Security Considerations

ClawEh is intended to function as a personal or small-team assistant that runs on a computer the operator controls. It is not designed or intended to provide any kind of public service.

The WebUI requires an admin login, HTTPS is on by default, and the **Check Up** page lists the settings that need attention. See [Security and administration](manual/admin/index.md) in the manual.

In general, Claw should be operated as a local, user-controlled tool, not as an internet-facing application.

This application assumes that messages sent to assistants come from an authorized user and are not malicious. While it may provide configuration options to restrict which users on services such as Slack or Telegram are allowed to communicate with an assistant, misconfiguration is possible, and flaws, bypasses, or unexpected behavior may exist. Exposing an assistant through external communication channels can be very convenient, but it also increases risk substantially: if an account is compromised, access controls are configured incorrectly, or a bug or unknown vulnerability is present, unauthorized parties may be able to interact with the assistant and trigger actions or consume paid API resources.

When enabling tools, including connecting MCP servers, users must carefully consider the security and privacy implications. Granting an assistant access to sensitive systems or data can have severe consequences if that assistant is exposed beyond the intended user. For example, giving an assistant access to email and then accidentally allowing anyone on Telegram to interact with it could have catastrophic privacy and security consequences. The same principle applies to file systems, shells, calendars, internal APIs, and any other connected capability.

Users should also understand that data made available to an assistant through connected tools and services may contain malicious or misleading content. Content from email, chat systems, documents, web pages, issue trackers, or other data sources could potentially be interpreted or acted on by the assistant as if it were an instruction. It is the user's responsibility to ensure that they fully understand the risks, that appropriate security controls are in place, and that, where necessary, appropriate testing has been conducted.

ClawEh does not attempt to determine whether your deployment is appropriately secured for your use case. While it is possible to configure external channels so that other individuals can access assistants, doing so is entirely the user's choice and entirely the user's responsibility. You are solely responsible for deciding whether ClawEh is suitable for your application, whether the security posture is acceptable, and for all consequences of how the software is configured and used.

Users should also carefully consider the financial implications of connecting applications to LLM APIs and other metered services. A bug, a bad configuration, or accidental exposure through channels like Telegram or Slack can potentially result in large numbers of unintended requests and, in turn, unexpectedly large bills. This risk becomes especially serious when assistants are connected to paid APIs, tools, or automated workflows.

To reduce the risk of financial surprises, we strongly recommend using prepaid APIs and/or subscription-based CLIs where possible. You should also ensure that appropriate cost monitoring, usage limits, budget alerts, rate limits, and other containment controls are in place. Choosing to run Claw, expose it through external services, and connect it to paid models or sensitive tools is your decision, and you bear full responsibility for the outcome.

## Copyright and license

Copyright (c) 2026 Tenebris Technologies Inc.

This software is licensed under the MIT License. Please see `LICENSE` for details.

## Trademarks

Any trademarks referenced are the property of their respective owners, used for identification only, and do not imply sponsorship, endorsement, or affiliation.

## No Warranty

**(zilch, none, void, nil, null, "", {}, 0x00, 0b00000000, EOF)**

THIS SOFTWARE IS PROVIDED “AS IS,” WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NON-INFRINGEMENT. IN NO EVENT SHALL THE COPYRIGHT HOLDERS OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Made in Canada with internationally sourced components.
