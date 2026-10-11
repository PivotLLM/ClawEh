# External services through Fusion

Fusion turns a REST API described in a JSON file into tools your agents can call, with each agent signing in to the service on its own.

ClawEh includes the MCPFusion engine. You describe a service (its address,
how it signs in, and its endpoints) in a file; every endpoint becomes a tool.
Weather services, Microsoft 365, Google Workspace, Trello and similar APIs
are typical.

## Add a service

Services are defined by files, not in the WebUI.

1. Get a service definition: a JSON file with a `services` object. The
   MCPFusion project publishes ready-made definitions (for example
   Microsoft 365, Google Workspace, weather services) in its `configs/`
   folder, and its configuration guide describes the format.
2. Copy the file into the `fusion/` folder of the data directory
   (`<CLAW_HOME>/fusion/`, by default `~/.claw/fusion/`). Every `*.json` file
   there is loaded.
3. Put the secrets the definition refers to (written as `${NAME}` in the
   file, such as a client ID or an API key) in `<CLAW_HOME>/fusion/env`, one
   `NAME=value` per line. Keep that file readable by the service user only.
4. Restart ClawEh. Changes in the `fusion/` folder are read only at start.

The service's name is its key in the file's `services` object, such as
`microsoft365` or `wxca`. Its tools are named after it:
`microsoft365_calendar_read_summary`, `wxca_city_current`.

The [Check Up](admin/checkup.md) report lists the `fusion/` folder and the token store under
Data.

## How a service signs in

The definition says how ClawEh authenticates to the service:

| Type in the file | Who signs in | How |
|---|---|---|
| `bearer`, `api_key`, `basic` | Nobody | ClawEh uses the key or password from `fusion/env`. The same credential serves every agent. |
| `oauth2_device` | Each agent's user | A code shown in the chat, entered on the provider's sign-in page. |
| `oauth2_external` | Each agent's user | The `claw-auth` helper on a computer with a web browser. Used for Google. |
| `user_credentials` | Each agent's user | The `claw-auth` helper asks for the values (such as a personal API key). |

Sign-ins belong to the **agent**: Alice's Microsoft 365 sign-in is used only
by Alice. To reach two accounts, use two agents.

## Give an agent access

An agent needs two things, both on the **Agents** page (see
[Tools and skills](agents/tools-and-skills.md#grant-tools)):

1. On the agent's card, turn on **Fusion (REST-API tools)** ("Lets this agent
   use the Fusion services ticked under Tools.").
2. In the card's **Tools** section, under **Fusion services**, tick each
   service the agent may use.

Changes are saved automatically. The switch alone grants nothing: an agent
with Fusion on and no service ticked has no Fusion tools, and Check Up marks
it ("Fusion on with no service").

In `config.json` the same grant is the `fusion` switch plus the service
names in the agent's `mcp_tools` list (shared with
[external MCP servers](mcp.md#external-mcp-servers)):

```json
{
  "id": "alice",
  "fusion": true,
  "mcp_tools": ["wxca", "microsoft365_calendar"]
}
```

An entry admits every tool whose name starts with it, so
`microsoft365_calendar` grants only the calendar tools of the
`microsoft365` service. To take away single tools, such as
`microsoft365_calendar_event_delete`, list them under **Denied tools**.

## Signing in

The agent handles sign-in in the chat. You act on what it relays.

### With a code (device sign-in)

1. Ask the agent to use the service ("what is on my calendar today?").
2. The first time, the agent relays a message like:

   ```
   You need to authenticate to continue. Please:
   1. Visit: https://microsoft.com/devicelogin
   2. Enter code: ABCD-EFGH
   ```

3. Open the address, enter the code and sign in with the account the agent
   should use.
4. Ask the agent to try again.

### With the claw-auth helper

1. Ask the agent to use the service. When it is not signed in, it gives you a
   command such as:

   ```
   claw-auth eyJ1IjoiaHR0cHM6Ly9jbGF3LmV4YW1wbGU...
   ```

2. Within 15 minutes, run that command on a computer with a web browser,
   and sign in when the browser opens (or type the values it asks for).
3. Ask the agent to try again.

The computer running `claw-auth` must reach ClawEh's web address: the
**Hostname / external URL** on the **Network** page when set, otherwise the
HTTPS address ClawEh derives (for example `https://<host name>:18443`). A
computer other than the ClawEh machine must also be in **Allowed networks**
on the same page (see [Network access](admin/network.md)). The sign-in itself does not need the WebUI login.

`claw-auth` is included in every release download, next to `claw`. If you
build ClawEh from source, `make build` builds it too (or `make claw-auth` on
its own); the binary is written to `build/claw-auth`.

To sign in again to a service that uses `claw-auth`, for example with a
different account, ask the agent to run the service's sign-in setup again.
That discards the old sign-in and gives you a new command.

## Tokens

- Sign-in tokens are kept in `<CLAW_HOME>/internal/fusion-tokens.db`, one
  set per agent and service, and refreshed automatically.
- The nightly [backup](admin/backup.md) includes them.
- If the token store cannot be opened, every Fusion tool is disabled and
  the **Fusion token store unavailable** [alert](admin/alerts.md) is raised; restart ClawEh once
  the file is readable.
- Sign-ins are kept under the agent's ID. An agent whose ID changes must
  sign in again.

## What the agent sees

- One tool per endpoint, named `<service>_<endpoint>`, with the descriptions
  from the definition.
- For a service that uses `claw-auth`, a `<service>_auth_setup` tool that
  produces the `claw-auth` command.
- Tools that delete data or cannot be undone (all `DELETE` endpoints, and
  any the definition marks as destructive) are listed but refuse to run
  unless `fusion/env` contains `MCP_FUSION_ALLOW_DESTRUCTIVE=true`. The
  agent can tell you the capability exists.
- A clone or [sub-agent](agents/subagents.md) of Alice uses Alice's sign-ins.
- Agents on a CLI provider reach the same tools through ClawEh's MCP host
  (see [ClawEh as an MCP server](mcp.md)).

When progressive tool discovery is on (the **MCP** → **Config** page),
Fusion tools are hidden from the model until it searches for them.
