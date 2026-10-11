# ClawEh: Yet another claw - Canadian style

**ClawEh is a small, fast, self-hosted runtime for personal AI assistants.**

Written in Go, it can run one or more agents, each with its own workspace, tools, and persistent memory. Agents can connect to Telegram, Slack, Discord, Matrix, Signal (via an additional daemon), the built-in web interface, and external devices such as the Rabbit R1 and compatible voice apps.

Although the conversation context can be reset at any time, ClawEh is designed primarily for long-running assistants that maintain continuity over time. Its development emphasizes efficient context management, practical persistent memory, security, and a stable, dependable core.

## Documentation

The **user manual** covers installing, providers and models, agents, channels, forums, Maestro, MCP, security and administration, and a configuration and command reference:

- in a running ClawEh, open **Manual** in the WebUI (served at `/manual/`);
- in this repository, the Markdown source is under [`manual/`](manual/index.md).

A quickstart tutorial is at https://github.com/PivotLLM/Tutorials/blob/main/docs/claweh-quickstart.md.

Changes in each release are listed in [CHANGELOG.md](CHANGELOG.md).

## Development Status

Version 0.6.0 represents a significant evolution toward a more secure business-class product. HTTPS is now enabled by default, and authentication is required to access the web interface. An overview of the more important security-related configuration items is displayed on the **Check Up** page and a detailed PDF report can be downloaded for review. Remaining legacy code from the original fork has also been removed and rewritten.

## Features

- **Multi-agent architecture** — Run multiple named agents, each with its own workspace, models, tools, system prompt, memory, and channel bindings.
- **Self-hosted** — Workspace sandboxing, per-agent tool allowlists, and loopback-bound services, delivered as MIT-licensed Go software that you run on your own infrastructure.
- **Strong security posture** — Only essential features are enabled by default, with fine-grained access controls for tools, files, agents, and external services.
- **Broad LLM support** — Connect to OpenRouter, Anthropic, OpenAI, Google Gemini, AWS, x.ai, and others, or use CLI agents such as Claude Code, Codex, Antigravity, and Cursor CLI. Configurable fallback chains and cooldowns improve availability.
- **Messaging channels** — Connect agents to Telegram, Slack, Discord, Matrix, Signal, and the built-in web interface, with configurable per-agent routing.
- **Cognitive memory** — Each agent can maintain persistent memory that updates in the background, distilling conversations into structured, de-duplicated facts and automatically recalling relevant information for future prompts.
- **Smart context management** — Automatic summarization and compaction, combined with per-turn eviction of stale tool output, keep long-running conversations responsive and within model context limits.
- **External voice/hardware devices** — A built-in device listener speaks the OpenClaw Gateway WebSocket protocol, so hardware and voice clients can pair and talk to your agents. Tested with the **Rabbit R1** and the **"Claw to Talk" voice app**.
- **Forums** — Structured discussions among agents, run by ClawEh from a template: independent opinions, debate, and a final report.
- **Maestro built-in** — Orchestrate complex, multi-step work using projects, playbooks, and resumable task lists.
- **MCP server and client** — ClawEh provides its tools directly to API-based LLMs and exposes them through MCP to CLI agents. It can also connect to upstream MCP servers over stdio or HTTP, with granular control over which tools each agent may use.
- **Files, mounts and scheduled jobs** — Sandboxed file tools, external folders mounted per agent, and reminders and recurring jobs.
- **Message API** — Named tokens let other applications send a message to a chosen agent.
- **Web UI** — Manage agents, providers, channels, MCP connections, memory, and configuration without editing JSON.

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
| `cli/` | claw | The working directory for CLI providers whose model sets no workspace. |
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
make build             # build/claw, with the WebUI and the manual embedded
./build/claw install   # install it as a service
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

None of them imports ClawEh; policy (model selection, fallback, cooldown, configuration) stays in ClawEh.

## Why ClawEh exists

ClawEh began as a fork of [PicoClaw](https://github.com/sipeed/picoclaw), whose small, easy-to-deploy Go foundation provided the inspiration and starting point for the project.

It has since evolved into a separate project with different priorities: a smaller, focused codebase that puts core stability, reliability, security, and maintainability first. Over time, the implementation was progressively rewritten, and the original PicoClaw code has now been completely replaced.

ClawEh remains grateful to PicoClaw for the original idea and inspiration.

## Terms of Use and Compliance
ClawEh supports a wide range of LLM providers. It is your responsibility to ensure that your use of any provider, API, service, or model is consistent with the applicable terms of service, acceptable use policies, contracts, and legal requirements. This includes use-case restrictions, data handling obligations, and any prohibition on accessing non-public or undocumented APIs. We have removed support for some providers where we determined the implementation could not reasonably be used without violating the provider's terms. We welcome feedback from any LLM providers on this topic.

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
