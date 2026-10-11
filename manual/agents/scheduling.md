# Scheduled jobs

A scheduled job sends a message to an agent at a set time or interval, and the agent handles it like any other message in its default channel.

## Before an agent can schedule

1. Grant the agent the `cron_schedule` tool (it is in **Default tools**; see [Tools and skills](./tools-and-skills.md)).
2. Give the agent a **Default Channel** on its card (see [Creating and editing agents](./creating.md#default-channel)).

Without a default channel, creating a job is refused: "agent "alice" has no default channel configured; set a default channel (binding) before scheduling".

## Create a job

Ask the agent in plain words, for example:

- "Remind me in 30 minutes to call the bank."
- "Every weekday at 9am, give me the day's list."
- "Every two hours, check the shared folder for new reports."

The agent calls `cron_schedule` and confirms the job. A job can be:

| Kind | Example |
|---|---|
| One time, after a delay | in 10 minutes |
| A fixed interval | every 2 hours |
| A calendar schedule (cron expression, server's local time) | `0 9 * * 1-5`, weekdays at 9am |

Ask the agent to list, show, disable, enable or remove its jobs. An agent sees and manages only its own jobs.

## What happens when a job fires

1. The job's message is delivered to the agent's **current** default channel, as if it had been sent in that chat. Changing the default channel redirects existing jobs.
2. The agent handles it in its one conversation, behind any message it is already working on, and replies in that chat.
3. A job runs at sub-agent depth 0, like a message from a person, so it may spawn sub-agents and ask other agents within the [depth limit](./subagents.md#the-depth-limit).

A one-time job is removed after it runs. A job that cannot run, including one whose agent has lost its default channel, raises the "Scheduled job failed" [alert](../admin/alerts.md); its next run is still scheduled.

To have the agent recall a topic from memory when a job fires, word the job's message so it contains one of that memory domain's keyword triggers (see [Conversation and memory](./memory.md#long-term-memory-cognitive-memory)).

## Watch jobs: only when something changed

A watch job calls one of the agent's tools on the schedule and messages the agent **only when the result changed**. No model runs for a check that finds nothing, so frequent checks cost nothing.

Ask the agent for one, for example "Every 15 minutes, check for unread mail and tell me when new mail arrives." It sets:

- `watch_tool`: the tool to call;
- `watch_args`: its arguments;
- `watch_fields`: the values that decide "changed", for example `messages.id`.

On each run:

- The first successful check records the current state silently.
- Unchanged: nothing is delivered.
- Changed: the job's message is delivered, followed by the new values.
- A check that fails is retried on the next run. After five failures in a row the agent is told once. Each check may take at most 60 seconds.

The tool must be one the agent is granted, checked again on every run: if you remove the tool from the agent, the watch stops working and the agent is told after five failures.

## Listen jobs: wait for events

A listen job keeps calling a tool that waits for an event (a long-poll tool) and messages the agent each time an event arrives. It has no schedule: it runs for as long as the job is enabled, and starts again when ClawEh starts. Ask the agent for one when a tool it has waits for events.

## Scheduling for other agents

An agent schedules only for itself unless **Global cron** is on for it on the Agents page. With it, the agent can create and manage jobs for any agent by naming that agent; the job is delivered to that agent's default channel. Typically one coordinating agent has it.

Jobs cannot be scheduled for a [human agent](./human-agents.md).

## Jobs from the command line

The `claw cron` command shows and manages every job, whatever agent it belongs to:

```
claw cron list
claw cron disable <job-id>
claw cron enable <job-id>
claw cron remove <job-id>
```

`claw cron add` creates a job delivered to a chat you name rather than to an agent:

```
claw cron add --name "Standup" --message "Time for the standup." --cron "0 9 * * 1-5" --channel slack --to C0ANLEQP5GQ
```

The message is handled by whichever agent that chat is bound to (see [Routing and bindings](../channels/routing.md)). All `claw cron` options are in [Command line](../reference/cli.md). Use `--every <seconds>` instead of `--cron` for an interval.

Jobs are stored in `cron/jobs.json` in the data directory and survive restarts.
