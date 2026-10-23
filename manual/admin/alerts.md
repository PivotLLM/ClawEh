# Alerts

ClawEh raises an alert when something needs a person rather than a retry, and delivers it outside the agents' own chats so you hear about it even when a channel is down.

## What alerts are for

Alerts are for failures and outages: a model whose key was revoked, a channel that cannot connect, a backup that did not run. A setting that is off or ignored is not an alert; it shows on the [Check Up](./checkup.md) page.

Every ClawEh alert is sent at normal priority.

## Where alerts go

Every alert is written to `<CLAW_HOME>/logs/alerts.log`.

To see them in the WebUI:

1. Open **Logs** in the **Services** group.
2. Set the **Log** selector to **Alerts**.
3. Choose how many lines to **Show** and click **Refresh** for new ones.

"No alerts" means none has been raised.

## Receive alerts by phone, mail or webhook

Alerts can also be delivered by Pushover, SMS (Telnyx), mail (SMTP) or a JSON webhook. These are set with `ALERTER_*` environment variables, not in the WebUI.

1. Create the file `~/.alerter` in the home directory of the user ClawEh runs as, and make it private:

   ```
   touch ~/.alerter && chmod 600 ~/.alerter
   ```

2. Add the variables for the channels you want, one `KEY=VALUE` per line:

   | Channel | Variables |
   |---|---|
   | Pushover | `ALERTER_PUSHOVER_TOKEN`, `ALERTER_PUSHOVER_DEST` |
   | SMS (Telnyx) | `ALERTER_TELNYX_API_KEY`, `ALERTER_SMS_FROM`, `ALERTER_SMS_TO` |
   | Mail | `ALERTER_SMTP_HOST`, `ALERTER_SMTP_FROM`, `ALERTER_SMTP_TO`; optional `ALERTER_SMTP_PORT`, `ALERTER_SMTP_USER`, `ALERTER_SMTP_PASSWORD` |
   | Webhook | `ALERTER_WEBHOOK_URL`; optional `ALERTER_WEBHOOK_HEADERS` |

3. Restart ClawEh.

The same variables can instead be set in the service's environment, which wins over the file. To write the alerts log somewhere else, set `ALERTER_LOG` to a full path and file name.

A channel with a missing or malformed variable stops alerting from starting; ClawEh then logs an error and runs without alerts. Check the [log](./logs.md) after a change.

Each channel can be limited to a minimum priority with `ALERTER_PUSHOVER_MIN_PRI`, `ALERTER_SMS_MIN_PRI`, `ALERTER_SMTP_MIN_PRI` or `ALERTER_WEBHOOK_MIN_PRI` (0 normal, 1 urgent, 2 emergency). Since every ClawEh alert is normal, a channel set to 1 or 2 receives nothing from ClawEh.

## Repeats

The same alert about the same thing within ten minutes is counted, not sent again. The next one after that says how many were held back.

## The alerts

### Models

See [Cooldowns](../providers/concepts.md#cooldowns) for when a model is set aside.

| Alert | Raised when |
|---|---|
| Model authentication or billing failure | A model failed for a key, login or credit problem; it is set aside until fixed. |
| Model parked after repeated failures | A model kept failing for another reason and is set aside for a while. |
| Daily model spend over threshold | The day's model cost reached `agents.defaults.daily_spend_alert_usd`. |
| Context compaction breaker tripped | Summarizing a long conversation failed three times in a row; check the memory models. |

### Channels and devices

See [Channels](../channels/index.md#when-a-message-cannot-be-delivered) for which delivery failures are alerted.

| Alert | Raised when |
|---|---|
| Channel failed to start | A channel failed to start ten times in a row; it keeps retrying. |
| Channel send failed | A message or file could not be sent after retrying. Not raised when the recipient is just offline. |
| Channel receive loop stopped | The device listener failed; it keeps retrying. |
| `<Platform>` down | A platform's channels (for example Telegram) have had no connection for ten minutes. |
| `<Platform>` up | Every channel in that outage has reconnected. |
| Channel credentials rejected | Slack or Matrix rejected the channel's token. |
| Telegram polling failed | Telegram rejected the bot token. |
| SecMsg account discovery failed | The SecMsg service could not be asked for its accounts. |
| SecMsg has no linked accounts | The SecMsg service has no account to use. |
| Voice transcription rejected | The transcription service refused the key or credit. |
| Device source not started | A device event source failed to start. |
| USB device monitor stopped | USB plug/unplug monitoring ended. |
| Device store unavailable | The paired-device database could not be opened. |
| Device authentication locked out | An address failed device sign-in 5 times in ten minutes. |

### MCP servers and tools

| Alert | Raised when |
|---|---|
| MCP `<name>` down | An [MCP server](../mcp.md#external-mcp-servers) has been unreachable for five minutes; reminded at most hourly. A server that is back sooner, say after a reboot, raises nothing. |
| MCP `<name>` up | That server is back, after a down alert. |
| Fusion token store unavailable | The Google/Microsoft token database could not be opened; [Fusion](../mcpfusion.md#tokens) tools are off. |
| Maestro tools disabled | An agent's [Maestro](../maestro.md) folder could not be prepared. |
| Forum `<id>` run `<n>` of `<agent>` stopped | A [forum](../forums/running.md#restarts) run stopped on an error it cannot recover from by itself. |

### ClawEh itself

| Alert | Raised when |
|---|---|
| HTTP listener stopped | The WebUI listener died; ClawEh exits so the service restarts it. |
| MCP host server stopped | ClawEh's own MCP server died; ClawEh exits so the service restarts it. |
| Agent loop stopped | The agent loop failed; ClawEh exits so the service restarts it. |
| Config file invalid | A change to `config.json` on disk could not be loaded and was not applied. |
| Config reload failed | A valid change failed part way; some services may not be running. |
| Agent references a missing model | An agent named a model that no longer exists; the next model is used. |
| Service tokens not loaded | The service-token file could not be read. |
| Log rotation failed | The midnight log roll failed; logging may have stopped. |

### Data

| Alert | Raised when |
|---|---|
| Session not saved | A conversation could not be saved. |
| Session store write failed | A message could not be saved, so the turn was stopped. |
| Session state not persisted | A per-conversation setting could not be saved. |
| Session retention failed | The nightly clean-up of old conversations failed. |
| Sub-agent record not written | A sub-agent's status or result could not be saved. |
| Cognitive memory migration failed | An agent's memory database could not be opened. |
| Message-token store unreadable | An agent's message-token file is damaged. |
| Message-token store not written | A message-token change, such as a revocation, could not be saved. |
| Named message-token store unreadable | The named message-token file could not be loaded. |
| Mount marker not written | A watched mount's record of seen files could not be saved. |
| Cron store unreadable | The scheduled jobs file could not be read; no jobs run. Fix it before anything saves over it. |
| Cron store not saved | A scheduled job's state could not be saved. |
| Scheduled job failed | A [scheduled job](../agents/scheduling.md) did not run. |

### Security and backup

| Alert | Raised when |
|---|---|
| WebUI login address locked out | An address failed 10 sign-ins in ten minutes. See [Signing in](./login.md#failed-sign-ins). |
| WebUI login account locked out | A username failed 10 sign-ins in ten minutes. |
| TLS certificate reload failed | A changed certificate did not load; the old one keeps serving. See [HTTPS](./https.md#renewals). |
| TLS certificate expires soon | Your own certificate is within 14 or 3 days of expiry, or expired. |
| Self-signed TLS certificate renewal failed | The self-signed certificate could not be renewed. |
| Nightly backup failed | The nightly backup did not run. See [Backup](./backup.md). |
| Database failed integrity check | A database was damaged and left out of a backup. |

If the alerts log itself cannot be opened, ClawEh writes an error to its own log and runs without alerts.
