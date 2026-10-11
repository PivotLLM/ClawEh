# MCP: ClawEh as server and client

ClawEh speaks the Model Context Protocol (MCP) in both directions: it serves its tools to CLI-based models and other MCP clients, and it connects to external MCP servers whose tools you grant to agents.

## ClawEh as an MCP server

### Why it exists

Agents on a CLI provider (Claude Code, Codex CLI, Antigravity, Cursor CLI)
run the vendor's CLI as a separate program. That program cannot call
ClawEh's tools directly, so it calls them over MCP from ClawEh's **MCP
host**. Agents on an HTTP provider (Anthropic, OpenAI and similar APIs)
call tools inside ClawEh and do not need it.

### Turn it on

The MCP host starts by itself when any enabled model uses a CLI provider.
To check or change that:

1. Open **MCP** → **Config**.
2. Under **Enable**:
   - **Enabled**: always start the MCP host.
   - **Auto-enable** (on by default): start it when a CLI provider is
     configured, even with **Enabled** off.
3. Under **Transport**:
   - **Listen Address**: default `127.0.0.1:5911`. It must be a loopback
     address; the MCP host is for programs on this machine.
   - **Endpoint Path**: default `/mcp`.

The MCP host has two endpoints on that address:

| Endpoint | Sign-in | Used by |
|---|---|---|
| `/internal` | A session token passed with every tool call | ClawEh's own CLI providers. One CLI install can act for several agents, each call naming its agent by its token. |
| `/mcp` (the **Endpoint Path**) | `Authorization: Bearer <token>`, one agent per connection | Other MCP clients, such as your own scripts or a desktop assistant. |

### Register ClawEh in each CLI

Each CLI you use as a provider must know where ClawEh's MCP host is.
Without this the CLI still answers, but has none of ClawEh's tools.

- **All at once:** run `./set-mcp.sh` from the ClawEh source folder. It
  registers ClawEh in Antigravity, Codex CLI and Claude Code, whichever are
  installed. If you changed the port, run
  `CLAW_MCP_URL=http://127.0.0.1:<port>/internal ./set-mcp.sh`.
- **Claude Code:**
  `claude mcp add --transport http claw --scope user http://127.0.0.1:5911/internal`
- **Codex CLI:** `codex mcp add claw --url http://127.0.0.1:5911/internal`
- **Antigravity:** `agy mcp add claw http://127.0.0.1:5911/internal --type http`
- **Cursor CLI:** add `"claw": {"url": "http://127.0.0.1:5911/internal"}`
  under `mcpServers` in `~/.cursor/mcp.json`.

Run these as the user the ClawEh service runs as.

### Session tokens (CLI providers)

ClawEh gives each conversation a session token, `SST` followed by 64
characters, and places it in the agent's prompt with an instruction to pass
it on every ClawEh tool call. You never handle it.

- The token tells ClawEh which agent and conversation the call belongs to,
  and the call gets that agent's tools only.
- It changes as the session is used, is revoked after 2 hours idle, and
  does not survive a restart.
- The agent is told never to show it or write it to a file; ClawEh also
  masks `SST...` values in its logs.

### Service tokens

A service token is a long-lived credential for one agent, for an MCP client
outside ClawEh, such as a program that drives an agent's Maestro projects.

```
claw token issue  alice   # create (or replace) Alice's token and print it once
claw token rotate alice   # same as issue
claw token revoke alice   # remove it
claw token list           # agents that have a token (tokens are not shown)
```

- Use it as `Authorization: Bearer <token>` on `/mcp`, or as the session
  token on `/internal`.
- It is stored only as a hash in `<CLAW_HOME>/internal/service-tokens.json`;
  a lost token cannot be shown again, only replaced.
- A running ClawEh picks up a change within a few seconds; no restart.
- It acts in the agent's **main conversation**, with the agent's tools and
  memory. It is as powerful as the agent itself. For an integration that
  must not see Alice's conversations or memory, create a separate agent for
  it and issue that agent's token.
- Replies meant for a person are dropped; the client gets only the tool
  results.

### Which tools a client sees

Every call is checked against the agent's own permissions: its tool list,
**Denied tools**, its suites (**Maestro**, **Fusion (REST-API tools)**,
**Allow forum**, cognitive memory) and its MCP access. The MCP host never
gives an agent a tool it would not have in a normal chat.

- On `/mcp`, the tool list shows only the tools the token's agent may call.
- On `/internal`, the list shows the tools of all agents, because the agent
  is known only per call; each call is still checked.

Two optional filters on **MCP** → **Config** narrow what each endpoint
advertises, on top of the agents' permissions:

- **Internal MCP tool visibility** for `/internal`;
- **External MCP tool visibility** for `/mcp`.

Each takes one entry per row: an exact tool name or a prefix (`file`,
`session_info`, `wxca`), or `*` for everything. With no entries, every tool
is exposed. The preview below each list shows which tools match. The filters
never grant anything an agent lacks.

## External MCP servers

ClawEh can connect to other MCP servers (a GitHub server, a browser
automation server, your own) and offer their tools to your agents. Agents on
HTTP providers get the tools directly; agents on CLI providers get them
through ClawEh's MCP host, so no per-CLI setup is needed.

### Add a server

1. Open **MCP** → **Servers**.
2. Click **Add server**.
3. Fill in:
   - **Name**: a short name such as `github`. The server's tools are named
     after it, and it is the name you grant to agents.
   - **Type**: `http` for a server reached by URL, or `stdio` for a program
     ClawEh starts.
   - For `http`: **URL**, and any **Headers** (one per line,
     `Header: value`), such as an authorization header.
   - For `stdio`: **Command**, **Arguments** (one per line),
     **Environment** (one `KEY=VALUE` per line) and optionally an
     **Env file**. A relative env file path is read from the data directory.
   - **Enabled**.
4. Changes are saved automatically. Click **Apply now** to connect at once
   instead of at the next automatic reload.

Each server shows its state: **Connected**, **Reconnecting…**,
**Cooldown**, **Disconnected** or **Disabled**. **Reconnect** connects it
again immediately, also during a cooldown after failures.

A program started over `stdio` gets a limited environment, not the whole
environment of the ClawEh service; pass what it needs under **Environment**
or in an env file.

### Grant a server to an agent

A server's tools are available only to agents granted access.

1. Open the **Agents** page.
2. In the agent's card, **Tools** section, under **MCP access**, tick the
   server.

In `config.json` this is the agent's `mcp_tools` list, which it shares with
[Fusion services](mcpfusion.md#give-an-agent-access):

```json
{
  "id": "bob",
  "mcp_tools": ["github", "playwright_browser_navigate"]
}
```

An entry admits every tool of the server whose name starts with it:
`github` grants all of the github server's tools,
`playwright_browser_navigate` only that one. An empty or missing list
grants nothing. To keep one tool away from an agent that has the server,
list it under **Denied tools** (for example `playwright_browser_run_code_unsafe`).

### Keeping the tool list current

ClawEh refreshes a server's tool list, without a restart, when:

- the server announces a change;
- the regular check of each server (**Liveness probe interval (seconds)**,
  every 60 seconds by default) finds a different list;
- the server is reconnected (after a dropped connection, or with
  **Reconnect**).

Renamed or removed tools disappear from the agents rather than lingering.
A CLI that listed ClawEh's tools at the start of its session sees such a
change only when it next connects.

### Failures

A server that keeps failing is retried after **Reconnect cooldown
(seconds)** (30 by default), doubling with each failure up to 10 minutes. A
tool call with no deadline of its own is stopped after **Call timeout
backstop (seconds)** (300 by default). These settings are under **Client
Resilience** on **MCP** → **Config**.

## See also

- [External services through Fusion](mcpfusion.md)
- [Maestro](maestro.md)
