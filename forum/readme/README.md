# Forums

A forum is a structured discussion that you set up and ClawEh runs for you.
You choose the participants, write a brief and the material they work from,
and lay the discussion out in ordered layers. ClawEh sends every turn itself,
keeps everything on disk under `forums/<id>/` in your workspace, and tells you
when the forum has finished.

Use a forum when several independent views, a critique-and-revise cycle or a
review panel improves the result. Do not use one when a single sub-agent or a
single `agent_message` would do: a forum costs a call per participant per
round.

## Participants

Each entry of `participants` takes one of three forms:

- `{"agent": "bob"}`: Bob himself, with his conversation, memory and tools.
- `{"clone": "bob"}`: a temporary copy of Bob (his prompt, tools and models,
  a fresh conversation), deleted when the forum ends.
- `{"model": "<name>"}`: a fresh temporary agent with no tools, on one of
  your models (`forum_models` lists them). Optional `system_prompt` and
  `mode` (`memory`, `context`, `single_shot`).

Any entry may add private `instructions` and a display `name`. You can name
an agent (as itself or as a clone) only if it is in your allowed agents.

## Layers

`layers` run in order. Each has `participants` (also the turn order),
`instructions`, `inputs`, `delivery`, `max_rounds` and an `output` format
(`text`, `markdown` or `json`).

- `delivery: after_round`: turns in a round do not see each other; the round
  is published when it ends. Use it for independent opinions.
- `delivery: per_turn`: each turn sees the turns before it. Use it for a
  debate.
- `moderator`: an optional participant (not one of the layer's) consulted
  between rounds; it continues, guides or stops the layer.
- `inputs`: what the layer receives, each `{"from": "source:<id>"}` or
  `{"from": "layer:<id>"}` (an earlier layer only). Options include
  `select`, `authors`, `to`, `distribute` and `optional`.
- `"anonymous": true` on a layer input hides who wrote what; see below.

`result_layers` names the layers whose outputs are the result; the default is
the last layer. `limits` (`max_calls`, `max_duration_seconds`,
`call_timeout_seconds`, `max_attempts_per_turn`, `max_parallel_calls`) are
required and are hard limits.

## Anonymous review

`{"from": "layer:answer", "anonymous": true}` shows that layer's outputs as
"Response A", "Response B", ... without their authors.

- Letters are assigned per producing layer, by the author's position in its
  `participants`.
- Each reader's own outputs are left out; with `distribute: random` it is
  never dealt its own.
- Anyone reading the same layer by name sees "Bob (Response A)", so a chair
  can match reviews to authors. The transcript keeps the real names.
- The reading layer must be `after_round` with one round and no moderator.
- A reader must not also read that layer by name, moderate it, or take part
  in it while it is `per_turn` or has more than one round, and must have
  someone else's output to read.
- A clone of you cannot take part, since it can read the forum's files.

## How to run one

1. `forum_readme` with `template` to get a starting configuration.
2. Edit it: fill every `<...>` placeholder, set the models from
   `forum_models`.
3. `forum_validate` with `config` (or `config_file`). Fix every issue it
   lists.
4. `forum_launch`. It returns the forum ID at once; the forum runs in the
   background.
5. `forum_status` with the ID to follow it; `forum_pause`, `forum_resume` and
   `forum_cancel` control it.
6. When you are told it has finished, `forum_results` with the ID.
7. `forum_delete` removes a finished or paused forum's files.

## Limits

- The 8,000-character limit on messages to other agents does not apply inside
  a forum.
- `forum_results` returns each output's text up to 4,000 characters, and up to
  16,000 characters for all outputs together; the rest is in the files it
  names.
- For long work, end with a short summary layer and make it the result layer.
  You then get the summary inline and read the full outputs from their files
  only when you need them.
- Participants cannot launch forums, spawn sub-agents or ask other agents.

## Example

The templates are complete examples: call `forum_readme` with `template` set
to one of the names below.
