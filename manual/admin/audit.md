# Audit log

The audit log records every tool an agent runs, every configuration save, every sign-in and every restart from the WebUI, so you can see who did what.

## What is recorded

| Kind | Recorded when | Who |
|---|---|---|
| **Tool call** | An agent runs a tool, including starting a sub-agent. | The agent, and the person who sent the message. |
| **Config write** | A setting is saved from the WebUI (any settings page or the raw config editor). | The WebUI username and client address. |
| **Auth** | A sign-in, sign-out, lockout, or attempt refused during a lockout. | The username and client address. |
| **Restart** | ClawEh is restarted from the WebUI. | The WebUI username and client address. |

What is kept, and what is not:

- Tool calls record the tool, the outcome (ok or error), how long it took, and a shortened copy of the arguments. File contents, edit text and web request bodies are recorded only as their size.
- Config writes record which sections changed, never the values.
- Sign-ins never record the password.
- A call made by a sub-agent is recorded under the agent it copies, marked as a clone.

## Read it in the WebUI

1. Open **Audit** in the **Services** group.
2. Narrow the list with **Kind** (**Tool call**, **Config write**, **Auth**, **Restart**) and **Agent**.
3. Newest events are at the top. Click **Load more** to page back, and **Refresh** to fetch new events.

Columns: **Time**, **Kind**, **Actor / agent**, **Channel**, **Tool / summary**, **Outcome**, **Duration**.

## Follow one turn through the logs

Each agent turn gets a short `turn_id`. It is on the turn's tool calls in the audit log and on its lines in `claw.log`, so you can search the [log](./logs.md) for it to see the whole turn.

## Where it is kept

`<CLAW_HOME>/internal/audit.db`, readable only by the service user. Events are kept for 90 days; older ones are deleted daily. Nothing is ever edited.

The audit log is included in the [nightly backup](./backup.md).

## Gaps

Recording never slows an agent down. If events arrive faster than they can be written, the extra ones are dropped and counted. The **Audit** page then says "N events were dropped because the audit write queue was full; the trail has gaps."

If the audit log cannot be opened at start, ClawEh runs without one and logs a warning. [Check Up](./checkup.md) marks the **Audit log** row when its file is missing.
