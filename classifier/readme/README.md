# Classifiers

A classifier sorts the items one of your tools can list, such as email
messages, files or tickets. On every run it lists the items, skips those it
has already handled, reads each new one, asks a model one question with a
fixed set of categories, and runs the action you named for the answer. The
model never calls tools: it only picks a category. One model call per item.

Every classifier you create belongs to you and runs as you: the source,
fetch and action tools must be tools you have. They can't be classifier
tools (`classifier_*`). Its model is the classifier model the operator set
for you; a definition does not name one.

## Recommended workflow

1. **Agree the setup with the user.** Confirm the categories, the action for
   each, and how often to run. If the user has not said how fresh results
   must be, ask; do not guess a short interval. Every run calls the source
   tool even when nothing is new, so use minutes, not seconds: for email,
   300 (five minutes) is a sensible default, 60 the shortest worth using.
2. **Start in dry run.** Create the classifier with `"dry_run": true` unless
   the user asks for it to act at once. It then decides but moves nothing.
   Tell the user it is in dry run and that its decisions are kept for
   `activity_hours` (72 by default), so they should review it within that
   time or ask you to raise it.
3. **Review when asked.** `classifier_status` gives the last run, counts and
   recent decisions; `classifier_activity` gives each decision with its
   reason and the action it would have run (`action_args`), filtered by
   `search` (a sender, a subject word, a destination). Summarise for the
   user: what went where, and anything that looks wrong.
4. **Adjust.** Turn the user's corrections into rules: edit or add a rule
   file with your file tools (see "Rules in Markdown"), and check a specific
   item with `classifier_test` before relying on the change.
5. **Go live only when the user agrees.** Set `"dry_run": false`. Every item
   still in the source is then classified again with the current rules and
   acted on, so say so before switching.

## A definition

```json
{
  "id": "inbox-priority",
  "enabled": true,
  "dry_run": true,
  "trigger": { "every_seconds": 300 },
  "source": {
    "tool": "mail_list_messages",
    "args": { "folder": "Inbox", "top": 50 },
    "items": "messages",
    "id": "id"
  },
  "fetch": {
    "tool": "mail_read_message",
    "args": { "id": "{{item.id}}" },
    "fields": {
      "from": "from.address",
      "subject": "subject",
      "body": "body.content"
    },
    "html": ["body"],
    "max_chars": 12000
  },
  "classify": {
    "instructions": "Decide how important this email is to the user.",
    "rules": [
      "Email from bob@example.com is from a client and is always urgent.",
      "Newsletters are low unless they mention an invoice."
    ],
    "categories": [
      { "name": "urgent", "description": "Needs the user today." },
      { "name": "normal", "description": "Needs the user, not today." },
      { "name": "low", "description": "Newsletters, notifications, FYI." }
    ]
  },
  "actions": {
    "urgent": { "tool": "mail_move_message", "args": { "id": "{{item.id}}", "destination": "Priority/Urgent" } },
    "low": { "tool": "mail_move_message", "args": { "id": "{{item.id}}", "destination": "Priority/Low" } }
  },
  "limits": { "items_per_run": 50, "parallel": 4, "stop_after_failures": 5 },
  "report": { "summary": "on_problems" }
}
```

- `trigger.every_seconds`: how often it runs, at least the install's
  `min_every_seconds` (listed at the end). A run that finds nothing new
  costs no model call, but it always calls the source tool (see the
  workflow above for choosing it).
- `source`: the tool that lists the items. `items` is the dot-path to the
  list in its result (`""` when the result is the list); `id` is the path,
  inside one element, to the item's stable ID.
- `fetch` (optional): the tool that reads one item. `fields` is what the
  model sees, by name; everything else is dropped. `html` names the fields
  whose HTML is reduced to text. The fields are cut to `max_chars` together,
  the longest first, and the model is told which were shortened. Without
  `fetch`, the model sees the list element's members.
- `classify`: the instructions, rules applied in order, and the categories.
  `unclassified` is reserved. `instructions_file` and `instructions_dir`
  add Markdown from `files/` (see below); `instructions` may then be empty.
- `actions`: the tool run for each category. A category with no action is
  only recorded.
- `limits`: items per run (the rest are taken next run), items handled at
  the same time (1 by default), and consecutive failures that stop a run (5
  by default).
- `report.summary`: `on_problems` (default) posts a summary to your chat
  when items failed or were unclassified, `always` after every run that
  handled items, `never` not at all.
- `activity_hours`: how long the activity log is kept (72 by default).

ClawEh writes `owner`, `created_at` and `updated_at`.

## Rules in Markdown

Keep the classification guidance and rules in Markdown under `files/`, and
keep the JSON for the source, fetch, categories and actions. Then you change
a rule by editing a file with your file tools, not the definition.

- `classify.instructions_file`: one `.md` file with the overall guidance,
  such as `files/classifiers/<id>/guide.md`.
- `classify.instructions_dir`: a folder, such as
  `files/classifiers/<id>/rules`. Every `.md` file directly in it is added
  under a `## <file name>` heading; sub-folders and names starting with `.`
  are skipped. Use one short file per rule, for example one per sender
  domain.
- Files load in alphabetical order of their names. To control the order,
  you can start names with zero-padded numbers such as `00-`, `01-`, …
  `33-`.
- The model gets, in order: `instructions`, the file, the folder's files,
  the numbered `rules`, then the categories.
- Both paths are relative to your workspace and must be inside `files/`;
  they are read with your own file permission, so you need
  `file_read_lines` or `file_read_bytes`. The files are read on every run, so an edit applies on the next run.
  Items already classified are not reprocessed.
- If a file or folder is missing or can't be read, or the text is over
  `max_prompt_chars` with `instructions` and `rules`, the run classifies
  nothing and its error says why (`classifier_status`).
- `classifier_test` shows the system prompt with the files loaded
  (`system_prompt`), so you can check your rules.

Example, for email:

```json
"classify": {
  "instructions_file": "files/classifiers/inbox-priority/guide.md",
  "instructions_dir": "files/classifiers/inbox-priority/rules",
  "categories": [
    { "name": "urgent" }, { "name": "normal" }, { "name": "low" }
  ]
}
```

`files/classifiers/inbox-priority/rules/00-example.com.md`:

```markdown
Email from example.com is from a client: urgent.
```

`files/classifiers/inbox-priority/rules/10-news.example.org.md`:

```markdown
Email from news.example.org is a newsletter: low, unless it mentions an invoice.
```

## Paths

A dot-path names a value in a tool's JSON result: `subject`,
`from.address`. When a path meets a list, the rest of the path is applied to
every element.

## Substitution

An argument value of `fetch.args` or `actions.<category>.args` may be
exactly one placeholder, replaced by what it names with its JSON type:

- `{{item.<path>}}`: the path inside the item's list element.
- `{{fetched.<name>}}`: a `fetch.fields` value (actions only).
- `{{category}}`: the category the model chose (actions only).

A placeholder must be the whole value: `"{{item.id}}"`, never
`"id={{item.id}}"`. `source.args` takes no placeholder. A placeholder that
resolves to nothing fails that item; nothing empty is passed to a tool.

## Runs and state

- An item is done once its action succeeds. A failed item is tried again on
  the next run; if the model had already answered, its first answer is
  reused. An unclassified item is not tried again.
- Changing the rules (in the definition or in its Markdown files),
  categories or actions does not reprocess items already handled. Use `classifier_rerun` to reclassify one item on purpose.
- `dry_run` records decisions without running actions, kept apart from the
  live state: switching to live classifies everything again. Its activity
  entries show each action's arguments (`action_args`) after substitution.
- When the model cannot be used (none set, or in cooldown), the run stops and
  its items are tried again on the next run.

## Tools

- `classifier_create`, `classifier_update` (a JSON merge patch: objects
  merge, null deletes a key, arrays are replaced whole), `classifier_delete`.
  A create or update is checked against your tools and calls the source
  tool once.
- `classifier_validate`: the same checks without saving.
- `classifier_list`, `classifier_get`, `classifier_status`.
- `classifier_test`: classify one item (or the first listed) without acting
  or storing anything; it shows the system prompt the model got.
- `classifier_rerun`: classify one item again and run its action.
- `classifier_activity`: the activity log, newest first, with `limit`,
  `offset` and `search`.
