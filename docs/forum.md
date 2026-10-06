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

The agent then has nine tools: `forum_models`, `forum_validate`,
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

One JSON object, passed to `forum_validate` / `forum_launch` as `config`, or
as `config_file`, a path Alice's file tools may read. Unknown fields are
refused.

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

- `brief` goes to every participant; `instructions` only to its participant.
- `sources` are `inline` or a `file`. A relative path resolves against Alice's
  workspace (inline configuration) or the configuration file's directory, and
  must be readable by Alice's file tools.
- `layers` run in order. `delivery` is `after_round` (turns in a round do not
  see each other) or `per_turn` (each turn sees the earlier ones). A layer may
  have a `moderator` that continues, guides or stops it, and JSON output may be
  checked against a schema in `schemas`.

Call `forum_validate` first: it checks everything, agents and models included,
without creating anything.

## Running it

`forum_launch` answers "Forum <id> launched." at once; the forum runs in the
background. `forum_status` shows its progress, `forum_pause` / `forum_resume`
/ `forum_cancel` control it, and `forum_results` returns its results (see
[Results](#results)). When
it ends (completed, incomplete, failed or cancelled) Alice gets
`[System: forum] Forum design-review finished: completed (id <id>).` in her
conversation. If she launched it from a chat, her answer goes to her default
chat (her default binding), or nowhere when she has none; a forum launched
locally is never posted.
`forum_delete` removes a paused or finished forum.

A forum survives a restart: an interrupted forum resumes where it stopped, and
a turn that was in progress is sent again (an existing agent may see that
message twice). A forum that stops on an error raises the "Forum stopped"
alert and continues with `forum_resume` or at the next start.

## Results

`forum_results` returns, for each output of the result layers, its author
(the participant), layer, round, size in characters, the file holding it, and
its text, plus the path of `transcript.md`. Paths are relative to Alice's
workspace (`forums/<id>/...`), so her file tools can open them. A running or
stopped forum returns the outputs published so far, the same way.

### Large results

Each output's text is returned in full up to 4,000 characters
(`forum.MaxResultInlineChars`). A longer output is cut there and followed by
`(truncated; full text in forums/<id>/...)`; the whole text stays in that
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
  forum.json          the configuration as launched
  transcript.md       the public transcript, written as turns are published
  result.json         the result, once the forum has ended
  layers/<layer>/...  every message sent, every reply, every output
  commits/            the run's log (what a restart replays)
```

Follow a running forum with:

```
tail -f <workspace>/forums/<id>/transcript.md
```

The transcript holds published outputs and the moderator's public decisions,
never a participant's private instructions, rejected replies or private
messages. Nothing is deleted until `forum_delete`.

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
- Temporary participants are kept alive while the forum is paused and deleted
  when it ends; the registry's 24-hour idle limit is only a backstop.
