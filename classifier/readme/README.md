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

## A definition

```json
{
  "id": "inbox-priority",
  "enabled": true,
  "dry_run": false,
  "trigger": { "every_seconds": 120 },
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

- `trigger.every_seconds`: how often it runs. A run that finds nothing new
  costs no model call.
- `source`: the tool that lists the items. `items` is the dot-path to the
  list in its result (`""` when the result is the list); `id` is the path,
  inside one element, to the item's stable ID.
- `fetch` (optional): the tool that reads one item. `fields` is what the
  model sees, by name; everything else is dropped. `html` names the fields
  whose HTML is reduced to text. The fields are cut to `max_chars` together,
  the longest first, and the model is told which were shortened. Without
  `fetch`, the model sees the list element's members.
- `classify`: the instructions, rules applied in order, and the categories.
  `unclassified` is reserved.
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
- Changing the rules, categories or actions does not reprocess items already
  handled. Use `classifier_rerun` to reclassify one item on purpose.
- `dry_run` records decisions without running actions, kept apart from the
  live state: switching to live classifies everything again.
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
  or storing anything.
- `classifier_rerun`: classify one item again and run its action.
- `classifier_activity`: the activity log, newest first, with `limit`,
  `offset` and `search`.
