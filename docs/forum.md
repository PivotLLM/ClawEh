# Forums

A forum is a structured discussion among agents that one agent sets up and
ClawEh runs. The agent that launches it chooses the participants, writes a
brief and the sources they work from, and lays out the discussion in ordered
layers (independent opinions, a debate, a final report). ClawEh then sends
every turn itself, keeps everything on disk, and tells the launching agent
when the forum has finished. The examples use two agents, Alice and Bob.

## Enabling

A forum is set up by an agent, never by a person directly. Allow it per agent:

- **WebUI:** Agents page, the agent's card, **Allow forum**.
- **Config:** `"forum": true` on the agent in `agents.list`. Off by default.

The agent then has fifteen tools: `forum_readme`, `forum_models`,
`forum_new`, `forum_config_template`, `forum_config_import`,
`forum_config_update`, `forum_config_export`, `forum_validate`,
`forum_launch`, `forum_status`, `forum_pause`, `forum_resume`,
`forum_cancel`, `forum_results` and `forum_delete`. They come as a set, like
Maestro and Fusion: the agent's `tools` list does not select them, its
`deny_tools` can still remove one. The Check Up report shows a Forum row for
every agent and lists `forum_launch` among the sensitive tools of an agent
with the switch on.

Turning the switch off removes the tools at the next reload. Forums already
running carry on to the end, and their completion notice is still delivered.
The switch gates only the tools: after a restart every agent's forums are
resumed, paused ones keep their participants alive, and finished ones are
cleaned up, whether the switch is on or off.

## Who can take part

| Participant | Form | What it is |
|---|---|---|
| An existing agent | `"agent": "bob"` | Bob himself: his conversation, memory and tools. Forum turns join his one conversation. |
| A clone | `"clone": "bob"` | A temporary copy of Bob: his prompt, tools and models, a copy of his memory, a fresh conversation. Deleted when the forum ends. |
| A fresh agent | `"model": "<name>"` | A temporary agent with no tools and no workspace files, on one of the launching agent's models (`forum_models` lists them). Optional `system_prompt` and `mode` (`memory`, `context`, `single_shot`). Deleted when the forum ends. |

Alice can name Bob (as himself or as a clone) only when Bob is in her
`subagents.allow_agents`. A human agent (see `human-agents.md`) can take part
as an existing agent; it is never cloned.

Every turn is an ask from Alice (see `agent-messaging.md`): Bob sees
`[Message from Alice — Alice is waiting for your reply]`. Each ask is checked
against Alice's permissions at that moment: if Bob has left her
`subagents.allow_agents`, the forum stops instead of asking him. The turn
runs at the maximum sub-agent depth (`agents.defaults.max_subagent_depth`),
so a participant cannot spawn workers or ask other agents, and forum tools
answer "Forum tools are not available at the maximum sub-agent depth.". A
temporary agent a forum created has no forum tools at all.

Each participant's turn uses that participant's own tools (a clone uses its
source's): it can run `shell_exec` only if the agent's tool permissions
include it, wherever the forum was launched from.

## Configuration overview

One JSON object, the forum's configuration (see
[Setting one up](#setting-one-up)). Unknown fields are refused.

```json
{
  "version": 1, "name": "design-review",
  "brief": {"purpose": "Expose weaknesses before adoption.",
            "task": "Review the proposal and recommend concrete changes."},
  "sources": {"proposal": {"decode": "markdown", "file": "files/proposal.md"}},
  "participants": {
    "bob":    {"agent": "bob", "instructions": "Challenge cost and reversibility."},
    "critic": {"clone": "bob", "instructions": "Challenge the trust assumptions."},
    "editor": {"model": "<a model from forum_models>",
               "instructions": "Prioritise supported findings. Keep dissent."}
  },
  "limits": {"max_calls": 20, "max_duration_seconds": 1800,
             "call_timeout_seconds": 300, "max_attempts_per_turn": 2,
             "max_parallel_calls": 2},
  "layers": [
    {"id": "review", "participants": ["bob", "critic"],
     "instructions": "List your objections.",
     "inputs": [{"from": "source:proposal"}],
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}},
    {"id": "report", "participants": ["editor"],
     "instructions": "Write the prioritised changes.",
     "inputs": [{"from": "source:proposal"}, {"from": "layer:review"}],
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "markdown"}}
  ]
}
```

- `name` is optional, one line of at most 100 characters. When set it names the forum in `forum_status`,
  `forum_results` and the transcript heading, and the tools' replies,
  refusals and the completion notice call it "<name> (<id>)"
  ("design-review (<id>)"); otherwise the forum's ID is used.
- `brief` goes to every participant; `instructions` only to its participant.
- `sources` are `inline` or a `file`. A `file` is read exactly as Alice's
  file tools would read that path: relative to her workspace, or in one of
  her mounts such as `maestro/`.
- `layers` run in order. `delivery` is `after_round` (turns in a round do not
  see each other) or `per_turn` (each turn sees the earlier ones). A layer may
  have a `moderator` that continues, guides or stops it, and JSON output may be
  checked against a schema in `schemas`.
- `"anonymous": true` on a layer input (`{"from": "layer:answer",
  "anonymous": true}`) shows that layer's outputs as "Response A",
  "Response B", … without their authors. The rules:
  - Letters are assigned per producing layer, by the author's position in its
    `participants`, so they are the same for every reader and after a restart.
  - Each reader's own outputs are left out; `distribute: random` never deals a
    reader its own. A reader left with nothing on an optional input is told
    "No other responses are available."
  - Once any input reads a layer anonymously, every other reader of that layer
    sees the author with the letter ("Bob (Response A)"), so a chair can match
    reviews to authors. The transcript and the results keep the real names;
    the transcript shows the letter next to each author ("Bob (Response A)"),
    so the reviews' letters can be matched there.
  - Validation refuses: `anonymous` on a source input or with
    `distribute: same_participant`; a reading layer that is not `after_round`
    with one round and no moderator; a reader that also reads the layer by
    name, moderates it, or takes part in it while it is `per_turn` or has more
    than one round; a reader with no other author to read; and the launching
    agent or a clone of it anywhere in a forum with an anonymous input (either
    can read the forum's files).

## Setting one up

A forum has a configuration and zero or more runs. Alice sets the
configuration up step by step and launches it when it is ready; each launch
is a new run:

| Tool | What it does |
|---|---|
| `forum_new` | Creates a forum with an empty configuration and answers "Forum <id> created.". Its status is `new` until its first run. |
| `forum_config_template` | `id`, `name`: the configuration becomes that built-in template. |
| `forum_config_import` | `id`, `config`: the configuration becomes `config`, a JSON object (usually one `forum_config_export` returned). |
| `forum_config_update` | `id`, `changes`: applies `changes` as a JSON merge patch (RFC 7386): objects merge, `null` deletes a key, arrays such as `layers` are replaced whole. |
| `forum_config_export` | `id`: the forum's configuration, to import into another forum. |
| `forum_validate` | `id`: checks the configuration, agents and models included, without creating anything. |
| `forum_launch` | `id`: validates the configuration and starts a new run of it from the beginning ("Forum design-review (<id>) launched (run 2). You will be notified when it finishes; end your turn instead of checking status."). |

The configuration need not be valid while it is being edited; only
`forum_validate` and `forum_launch` check it. An update keeps the order of
the keys already in the configuration and adds new ones at the end of their
object; the keys of an imported or patched object arrive in alphabetical
order. Numbers in `config` and `changes` pass through a floating-point value,
so an integer above 2^53 (a large `seed`) loses precision.

The configuration can be changed whenever the forum is not running: before
its first run, while its latest run is paused, and after it has ended. While
a run is running, `forum_config_template`, `forum_config_import`,
`forum_config_update`, `forum_launch` and `forum_delete` answer "Forum
design-review (<id>) is running; pause or cancel it first.", and while it is
pausing "Forum design-review (<id>) is still pausing; try again once it is
paused.", and while it is being cancelled "Forum design-review (<id>) is
being cancelled." (`forum_config_export` and `forum_validate` still work). A change takes effect at
the next launch, never in a run already started. A forum survives a restart,
and a launch that fails leaves it as it was.

To run the same forum on new material, change only its source and launch
again: to review a book chapter by chapter, run chapter 1, then
`{"sources": {"chapter": {"file": "files/chapter2.md"}}}` and launch; run 2
reviews chapter 2 and run 1 keeps its own files and results. The guide tells
the agent to do this rather than build a new forum. A source holds either
`inline` or `file`, so turning an inline source into a file removes `inline`
in the same patch:
`{"sources": {"question": {"inline": null, "file": "files/question.md"}}}`.
To start another
forum from this one, export its configuration and import it into a forum made
with `forum_new`.

## Guide and templates

`forum_readme` without arguments returns a one-page guide for the agent (what
a forum is, participants, layers, the steps from a new forum to results, the
limits, and when a single sub-agent or `agent_message` is enough), followed
by the built-in templates. With `template` it returns that template's
configuration; `forum_config_template` puts one in a forum. An unknown name
is refused with the valid ones.
Every other forum tool's description tells the agent to call it first.

| Template | What it does |
|---|---|
| `writing` | A writer drafts on a topic read from a file, two critics comment separately (structure and argument; style and clarity), the writer revises, and an editor produces the final text, ending with a short "Notes" section on what changed. |
| `council` | Three members on different models answer a question independently, review the others' answers anonymously ending with a `FINAL RANKING:`, and a chair writes the final answer with the consensus, the disagreements and the aggregate ranking. |

A template's models are placeholders such as `"<a model from forum_models>"`;
the `writing` template's topic is a source `file` whose path is a `<...>`
placeholder (so the next topic is one `forum_config_update` of that path),
and the `council` template's question is an inline `<...>` placeholder.
Text in the form `<...>` is always a placeholder to replace and never real
content: validation refuses any value, anywhere in the configuration, that is
entirely `<...>` (after trimming spaces), naming its path, such as
`sources.question.inline: is still a placeholder; replace it.`. Text that
only contains angle brackets, such as `Is a<b?`, is unaffected. The guide and
templates are embedded in the binary (`forum/readme/`).

## Running it

`forum_launch` answers at once, "Forum design-review (<id>) launched (run
<n>). You will be notified when it finishes; end your turn instead of
checking status.", and the run goes on in the background. `forum_status` shows the progress of the latest run
(or of `run`), how many runs the forum has and whether its configuration
changed since the latest run (`config_changed`). `forum_pause`,
`forum_resume` and `forum_cancel` control the latest run, and
`forum_results` returns a run's results (see [Results](#results)). A paused
run resumes only while the configuration is unchanged; after a change
`forum_resume` answers "Forum design-review (<id>): the config changed;
launch to start a new run." (formatting, key order and number spelling are not
changes), and
launching cancels the paused run (without a notice) before the new one
starts. A forum tool called with an argument it does not take is refused,
naming it ("Unknown argument forum_id; use id."). When a run ends
(completed, incomplete, failed or cancelled) Alice gets
`[System: forum] Forum design-review (<id>) run 1 finished: completed.` in
her conversation. If she launched it from a chat, her answer goes to her
default chat (her default binding), or nowhere when she has none; a forum
launched locally is never posted. `forum_delete` removes a forum and all its
runs, unless a run is running.

A run survives a restart: an interrupted run resumes where it stopped, and a
turn that was in progress is sent again (an existing agent may see that
message twice). A launch interrupted before its run started leaves the forum
as it was. A run that stops on an error raises the "Forum run stopped" alert
and continues with `forum_resume` or at the next start.

## Results

`forum_results` returns, for the latest run or the one `run` names, each
output of the result layers: its author (the participant), layer, round, size
in characters, the file holding it, and its text, plus the path of
`transcript.md` once it exists. Paths are relative to Alice's workspace
(`forums/<id>/runs/<n>/...`), so her file tools can open them. A running or
stopped run returns the outputs published so far, the same way. When the
result layers have no output (a run that failed early), the other layers'
outputs are listed under `other_layers` instead, within the same limits.

### Large results

Each output's text is returned in full up to 4,000 characters
(`forum.MaxResultInlineChars`). A longer output is cut there and followed by
`(truncated; full text in forums/<id>/runs/<n>/...)`; the whole text stays in that
file. All outputs together get at most 16,000 characters inline
(`forum.MaxResultInlineTotalChars`), in order; the ones after that are listed
with `"inline_omitted": true`, their size and file, but no text. An output
whose file cannot be read is marked `"unreadable": true`. For long work, such as a book chapter, end the forum with a short
summary layer and make it the result layer: Alice then gets the summary
inline and reads the full outputs from their files only when she needs them.

## Where files live

Everything is under the launching agent's workspace:

```
<workspace>/forums/<id>/
  forum-meta.json       which agent the forum belongs to
  forum.json            the configuration, as it stands now
  runs/<n>/             run n (1, 2, 3, ...), never changed by a later run
    forum.json          the configuration this run used
    transcript.md       the public transcript, written as turns are published
    result.json         the result, once the run has ended
    layers/<layer>/...  every message sent, every reply, every output
    commits/            the run's log (what a restart replays)
```

Follow a running forum with:

```
tail -f <workspace>/forums/<id>/runs/<n>/transcript.md
```

Each output in the transcript is quoted in a code block, so its own headings
do not mix with the transcript's. The transcript holds published outputs and
the moderator's public decisions,
never a participant's private instructions, rejected replies or private
messages. Its heading names the forum and the run. Nothing is deleted until
`forum_delete`.

With the default workspace restrictions (`restrict_to_workspace` on and a
`workspace_write_subdir` set), Alice's file tools can read her `forums/`
folder, whatever `agents.defaults.workspace_read_subdirs` lists, but cannot
write to it, and cannot read another agent's forums.

## Limits

- `limits` in the configuration are required and are hard limits: total calls
  (repairs and moderator checks included), total duration, time per turn,
  attempts per turn and parallel turns. A layer may set its own `max_calls`.
- Each participant's own settings (its `request_timeout`, its tool limits)
  still apply to its turns.
- A turn whose participant's models are all in cooldown is held until one is
  available and then sent after a random 2 to 5 seconds, so held turns do
  not all reach the model at once; the wait counts against `call_timeout_seconds`
  (and the run's `max_duration_seconds`). A cooldown that ends within the
  call timeout costs nothing. One that outlasts it (or leaves less than a
  second) is recorded as a timeout attempt without being sent, and that
  attempt counts toward `max_attempts_per_turn` and `max_calls` like any
  timeout. The wait is logged at INFO with the forum, the participant and
  the model.
- Temporary participants are created for each run, kept alive while it is
  paused and deleted when it ends; the registry's 24-hour idle limit is only a
  backstop.
