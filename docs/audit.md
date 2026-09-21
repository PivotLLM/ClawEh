# Audit report

The Audit page in the WebUI (after Services in the left menu) opens a PDF that
describes what this ClawEh instance is able to do: its footprint. It is meant
for the operator and for a security professional assessing the install. The
emphasis is inventory: the report says what is configured and what that lets
Claw reach, and leaves the judgement to the reader.

The report is built from the configuration as loaded plus facts about the
running process (user, group, host, binary). It does not report live state such
as which channels are connected right now, because the question it answers is
"what can this Claw do", which the configuration decides.

Endpoint: `GET /api/audit/pdf`, served inline (the browser renders it) and
never cached. It sits behind the same access controls as the rest of `/api/`.

## What it never contains

Secret values. API keys, channel tokens, the WebUI and device tokens, MCP
headers and environment values appear only as "set" or "not set", and
environment variables by name. The package has a test that renders a
configuration seeded with distinctive fake secrets and asserts none of them
appear in the output.

## Sections

- **Identity**: product, version with build metadata, commit, build time, Go
  toolchain, OS and architecture, hostname, executable, config path, data
  directory, the user and group the process runs as, and when the report was
  generated.
- **Summary**: one table of what Claw can access, by area: files, shell,
  outbound network, inbound listeners, messaging channels, devices, external
  execution. Each cell names the agents or services concerned. The Files row
  is the one to read first: it says whether agents are confined to their
  workspaces and mounts, or whether one of them can read anything the named
  user can read.
- **Network**: every listener (WebUI/API/MCP host, device gateway, LINE
  webhook) with its bind address, whether it is reachable from other hosts,
  and its allowlist; proxies.
- **Providers and models**: API providers grouped by protocol with base URL,
  key set or not, and their models as `alias → model id` with the flags that
  change capability; CLI providers with the exact command line each model is
  launched with, its working directory and environment variable names. A CLI
  provider is its own program with its own configuration on the host, so
  ClawEh's file sandbox does not govern what it does on its own behalf.
- **Credentials and tokens**: counts of service, integration and device
  tokens per agent, and which credential fields are set on each channel.
- **Channels**: each enabled channel, its identity, who may send to it
  ("any sender" is called out), and which agents answer on it.
- **Agents**: one block per agent: models, memory, sub-agents, suites, the
  internal tools granted with the sensitive ones marked, MCP access and which
  servers it resolves to, and a folder table with one row per path, resolved,
  marked `[read]` or `[write]`. When `restrict_to_workspace` is off, a
  highlighted row says the agent can reach anything the process user can.
- **External services**: MCP servers with transport, command or URL and
  environment names; installed skills and registries; web search providers.
- **Devices**: the device gateway, paired and pending devices.
- **Data**: logs and what they contain (message-content logging and dumps are
  called out), session archives, memory and their retention, media store,
  backups.
- **Scheduled activity**: cron jobs and Maestro runner limits.

## Reading the folder table

Paths are shown resolved, with the configured form in brackets when it
differs, so a mount that is a symlink shows where it really points. Patterns
from `tools.allow_read_paths` and `tools.allow_write_paths` are printed as
patterns. A path that does not exist is printed as configured and noted.
