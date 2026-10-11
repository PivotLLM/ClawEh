# Overview

ClawEh is a self-hosted program that runs one or more personal AI assistants on a computer you control.

## What it does

ClawEh runs [agents](../agents/index.md). An agent is an assistant with its own name, models, tools, files and memory.

You reach an agent through [channels](../channels/index.md): the built-in WebUI chat, Telegram, Slack, Discord, Signal, Matrix, and paired devices such as the Rabbit R1 or a voice app.

Each agent uses one or more **models**. A model is reached through a [provider](../providers/index.md): an API such as OpenAI, Anthropic or OpenRouter, or a coding CLI installed on the same machine (Claude Code, Codex, Antigravity, Cursor).

Everything runs in one program, `claw`. It serves the agents, the WebUI and its API from the same process.

## Main ideas

- **One conversation per agent.** Everything that reaches an agent joins its one conversation, whatever the channel. If you want a separate conversation, create a separate agent.
- **Nothing is granted by default.** A new agent has no tools and no skills. You grant them on the **Agents** page ([Tools and skills](../agents/tools-and-skills.md)). Shell commands are a separate switch, **Allow shell commands**, off by default.
- **Memory that persists.** Each agent can keep a cognitive memory that is updated in the background from its conversation and recalled when relevant ([Conversation and memory](../agents/memory.md)).
- **Fallback between models.** An agent lists its models in order. When one fails, ClawEh tries the next and pauses the failing one for a while ([Providers, models and fallback](../providers/concepts.md)).
- **One configuration file.** All settings live in `config.json` in the data directory. The WebUI edits the same file, and ClawEh reloads it when it changes ([Configuration file](../reference/configuration.md)).
- **Login required.** The WebUI and its API require the admin account, also on the same machine ([Signing in](../admin/login.md)).

The [Glossary](../reference/glossary.md) explains these and other terms.

## What it needs

- Linux or macOS on amd64 or arm64. Linux on 386 is also built. Windows is not supported.
- At least one model: an API key for a provider, a local model server such as Ollama, or a supported CLI installed and signed in.
- A web browser to reach the WebUI.

## Where its files are

ClawEh keeps everything in one data directory: `~/.claw`, unless the `CLAW_HOME` environment variable names another. The main files are:

| Path | What it is |
|---|---|
| `config.json` | The configuration. |
| `credentials.json` | The WebUI admin account. |
| `agents/<id>/` | One folder per agent: its prompt files, its `files/` working area, conversation and memory. |
| `skills/` | Skills shared by all agents. |
| `logs/` | `claw.log`, `error.log` and `alerts.log`. |
| `backup/` | The nightly backup archives ([Backup and restore](../admin/backup.md)). |

## Next step

[Install ClawEh](./install.md).
