# Backup and restore

ClawEh writes one backup archive every night, and `claw restore` puts an install back from one.

The nightly backup is on by default. Each run writes one file, `claw-backup-YYYYMMDD-HHMMSS.tar.gz`, readable only by the service user. It contains every secret the install has, so keep copies somewhere private.

## Settings

On the **System** page, in the **Backup** section:

| Setting | Default | What it does |
|---|---|---|
| **Nightly backup** | On | Write an archive once a day. |
| **Backup time** | 03:00 | Local time of day. |
| **Keep backups (days)** | 30 | Older archives are deleted from the destination. |
| **Destination directory** | `<CLAW_HOME>/backup` | Where archives are written. |

The page saves by itself, and changes apply within a minute without a restart.

The same settings in `config.json`:

```json
"backup": { "enabled": true, "at": "03:00", "retain_days": 30, "dest": "" }
```

## Back up now

- In the WebUI: **System** → **Backup** → **Back up now**. It runs even when **Nightly backup** is off, and says where the archive was written.
- On the server:

  ```
  claw backup
  claw backup --dest /mnt/nas/claw
  ```

  It prints the archive's path, size and number of files. If a database had to be left out, it names it and exits with an error.

## Keep a copy off the machine

A backup on the same disk protects against mistakes, not against losing the machine. Either set **Destination directory** to a mounted network folder, or copy the archives after each run, for example:

```
rsync -a --chmod=F600,D700 "$CLAW_HOME/backup/" backup-host:claw-backups/
```

Each archive is complete on its own.

## What is included

| Included | What it is |
|---|---|
| `config.json` | The configuration, with every credential in it. |
| `cron/jobs.json` | [Scheduled jobs](../agents/scheduling.md). |
| `credentials.json` | The admin account. |
| `tls/` | The HTTPS certificate and key. |
| `cards/` | The [character cards](../agents/character-cards.md). |
| `internal/` | ClawEh's own state: tokens, paired devices, Google/Microsoft sign-ins, the [audit log](./audit.md). |
| `agents/<agent>/sessions/` | Conversations. |
| `agents/<agent>/cogmem/` | Memory. |
| `agents/<agent>/forums/` | Each [forum](../forums/results.md#where-the-files-are)'s setup and every run's files. |
| `agents/<agent>/maestro/` | [Maestro](../maestro.md#where-its-data-lives) projects and playbooks. Lock and temporary files are left out. |
| Any other database under the data directory | |

Databases are copied safely while ClawEh keeps running. Each one is checked first; a damaged database is left out, the rest is still written, and the "Database failed integrity check" [alert](./alerts.md) names it.

A forum that is running during the backup is copied file by file. After a restore it carries on like a run interrupted by a restart.

## What is not included

- Agent `files/` and `skills/` folders. Back these up like any other folder you own.
- Logs, the media cache, each agent's `tmp/`.
- [Temporary agents](../agents/subagents.md#temporary-agents) (sub-agent runs).
- Earlier backups.

### Agents with their own workspace

Only agents in the usual agents folder are fully covered. For an agent whose workspace is set to another folder:

- inside the data directory: its conversations and memory are backed up, its forums and Maestro data are not;
- outside the data directory: nothing of it is backed up.

Back such a folder up yourself, or move the agent back into the agents folder.

## Restore

1. Stop ClawEh, for example `sudo systemctl stop claw`. `claw restore` refuses to run while ClawEh is running.
2. Run:

   ```
   claw restore /path/to/claw-backup-20260926-030000.tar.gz
   ```

3. It lists every file it will write, marking the ones it will replace, and asks you to confirm. (`--yes` skips the question.)
4. Every database in the archive is checked before anything changes. If one fails, the restore stops and nothing is changed.
5. Replaced files are moved to `<CLAW_HOME>/restore-backup-<timestamp>/`. Delete that folder once you are happy with the result.
6. Start ClawEh.

### Restore on a new machine

1. Install ClawEh.
2. Copy the archive to the new machine.
3. Run `claw restore` as above, with ClawEh stopped.

The archive's paths are relative to the data directory, so the new one can be anywhere.

## If the nightly backup fails

The "Nightly backup failed" [alert](./alerts.md) is raised and nothing retries until the next night. The cause is usually disk space or permissions on the destination; the [log](./logs.md) has the details. Run **Back up now** once it is fixed.
