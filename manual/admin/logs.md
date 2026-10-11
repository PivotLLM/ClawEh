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

The data directory holds keys and tokens, so at every start ClawEh makes it private before reading anything:

- The data directory and every folder in it are set to owner-only (`700`).
- Files holding secrets (credentials, tokens, keys, databases) are set to owner-only (`600`).
- Each change is logged as a warning naming the file.

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
