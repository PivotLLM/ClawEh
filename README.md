# ClawEh: Yet another claw - Canadian style

ClawEh began as a fork of [PicoClaw](https://github.com/sipeed/picoclaw). Written in Go, ClawEh it is focused on a minimal footprint, efficient deployment, core stability, reliability, security, and long-term maintainability.

## Why ClawEh exists

PicoClaw originally caught my attention because it is written in Go, my language of choice for building performant systems with efficient development workflows, straightforward deployment, strong cross-platform tooling, and long-term maintainability.

When I began using PicoClaw, I quickly encountered bugs and design issues that affected reliability, maintainability, and day-to-day use. I contributed a number of fixes upstream, but in practice I could not rely on the upstream softare in its state at the time for my own use. Given the volume of incoming changes, continuing to route needed fixes through a large upstream queue no longer seemed practical, so a separate project with a smaller scope, a higher quality bar, and a stronger focus on core stability became the more sustainable path.

This is not intended as criticism of the original authors or their effort. I am grateful for the starting point they provided and for making the project available in Go in the first place. The PicoClaw project is clearly receiving a high volume of contributions and proposed changes, and I appreciate that keeping up with that kind of volume is difficult under any circumstances. This fork simply reflects a different set of needs and priorities: a smaller, more focused codebase with a stronger emphasis on core stability, reliability, security, and maintainability.

ClawEh exists because I desired a small, reliable, performant and secure "Claw" focued on:

- flexible support for both CLI-based agents and direct multi-provider API integrations
- integration with messaging platforms such as Slack, Telegram, and Discord
- effective use of MCP servers
- features such as cron to execute periodic tasks

From a security practitioner’s perspective, expanding AI agents by packing an ever-growing range of capabilities into a single monolithic application is a mistake. The broader and more complex the feature set becomes, the larger the attack surface and the harder it is to secure effectively. If you are looking for a "claw" with everything including the kitchen sink, this isn't it.

## Core features

- Lightweight Go implementation with straightforward deployment
- Multi-agent support with per-agent configuration and workspaces
- Flexible LLM integration through CLI-based agents and direct multi-provider APIs
- Channel integrations and automation capabilities for practical operational use
- Cron-based scheduling for all periodic and time-based task execution — to reduce unnecessary duplication and complexity, all scheduling and periodic execution is consolidated in cron (see [docs/cron.md](docs/cron.md))
- MIT-licensed, with a strong emphasis on openness, reuse, and maintainability

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

Clone the repository and build both binaries:

```bash
git clone https://github.com/PivotLLM/ClawEh.git
cd ClawEh
make install
```

This builds `claw` and `claw-launcher` and installs them to `~/.local/bin`.

**Agent only (no web frontend):** Node.js and pnpm are not required if you skip the launcher:

```bash
make install-agent
```

**Available make targets:**

| Target | Description |
|---|---|
| `make build` | Build both binaries |
| `make install` | Build and install both to `~/.local/bin` |
| `make build-agent` | Build `claw` only |
| `make install-agent` | Build and install `claw` only |
| `make build-launcher` | Build `claw-launcher` only |
| `make install-launcher` | Build and install `claw-launcher` only |
| `make test` | Run tests |
| `make clean` | Remove build artifacts |

## Terms of Use and Compliance
ClawEh supports a wide range of LLM providers. It is your responsibility to ensure that your use of any provider, API, service, or model is consistent with the applicable terms of service, acceptable use policies, contracts, and legal requirements. This includes use-case restrictions, data handling obligations, and any prohibition on accessing non-public or undocumented APIs. We have removed support for some providers where we determined the implementation could not reasonably be used without violating the provider's terms. We welcome feedback from any LLM providers on this topic.

## Assistant behaviour

`session_scope` (in the `session` config block) controls how an agent's memory is divided across users and platforms.

| Mode | Memory per | Description |
|---|---|---|
| `unified` | Agent | One shared memory for the entire agent, across all users, channels, and platforms |
| `per-user` | Person | Each person gets their own private memory. Recognises the same person across platforms if `identity_links` are configured; otherwise each platform ID is a separate person |
| `per-platform` | Person × platform | Each person has a separate memory per platform. Slack and Telegram are independent conversations even for the same person |
| `per-account` | Person × platform × bot | Like `per-platform`, but also separates by bot account. Relevant only when multiple bots on the same platform are routed to the same agent |

The default is `unified`.

**Choosing a mode**

*Personal assistant, or a purpose-built specialist* — use `unified`. This is the right choice in two situations. For a personal assistant: one continuous memory across all your channels, it knows your preferences, remembers your projects, and picks up where you left off regardless of where you reach it. For a purpose-built assistant — if you create an agent named Alice who specialises in security, there is one Alice. Anyone who contacts her, through any channel you have configured, is talking to the same Alice with the same accumulated knowledge and context. She does not have separate memories for different users; she is one coherent assistant.

*Shared assistant for a team or family* — use `per-user`. Each person gets their own private relationship with the assistant — their own context, their own memory, no bleed between users. If the same person might contact the assistant from multiple platforms, configure `identity_links` to tell the system they are the same person (see below).

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

In `unified` mode every conversation adds to the shared memory. If you want the agent to handle a task in isolation — without drawing on prior context and without polluting the main history — ask it to use the `spawn` tool. A spawned subagent runs in a completely separate session, completes its work, and reports back. Nothing from that exchange appears in or affects the main conversation.

**Security: access control**

Every channel has an `allow_from` list. An empty list means **nobody** can connect. Set it to your user IDs to restrict access, or `["*"]` to allow all users.

ClawEh is designed as a personal assistant framework. We strongly advise against allowing untrusted users to access your assistants. In `unified` mode in particular, every person who can reach the assistant is contributing to — and reading from — the same shared memory and context. An untrusted user can see the assistant's full history of what it knows and has been told. Only grant access to people you trust completely, and ensure you fully understand the security implications before opening any channel beyond your own use.

On platforms like Telegram where bots are publicly discoverable by username, this risk is especially acute. Always set `allow_from` explicitly.

## Security Considerations

ClawEh is intended to function as personal assistant that runs on a computer the user controls. It is not designed or intended to provide any kind of public service. The current web interface uses HTTP and has no authentication, so it should be treated as unsafe for exposure to untrusted networks. We strongly recommend running it on `localhost` only, and only when needed.

The web management API has no authentication layer. Any client that can reach the management port can add or modify model configurations (including API keys and endpoints), read session history, and start or stop the gateway process. Access control relies entirely on the listen address (localhost-only by default) and, when running in public mode, the IP allowlist. Do not run with `-public` and an empty `allowed_cidrs` list on any network where untrusted hosts could reach the port.

In general, Claw should be operated as a local, user-controlled tool, not as an internet-facing application.

This application assumes that messages sent to assistants come from an authorized user and are not malicious. While it may provide configuration options to restrict which users on services such as Slack or Telegram are allowed to communicate with an assistant, misconfiguration is possible, and flaws, bypasses, or unexpected behavior may exist. Exposing an assistant through external communication channels can be very convenient, but it also increases risk substantially: if an account is compromised, access controls are configured incorrectly, or a bug or unknown vulnerability is present, unauthorized parties may be able to interact with the assistant and trigger actions or consume paid API resources.

When enabling tools, including connecting MCP servers, users must carefully consider the security and privacy implications. Granting an assistant access to sensitive systems or data can have severe consequences if that assistant is exposed beyond the intended user. For example, giving an assistant access to email and then accidentally allowing anyone on Telegram to interact with it could have catastrophic privacy and security consequences. The same principle applies to file systems, shells, calendars, internal APIs, and any other connected capability.

ClawEh includes an http callback mechanism that is disabled by default. When enabled for an individual agent, this feature provides the assistant with a callback URL that can be used to send the assistant a message. If used, the callback will be sent to the agent through it's standard communication channel. If this feature is used, care must be used to avoid it being used for malicious purposes. 

Users should also understand that data made available to an assistant through connected tools and services may contain malicious or misleading content. Content from email, chat systems, documents, web pages, issue trackers, or other data sources could potentially be interpreted or acted on by the assistant as if it were an instruction. It is the user's responsibility to ensure that they fully understand the risks, that appropriate security controls are in place, and that, where necessary, appropriate testing has been conducted.

ClawEh does not attempt to determine whether your deployment is appropriately secured for your use case. While it is possible to configure external channels so that other individuals can access assistants, doing so is entirely the user's choice and entirely the user's responsibility. You are solely responsible for deciding whether ClawEh is suitable for your application, whether the security posture is acceptable, and for all consequences of how the software is configured and used.

Users should also carefully consider the financial implications of connecting applications to LLM APIs and other metered services. A bug, a bad configuration, or accidental exposure through channels like Telegram or Slack can potentially result in large numbers of unintended requests and, in turn, unexpectedly large bills. This risk becomes especially serious when assistants are connected to paid APIs, tools, or automated workflows.

To reduce the risk of financial surprises, we strongly recommend using prepaid APIs and/or subscription-based CLIs where possible. You should also ensure that appropriate cost monitoring, usage limits, budget alerts, rate limits, and other containment controls are in place. Choosing to run Claw, expose it through external services, and connect it to paid models or sensitive tools is your decision, and you bear full responsibility for the outcome.

## Running as a service (Linux)

`claw.service` and `claw-web.service` in the project root are systemd unit files for running ClawEh as a background service on Linux. Replace `YOUR_USERNAME` with the user account the service should run under, then install with:

```
sudo cp claw.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now claw
```

ClawEh writes logs to `~/.claw/logs/claw.log` and the web console to `~/.claw/logs/claw-launcher.log`. No log redirection is required in the service file.

## Callback endpoint

ClawEh provides an optional per-agent HTTP endpoint that allows external processes — MCP servers, scripts, spawned subprocesses — to deliver messages to an agent without a persistent channel connection. The agent receives the message on its last active channel and responds normally.

```
POST http://localhost:18790/api/reply/{token}
```

Enable it per agent in the config or via the web console under **Agents**:

```json
{
  "id": "alice",
  "callback": {
    "window_minutes": 30,
    "window_count": 2
  }
}
```

When enabled, the current token is injected into the agent's system prompt (marked confidential) so the agent can pass it to subprocesses or MCP servers that need to call back.

> **Security notice:** This endpoint is plain HTTP, bound to `127.0.0.1` only. Do not expose it externally. See [docs/callback.md](docs/callback.md) for full details.

See [docs/callback.md](docs/callback.md) for configuration reference, token rotation, routing behaviour, and troubleshooting.

## MCP server (claw as an MCP host)

ClawEh can expose a subset of its host-side tools to MCP-compatible clients over a Streamable HTTP transport. This is primarily intended for CLI providers (Claude Code, Codex CLI, Gemini CLI) so they can call claw's tools natively instead of printing tool-call JSON in their prose — which historically caused runaway outer loops, since those CLIs are themselves agentic and return a single final answer per invocation.

> **Important:** CLI providers (`claude-cli`, `codex-cli`, `gemini-cli`) no longer receive tool descriptions in their prompt. Each invocation runs as a single agentic turn, and the CLI reaches claw's tools only via MCP. **You must register claw as an MCP server in each CLI you intend to use** — see [Client configuration](#client-configuration) below. Without that step, the CLI will still answer prompts, but it will have no access to claw's filesystem, web, or other host-side tools.

The server auto-starts whenever any enabled model in `model_list` uses a `*-cli` protocol (`claude-cli`, `codex-cli`, `gemini-cli`), since those CLIs depend on MCP for native tool calls. Set `enabled: true` to force it on regardless, or `auto_enable: false` to opt out of the auto-start. Full config shape with defaults:

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

### Client configuration

The server speaks the Streamable HTTP transport at `http://127.0.0.1:5911/mcp`.

No authentication is performed: the listener is bound to loopback only and is intended for local CLI clients. Do not expose it externally.

#### Claude Code

To register claw as an MCP server scoped to the user (all projects):

```bash
claude mcp add --transport http claw --scope user http://127.0.0.1:5911/mcp
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
codex mcp add claw --url http://127.0.0.1:5911/mcp
```

This writes the entry to `~/.codex/config.toml`. You can also edit the file directly:

```toml
[mcp_servers.claw]
url = "http://127.0.0.1:5911/mcp"
```

#### Gemini CLI

[Gemini CLI](https://github.com/google-gemini/gemini-cli) supports MCP servers via the `gemini mcp add` command or by editing `~/.gemini/settings.json` directly.

```bash
gemini mcp add claw http://127.0.0.1:5911/mcp --scope user --transport http
```

Omit `--scope user` to configure claw at the project level instead.

> **Warning:** The `--trust` flag grants Gemini CLI unrestricted access to all MCP tools without prompting for permission. Only use `--trust` in controlled environments where you fully trust the MCP server and its tools.

To grant access to all tools without prompting (use with caution — see warning above):

```bash
gemini mcp add claw http://127.0.0.1:5911/mcp --scope user --transport http --trust
```

Alternatively, add the following to `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "claw": {
      "url": "http://127.0.0.1:5911/mcp",
      "type": "http"
    }
  }
}
```

#### Clients without HTTP transport support

For clients limited to stdio MCP transport (e.g., Claude Desktop), bridge to claw's network-based server using <https://github.com/PivotLLM/MCPRelay>.

### Testing

A `probe`-driven integration test lives at `tests/test_mcpserver.sh`. Start claw with `mcp_host.enabled=true`, then run:

```bash
./test.sh -i          # run unit tests + MCP server integration
# or directly:
./tests/test_mcpserver.sh
```

Override `SERVER_URL`, `ENDPOINT`, or `PROBE_PATH` via a `tests/.env` file if needed.

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

Copyright (c) 2026 PicoClaw contributors  
Copyright (c) 2026 Tenebris Technologies Inc.

This software is licensed under the MIT License. Please see `LICENSE` for details.

## Trademarks

Any trademarks referenced are the property of their respective owners, used for identification only, and do not imply sponsorship, endorsement, or affiliation.

## No Warranty

**(zilch, none, void, nil, null, "", {}, 0x00, 0b00000000, EOF)**

THIS SOFTWARE IS PROVIDED “AS IS,” WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NON-INFRINGEMENT. IN NO EVENT SHALL THE COPYRIGHT HOLDERS OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Made in Canada with internationally sourced components.
