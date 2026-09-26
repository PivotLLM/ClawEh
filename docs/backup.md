# Backup and restore

ClawEh takes a nightly backup of everything needed to bring an install back on
the same or another host, and restores one with a single command.

## What is backed up

One archive per run, `claw-backup-YYYYMMDD-HHMMSS.tar.gz`, containing (paths
relative to `CLAW_HOME`):

| Entry | What it is |
|---|---|
| `manifest.json` | When and where the archive was taken, and the file list. |
| `config.json` | The configuration, including every credential in it. |
| `cron/jobs.json` | Scheduled jobs. |
| `credentials.json` | The WebUI admin account (when present). |
| `tls/` | TLS key and certificate (when present). |
| `state/` | Service and integration tokens, the device pairing database (`gateway.db`), the Fusion OAuth token store (`fusion-tokens.db`). |
| `agents/<agent>/sessions/*.archive.db` | Session archives. |
| `agents/<agent>/cogmem/*.db` | Cognitive memory. |
| any other `*.db` / `*.sqlite` under `CLAW_HOME` | |

If `agents.base_dir` points outside `CLAW_HOME`, its databases are stored under
`external/agents/` and the manifest records the real directory.

**Not included:** `media/` (tool-produced files, a cache), `logs/`, per-agent
`tmp/`, cogmem's per-sub-agent `subagents/` snapshots, previous backups, and
SQLite `-wal`/`-shm` files (see below). Agent `files/` and `skills/` are not
part of the backup; treat them like any other directory you own.

## How databases are copied

A SQLite database in WAL mode is two files plus a lock, and a plain copy of the
main file can miss committed rows or be torn. ClawEh never copies a database
with file I/O. Each one is opened read-only, checked with `PRAGMA quick_check`,
and written into the archive with `VACUUM INTO`, which takes a consistent
snapshot that already contains everything in the WAL. The gateway keeps running
throughout. A database that fails the check is left out, logged, and reported
through the alert "Database failed integrity check"; the rest of the backup is
still written.

## Where and when

```json
"backup": { "enabled": true, "at": "03:00", "retain_days": 30, "dest": "" }
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Set `false` to turn the nightly backup off. |
| `at` | `03:00` | Local time of day (`HH:MM`). |
| `retain_days` | `30` | Archives (and `YYYYMMDD` folders from older versions) older than this are deleted from the destination. |
| `dest` | `<CLAW_HOME>/backup` | Directory archives are written to. |

The destination is created `0700` and archives are `0600`: they contain every
secret the install has. The scheduler re-reads the config every minute, so
changes apply without a restart.

## On demand

```
claw backup                      # to backup.dest (default <CLAW_HOME>/backup)
claw backup --dest /mnt/nas/claw
```

Prints the archive path, its size and the number of files. If a database was
skipped the command lists it and exits non-zero. The WebUI's **Back up now**
button and `POST /api/backup` run the same code.

## Off-host copies

A backup on the same disk as the data protects against mistakes, not against
losing the host. Either set `backup.dest` to a mounted remote filesystem, or
copy the archives elsewhere after each run, for example:

```
rsync -a --chmod=F600,D700 "$CLAW_HOME/backup/" backup-host:claw-backups/
```

Archives are self-contained: one file is one restorable backup.

## Restore

1. Stop the gateway (`systemctl stop claw` or your unit). `claw restore`
   refuses to run while the gateway holds its lock.
2. Run:

   ```
   claw restore /path/to/claw-backup-20260926-030000.tar.gz
   ```

   It prints where the archive came from and every file it will write, marking
   the ones that replace an existing file, and asks for confirmation (`--yes`
   skips the prompt).
3. Every restored database is checked with `PRAGMA quick_check` in a staging
   directory first. If one fails, the restore stops and nothing has changed.
4. Files being replaced, and any `-wal`/`-shm` files beside a replaced
   database, are moved to `<CLAW_HOME>/restore-backup-<timestamp>/` before the
   archived copies are put in place. Delete that directory once you are happy
   with the result.
5. Start the gateway.

Restoring onto a new host: install ClawEh, set `CLAW_HOME` (or use the
default), copy the archive over, and run the same command. Paths inside the
archive are relative to `CLAW_HOME`, so the new home may be anywhere; an
external `agents.base_dir` is restored to the directory recorded in the
manifest.
