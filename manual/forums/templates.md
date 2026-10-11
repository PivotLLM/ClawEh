# Forum templates

ClawEh has two built-in forums, `council` and `writing`, that an agent can start from instead of designing a forum itself.

A template is a complete forum setup with a few blanks. The agent puts a
template into a new forum, fills the blanks (the question or topic, and the
models to use), and launches it. You only need to ask; the agent knows how
to use them.

The models in both templates are chosen from the agent's own models (its
model and fallbacks). An agent with a single model will use that one for
every participant; for the council especially, add more models to the
agent first.

## Council

Three members answer a question independently, review each other's answers
without knowing who wrote them, and a chair writes the final answer.

| Step | Who | What happens |
|---|---|---|
| 1. Answer | Three members | Each answers the question on its own. |
| 2. Review | The same three | Each reviews the other two answers, shown only as "Response A", "Response B", … (never its own), and ends with a ranking of them, best first. |
| 3. Verdict | The chair | Writes the council's final answer from the best of the answers and reviews, says where the members agree and disagree, and gives the combined ranking. |

The members are meant to be on different models, so the answers are
genuinely independent. The chair's verdict is the result.

**Ask for it like this:**

> Run a council on whether we should move our build system from Make to
> Bazel. Use three different models for the members.

> Alice, use the council template: "What are the main risks of storing
> customer files in a single cloud region?"

The question goes into the forum as text. For a long question, put it in a
file in the agent's `files/` folder and say so.

Default limits: at most 16 model calls, 30 minutes in total, 10 minutes per
turn, 2 attempts per turn, 3 turns at once.

## Writing

A writer drafts a text, two critics comment on it separately, the writer
revises, and an editor produces the final version.

| Step | Who | What happens |
|---|---|---|
| 1. Draft | Writer | Writes a complete first draft from the topic file. |
| 2. Critique | Two critics | One critiques structure and argument, the other style and clarity. Each lists concrete problems; neither rewrites the text. |
| 3. Revise | Writer | Revises the draft using both critiques, keeping its own version where it disagrees. |
| 4. Final | Editor | Edits the revised text into its final form and ends with a short **Notes** section on what changed from the first draft and why. |

The editor's text is the result.

The topic is read from a **file**: describe the text you want (topic,
audience, length, any other requirements) in a file the agent can read,
usually in its `files/` folder.

**Ask for it like this:**

> Write `files/briefs/onboarding-guide.md` describing what I need, then run
> the writing forum on it.

> Alice, run the writing template on `files/topics/release-notes.md`.

To write the next text with the same setup, ask the agent to run the same
forum again on a new topic file. That is a new run of the same forum (see
[Running a forum](running.md#run-it-again-on-new-material)).

Default limits: at most 12 model calls, 30 minutes in total, 10 minutes per
turn, 2 attempts per turn, 2 turns at once.

## Changing a template

The agent can change anything in its copy of a template before launching:
the instructions, the number of participants, the limits, or which
participants are existing agents such as Bob instead of fresh ones. The
built-in templates themselves never change.

A template's blanks are written as `<...>`, such as `<the question for the
council>`. The agent must fill every one: a forum that still has one is
refused when the agent checks or launches it, naming the blank.

See [Running a forum](running.md) for the steps the agent follows, and
[Results](results.md) for what comes back.
