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

The data directory holds keys and tokens, so at every start ClawEh sets every folder and file in it to the mode `security.permissions` gives it (see [File permissions](#file-permissions)):

- The data directory itself, and every folder and file in it, are set to exactly their area's mode. This adds access as well as removing it.
- Files outside `bin/` lose execute bits their mode does not have. Keep scripts and programs in `bin/`.
- A data directory that is itself a symbolic link is followed to its folder. Inside it, symbolic links are never followed or changed, a file with more than one hard link is left alone with a warning, and the running `claw` program keeps its mode wherever it is.
- Each change is logged at INFO, naming the file and the old and new modes.

`config.json` is never loosened or tightened past its mode silently. If it allows more than `root.file_mode`, ClawEh refuses to start with a message like:

```
<CLAW_HOME>/config.json has mode 0644, more than security.permissions.root.file_mode allows (0600); it holds credentials, so refusing to start. Fix with: chmod 600 <CLAW_HOME>/config.json
```

To fix it:

1. Run the `chmod` the message names, as the user ClawEh runs as. For example:

   ```
   chmod 600 ~/.claw/config.json
   ```

2. Start ClawEh again.

A `config.json` that allows less than `root.file_mode` is set to it.

The message appears in the service's output (`systemctl status claw`, or `journalctl -u claw`), or in the terminal when ClawEh was started by hand.

The admin credentials file and any `file:` secret reference are checked against `secrets.file_mode` the same way: one that allows more is refused, and the log names the `chmod` to run. See [Signing in](./login.md#no-account-yet).

[Check Up](./checkup.md) shows the **Data directory permissions** row for anything that has become more open than its mode since the last start.

### File permissions

`security.permissions` in `config.json` sets the exact modes of the folders and files in the data directory, one pair per area. Every key is written to the file: if one is missing at start, ClawEh adds it with its default and logs the keys it added. Deleting the whole block and restarting writes the defaults back.

| Area | Folders | Files |
|---|---|---|
| `root` | the data directory itself | files directly in it, such as `config.json` |
| `secrets` | `internal/`, `tls/`, `tokens/` | every secret: the credentials file, TLS files, token stores, everything in `internal/`, any file named for a token or secret, and `file:` secret references |
| `bin` | `bin/` | your scripts and programs; they keep execute bits |
| `logs` | `logs/` | logs, the alerts log and dumps |
| `agents` | the agents folder and every agent's own workspace | agent files, session archives and memory databases |
| `media` | `media/` | downloaded attachments |
| `common` | the common folder | shared files |
| `backup` | the backup folder | backup archives |
| `default` | everything else (`cli/`, `skills/`, `cards/`, …) | everything else |

Every folder defaults to `0700` and every file to `0600`, except `bin` files, which default to `0700`.

For example, to let a second user in ClawEh's group read the logs and the agents' folders:

```json
"security": {
  "permissions": {
    "root":    { "dir_mode": "0750", "file_mode": "0600" },
    "secrets": { "dir_mode": "0700", "file_mode": "0600" },
    "bin":     { "dir_mode": "0700", "file_mode": "0700" },
    "default": { "dir_mode": "0700", "file_mode": "0600" },
    "logs":    { "dir_mode": "0750", "file_mode": "0640" },
    "agents":  { "dir_mode": "0750", "file_mode": "0640" },
    "media":   { "dir_mode": "0700", "file_mode": "0600" },
    "common":  { "dir_mode": "0700", "file_mode": "0600" },
    "backup":  { "dir_mode": "0700", "file_mode": "0600" }
  }
}
```

- `root.dir_mode` must give the group access too, or it cannot reach `logs/` and `agents/`.
- A mode is 3 or 4 octal digits; a fourth (leading) digit must be `0`. A wrong value stops ClawEh from starting, with a message naming the key.
- The owner always keeps access: a folder mode without owner `rwx` (`r-x` for `bin`) or a file mode without owner `rw` (`r` for `bin`) is used with those bits added, and a warning names the key.
- A mode that gives other users any access is marked on the [Check Up](./checkup.md) page and logged as a warning at start.
- An area is matched by the paths ClawEh is configured with, not by another spelling through a symbolic link. Agent workspaces and a backup folder outside the data directory are not walked.
- New folders and files take their area's mode as they are created. Existing ones are set at the next start. After a config reload, new folders and files take the new modes (logs, dumps and downloaded media keep the modes from start), and the credentials file and `file:` secret references are checked against the new `secrets.file_mode` at once: lowering it makes a file that is more open fail until you `chmod` it.
