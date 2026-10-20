# Logs

ClawEh writes its own log files under the data directory, and the **Logs** page shows the latest lines in the browser.

## Where the logs are

All in `<CLAW_HOME>/logs/` (by default `~/.claw/logs/`; on a system install `/opt/claw/logs/`):

| File | What it holds |
|---|---|
| `claw.log` | The main log: everything at or above the configured level. |
| `error.log` | Warnings and errors only, to scan for problems quickly. |
| `alerts.log` | Every [alert](./alerts.md). |
| `YYYYMMDD-claw.log`, `YYYYMMDD-error.log` | Earlier days. |
| `dumps/` | Diagnostic dumps, when turned on. |

Log files are readable only by the service user.

## Daily rotation

At local midnight `claw.log` and `error.log` are renamed with the date and new files are started. If ClawEh was not running at midnight, this happens at its next start.

Earlier days are kept for 30 days by default. To change that:

1. Open **System** in the **Services** group.
2. In **Runtime**, set **Log retention (days)**. `0` keeps logs forever.
3. The change applies at the next start of ClawEh.

If rotation fails, the "Log rotation failed" [alert](./alerts.md) is raised.

## The Logs page

1. Open **Logs** in the **Services** group.
2. The **Log** selector chooses **ClawEh log** (`claw.log`) or **Alerts** (`alerts.log`).
3. **Show** sets how many of the latest lines to display (100 to 2000).
4. Click **Refresh** to fetch new lines; the page does not update by itself.

## Log levels

The level is set in `config.json` (see [Configuration file](../reference/configuration.md)), under `logging.level`: `debug`, `info` (default), `warn` or `error`. A restart applies it.

```json
"logging": { "level": "info" }
```

Use `debug` only while looking into a problem; it is much more verbose.

## Logging options

```json
"logging": {
  "file": true,
  "console": true,
  "level": "info",
  "json": false,
  "retention_days": 30,
  "log_message_content": false,
  "dump_refusals": false,
  "dump_all": false,
  "dump_failed_compressions": false
}
```

| Option | Default | What it does |
|---|---|---|
| `file` | `true` | Write `claw.log` and `error.log`. |
| `console` | `true` | Also write to the console. |
| `level` | `info` | Lowest level written. |
| `json` | `false` | Write JSON lines instead of plain text. |
| `retention_days` | `30` | Days of earlier logs to keep; `0` keeps them forever. |
| `log_message_content` | `false` | Include message text and model requests and replies. |
| `dump_refusals` | `false` | Write a dump when a model refuses. |
| `dump_all` | `false` | Write a dump of every model call. |
| `dump_failed_compressions` | `false` | Write a dump when summarizing a long conversation fails. |

`log_message_content`, `dump_all` and `dump_refusals` write message content to disk. [Check Up](./checkup.md) marks the **Message content in logs** row while any of them is on.

Each option can also be set with an environment variable in the service, such as `CLAW_LOGGING_LEVEL` or `CLAW_LOGGING_RETENTION_DAYS`.

## Follow one turn

Each agent turn has a short `turn_id` on its log lines and on its entries in the [audit log](./audit.md). Search `claw.log` for it to see the whole turn:

```
grep 1a2b3c4d ~/.claw/logs/claw.log
```

## Startup permission checks

The data directory holds keys and tokens, so at every start ClawEh checks it before using anything in it:

- The data directory and every folder in it are set to owner-only (`700`), or to the modes set under `security.permissions` (see [Letting another user read logs or agents](#letting-another-user-read-logs-or-agents)).
- Files holding secrets (credentials, tokens, keys, databases) are set to owner-only (`600`).
- Each change is logged as a warning naming the file.

The check only removes access. It never adds any.

It does not change `config.json`. If other users can read it, ClawEh refuses to start with a message like:

```
<CLAW_HOME>/config.json is readable by other users (mode 0644); it holds credentials, so refusing to start. Fix with: chmod 600 <CLAW_HOME>/config.json
```

To fix it:

1. Run the `chmod` the message names, as the user ClawEh runs as. For example:

   ```
   chmod 600 ~/.claw/config.json
   ```

2. Start ClawEh again.

The message appears in the service's output (`systemctl status claw`, or `journalctl -u claw`), or in the terminal when ClawEh was started by hand.

The same applies to the admin credentials file: if other users can read it, ClawEh refuses the account and logs the `chmod 600` to run. See [Signing in](./login.md#no-account-yet).

[Check Up](./checkup.md) shows the **Data directory permissions** row for files that have become readable since the last start.

### Letting another user read logs or agents

`security.permissions` in `config.json` sets the modes of the folders and files ClawEh creates in the data directory. For example, to let a second user in ClawEh's group read the logs and the agents' folders:

```json
"security": {
  "permissions": {
    "dir_mode": "0750",
    "logs":   { "dir_mode": "0750", "file_mode": "0640" },
    "agents": { "dir_mode": "0750", "file_mode": "0640" }
  }
}
```

- `dir_mode` and `file_mode` at the top are the default for everything, `0700` and `0600` when left out. The top-level `dir_mode` also sets the data directory itself, so the group can only reach `logs` and `agents` when it grants the group access too.
- Each area can set its own pair: `logs`, `agents` (the agents folder and any agent's own workspace), `media`, `common` and `backup`. A key left out uses the top-level value. An area is matched by the paths ClawEh is configured with, not by another spelling through a symbolic link.
- A mode is 3 or 4 octal digits. Folders must give the owner `7` and files `6`, and other users may never write. A wrong value stops ClawEh from starting, with a message naming the key.
- Secrets stay `600` whatever is set: `config.json`, the credentials file, TLS keys, everything in `internal/`, and any file named for a token or secret. The agents' session archives and memory databases follow the agents modes.
- `config.json` is read before these modes apply, so its own check is unchanged.

The modes apply to what ClawEh creates from the next start. The startup check only removes access, so existing folders and files keep their modes, with one exception: the agents' session archives and memory databases take the agents modes the next time they are opened, existing ones included. `logs/`, `claw.log` and `error.log` are created before the config is read, so on a first start they are `0700`/`0600` until you run the commands below. After allowing more, give the existing folders and files access once, as the user ClawEh runs as. For the example above:

```
chmod g+rX ~/.claw
chmod -R g+rX ~/.claw/logs ~/.claw/agents
```

Any secret this makes readable is set back to `600` at the next start.
