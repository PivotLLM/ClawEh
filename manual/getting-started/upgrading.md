# Upgrading

This page explains how to upgrade ClawEh and what to check after upgrading to 0.6.0.

## Upgrade with `claw upgrade`

1. Check whether a new release is available:

   ```bash
   claw upgrade --check
   ```

2. Upgrade:

   ```bash
   claw upgrade
   ```

   For a system service, run `sudo claw upgrade`.

`claw upgrade` downloads the release for your platform, verifies its signature and checksum, replaces the binary, and restarts the service if it is running. A release without a valid signature is refused.

Useful options:

- `--version <tag>` installs a specific release instead of the latest.
- `--force` reinstalls even if you already run that version.
- `-y` skips the confirmation question.

`claw update` is the same command.

## Refresh the service file

Some releases change the service file that `claw install` writes. `claw upgrade` does not rewrite it. After upgrading, run `claw install` again with the same privilege as the first time (`sudo` for a system service). It keeps your data and your admin account.

## Before a large upgrade

Take a backup first:

```bash
claw backup
```

See [Backup and restore](../admin/backup.md).

## Upgrading to 0.6.0

0.6.0 changes several defaults. Most installs need some of the steps below. The full list is in `CHANGELOG.md`.

### You must sign in

The WebUI and its API now require the admin account, also from the same machine. If you have none, the WebUI shows "No admin account. On the server run: claw admin". Run `claw admin` on the server.

### HTTPS is on

ClawEh also serves HTTPS on port 18443, on every interface, with a self-signed certificate. Other machines are still refused until their network is allowed. To keep the old footprint, set **Listen on** under **HTTPS** to **Localhost only** or **Off** on the **Network** page. See [HTTPS and certificates](../admin/https.md).

ClawEh now answers only to its own host names. If you reach it by another name, such as a reverse proxy name, set **Hostname / external URL** on the **Network** page.

### Agents have only the tools and skills they list

An agent with no tools list now has no tools, and one with no skills list has no skills. Before, it got the default set.

For each agent, on the **Agents** page:

1. Under **Tools**, click **Default tools** and untick what the agent should not have.
2. Under **Skills**, click **All skills** or pick the skills.

See [Tools and skills](../agents/tools-and-skills.md).

**Check Up** lists every agent that has no tools.

### Shell commands are a per-agent switch

`tools.exec.allow_remote` and the install-wide shell setting are gone. Turn on **Allow shell commands** on the card of each agent that should run shell commands. It is off by default. See [Tools and skills](../agents/tools-and-skills.md#shell-commands).

### CLI providers no longer bypass their permission prompts

A CLI provider (Claude Code, Codex, Antigravity, Cursor) now runs under its own permission settings. If its agents must run tools without asking, tick **Allow CLI to bypass restrictions** for that CLI on the **Providers** page. See [CLI providers](../providers/cli-providers.md#allow-cli-to-bypass-restrictions).

### Fusion services are granted per agent

The agent's Fusion switch no longer grants every Fusion service. Tick the services under **Fusion services** on the agent's card. See [MCPFusion](../mcpfusion.md#give-an-agent-access).

### Agent ids must be valid

An agent id may use only letters, digits, `-` and `_`, starts with a letter or digit, and is at most 64 characters. Ids are case-insensitive: `Bob` and `bob` are the same agent. A `config.json` with an invalid id does not start; the error names the id and suggests a fix. See [Agent ids](../agents/creating.md#agent-ids) and [Troubleshooting](../reference/troubleshooting.md#the-service-does-not-start).

### One conversation per agent

Session modes are removed. Every message to an agent joins its one conversation. For a separate conversation, create a separate agent. See [Conversation and memory](../agents/memory.md).

### The data directory layout changed

ClawEh's own files moved into `internal/`, shared skills into `skills/`, and the common folder into `common/`. There is no automatic migration. The old `agents/default` folder is no longer used; move anything you want to keep by hand.

### The device listener needs a secret

An enabled device listener on a network address now needs a token and approval of each device. Use **Pair a device** on the **Devices** page to set a token. See [Devices](../channels/devices.md).

## After any upgrade

1. Run `claw version` to confirm the new version.
2. Open [Check Up](../admin/checkup.md) and act on any marked rows.
3. Check `logs/error.log` in the data directory (see [Logs](../admin/logs.md)) for warnings such as "unknown config key", which name settings that no longer apply.
