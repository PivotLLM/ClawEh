# Running a forum

An agent sets a forum up in a few steps, launches it, and can run it again on new material; this page describes those steps and the limits that apply.

You do not run these steps yourself: the agent does, when you ask for a
forum. Knowing them helps you read the agent's replies and ask for the right
thing ("pause the forum", "run it again on chapter 3").

## How the agent sets one up

A forum has a **setup** (its configuration) and zero or more **runs**. The
agent edits the setup step by step and launches it when it is ready; each
launch is a new run.

1. **Create.** `forum_new` creates an empty forum and returns its ID.
2. **Start from something.** `forum_config_template` fills the setup from a
   built-in [template](templates.md), or `forum_config_import` from a setup
   exported from another forum.
3. **Edit.** `forum_config_update` changes parts of the setup: fills the
   blanks, picks the models (`forum_models` lists the ones it may use),
   adjusts instructions or limits. `forum_config_export` shows the setup as
   it stands.
4. **Check.** `forum_validate` checks everything, including that the named
   agents and models exist and that the limits are within the
   [install's maximums](#install-wide-maximums), and lists every problem.
5. **Launch.** `forum_launch` checks again and starts the run in the
   background. The agent is told: "Forum design-review (\<id\>) launched
   (run 1). You will be notified when it finishes; end your turn instead of
   checking status." It then ends its turn and waits.
6. **Results.** When the run ends, the agent is notified and reads the
   result with `forum_results` (see [Results](results.md)).

A forum may have an optional `name` (one line, up to 100 characters). The
agent's replies, the completion notice and the log then call it
"\<name\> (\<id\>)"; otherwise by its ID.

Material for the participants comes from **sources**: text written into the
setup, or a file. A file is read exactly as the agent's own file tools would
read it: from its workspace, or from one of its [mounts](../agents/files-and-mounts.md) such as `maestro/`.

## Run it again on new material

To use the same forum on new material, the agent changes only the source and
launches again. For a book reviewed chapter by chapter:

1. Run 1 reviews `files/chapter1.md`.
2. The agent points the source at `files/chapter2.md` and launches: run 2
   reviews chapter 2.
3. Run 1 keeps its own files and results; the agent can still read them by
   run number.

Each launch starts from the beginning. The agent's guide tells it to do this
rather than build a new forum. To start a different forum from this one,
the agent exports the setup and imports it into a new forum.

The setup can be changed only while the forum is not running: before its
first run, while its latest run is paused, and after a run has ended. A
change applies to the next launch, never to a run already started.

## Pause, resume, cancel

The agent controls the latest run. Ask in the chat ("pause the council",
"cancel the forum").

| Tool | What it does |
|---|---|
| `forum_status` | Progress of the latest run (or an earlier one), the number of runs, and whether the setup changed since the latest run. |
| `forum_pause` | Pauses the run after the turns in progress. Temporary participants are kept while it is paused. |
| `forum_resume` | Continues a paused run, but only while its setup is unchanged. After a change the agent is told to launch a new run instead. |
| `forum_cancel` | Stops the run. Its files stay. |
| `forum_delete` | Removes the forum and all its runs, unless a run is running. |

While a run is running, changing the setup, launching and deleting are
refused with "Forum \<name\> (\<id\>) is running; pause or cancel it first."
Launching while the latest run is paused cancels that paused run, without a
notice, and starts a new one.

A run ends as **completed**, **incomplete** (it ran out of calls or time),
**failed** (for example a turn failed every attempt) or **cancelled**. The
agent's notice gives the reason when it did not complete.

## Restarts

Forums survive a restart of ClawEh:

- An interrupted run resumes where it stopped. A turn that was in progress
  is sent again; an existing agent such as Bob may see that message twice.
- A restart does not use up a turn's attempts, but a turn that crashes the
  service every time ends the run as failed.
- Outputs from turns resent after a restart are marked, in the results, in
  `forum_status` and in the completion notice ("2 outputs came from turns
  resent after a restart.").
- A launch interrupted before its run started leaves the forum as it was.
- A run that stops on an error raises the **Forum run stopped** [alert](../admin/alerts.md) and
  continues when the agent resumes it or at the next start.

## Limits

Every forum setup carries its own hard limits, which the agent chooses:

| Limit | Meaning |
|---|---|
| `max_calls` | Total model calls in a run, repairs and moderator checks included. A layer may also set its own. |
| `max_duration_seconds` | Total run time. A run that reaches it ends incomplete. |
| `call_timeout_seconds` | Time allowed for one turn. |
| `max_attempts_per_turn` | Attempts per turn before the run fails. |
| `max_parallel_calls` | Turns sent at the same time. |

Each participant's own settings (its model's request timeout, its tool
limits) still apply to its turns. When all of a participant's models are in
[cooldown](../providers/concepts.md#cooldowns), its turn waits for one within the turn's time limit; a wait that
outlasts it counts as a timed-out attempt.

### Install-wide maximums

You set the most any forum on this install may use.

1. Open the **System** page.
2. Go to the **Forums** section ("The most any forum on this install may
   use. Blank uses the default.").
3. Set any of the four fields. Changes are saved automatically.

| Field | Default |
|---|---|
| **Max calls per forum** | 200 |
| **Max run time (seconds)** | 7200 |
| **Call timeout (seconds)** | 1800 |
| **Max parallel calls** | 8 |

A blank field (or 0) uses the default; a negative value is refused. A forum
whose limit is above a maximum is refused when the agent checks or launches
it, for example "limits.max_calls: 500 is more than this install allows
(200)". A layer's own call limit is held to the same maximum. There is no
maximum on attempts per turn.

The agent's forum guide ends with the current maximums, so it can stay
under them. A change applies from the next check or launch; a run already
started keeps the limits it was launched with.

In `config.json`:

```json
"forum": {
  "limits": {
    "max_calls": 200,
    "max_duration_seconds": 7200,
    "call_timeout_seconds": 1800,
    "max_parallel_calls": 8
  }
}
```

See also [Forums](index.md) and [Results](results.md).
