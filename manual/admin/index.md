# Administration

This part of the manual covers running a ClawEh install safely: signing in, reaching it over the network, checking its configuration, and keeping records and backups.

## Pages in this section

| Page | What it covers |
|---|---|
| [Signing in](./login.md) | The admin account, `claw admin`, sessions and login lockouts. |
| [HTTPS and certificates](./https.md) | The HTTP and HTTPS listeners, the self-signed certificate, using your own, `claw tls`. |
| [Network access](./network.md) | Localhost or network, allowed networks, trusted proxies, the device listener. |
| [Check Up and the full report](./checkup.md) | The **Check Up** page, the PDF report, and what to do about a marked row. |
| [Audit log](./audit.md) | The record of tool calls, configuration saves and logins. |
| [Alerts](./alerts.md) | What alerts are for, how to receive them, and the list of alerts. |
| [Backup and restore](./backup.md) | The nightly backup, `claw backup` and `claw restore`. |
| [Logs](./logs.md) | Where the logs are, log levels, the **Logs** page, and the startup permission checks. |

## Where things are in the WebUI

- **Check Up** sits on its own in the sidebar, below the groups.
- **Network**, **System**, **Logs** and **Audit** are in the **Services** group.
- **Sign out** is next to the version at the bottom of the sidebar.

## Commands on the server

Run these on the machine ClawEh is installed on, as the user it runs as.

| Command | What it does |
|---|---|
| `claw admin` | Creates or replaces the admin account. See [Signing in](./login.md). |
| `claw status` | Shows whether ClawEh is running, the addresses to open, the certificate and whether an admin account exists. |
| `claw network` | Shows or sets network access. See [Network access](./network.md). |
| `claw tls` | Shows the HTTPS certificate. See [HTTPS and certificates](./https.md). |
| `claw backup`, `claw restore` | Take or restore a backup. See [Backup and restore](./backup.md). |

On a system install, run them as the service user with the data directory set, for example `sudo -u <service user> CLAW_HOME=/opt/claw claw status`.
