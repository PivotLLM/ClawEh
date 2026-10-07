# Forums

A forum is a structured discussion that you set up and ClawEh runs for you.
You choose the participants, write a brief and the material they work from,
and lay the discussion out in ordered layers. ClawEh sends every turn itself,
keeps everything on disk under `forums/<id>/` in your workspace (each run in
`forums/<id>/runs/<n>/`), and tells you when a run ends.

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
- Neither you nor a clone of you can take part, since both can read the
  forum's files.

## How to run one

A forum has a configuration and runs. You set the configuration up step by
step, then launch it; every launch is a new run from the beginning.

To run the same forum on new material (the next topic, chapter or question),
do not build a new forum: change only the source with one
`forum_config_update`, such as
`{"sources": {"topic": {"file": "files/topic2.md"}}}`, and `forum_launch`
again. That is a new run; earlier runs stay readable with `run`.

1. `forum_new` creates a forum and returns its ID.
2. `forum_config_template` with `name` fills its configuration from a
   template, or `forum_config_import` with `config` from an exported one.
3. `forum_config_update` with `changes` edits it: a JSON merge patch, where
   objects merge, `null` deletes a key and arrays such as `layers` are
   replaced whole. Fill every `<...>` placeholder and set the models from
   `forum_models`. `forum_config_export` shows the configuration so far.
   For example, `forum_config_update` with
   `{"id": "<id>", "changes": {"sources": {"question": {"inline": "Should we use Go or Python?"}}, "participants": {"chair": {"model": "<a name from forum_models>"}}}}`
   sets the question and the chair's model and leaves everything else as
   it is. `changes` is a JSON object, never a string.
4. `forum_validate` with the ID. Fix every issue it lists.
5. `forum_launch` with the ID starts run 1 in the background. You will be
   notified when it finishes: end your turn then, and do not poll
   `forum_status` or the forum's files while it runs.
6. `forum_status` with the ID shows the latest run; `forum_pause`,
   `forum_resume` and `forum_cancel` control it.
7. When you are notified that the run has ended, `forum_results` with the
   ID.
8. To run it again, change the configuration (only while it is not running)
   and `forum_launch` again: run 2 starts from the beginning, and run 1 keeps
   its files. `forum_status` and `forum_results` take `run` to look at an
   earlier run. A paused run cannot be resumed once the configuration has
   changed; launching then replaces it.
9. `forum_delete` removes the forum and all its runs, unless it is running.

`name` in the configuration is optional (one line, at most 100
characters); status, results and notices then
name the forum "<name> (<id>)" instead of the ID alone.

A source `file` is read as your file tools read that path (your
workspace, or a mount such as `maestro/`).

A source has either `inline` or `file`: to turn an inline source into a
file, remove `inline` in the same patch, as in
`{"sources": {"question": {"inline": null, "file": "files/question.md"}}}`.
To start another forum from this one, `forum_config_export` it and
`forum_config_import` the result into a forum made with `forum_new`.

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

The templates are complete examples: `forum_config_template` with one of the
names below puts it in a forum's configuration, and `forum_readme` with
`template` shows it.
Text in the form `<...>` is a placeholder to replace. Never use that form
for real content: validation refuses any value that is entirely `<...>`.
