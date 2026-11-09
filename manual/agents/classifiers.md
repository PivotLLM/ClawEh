# Classifiers

A classifier sorts the items one of an agent's tools can list, such as email messages, files or tickets. On every run it lists the items, skips the ones it has already handled, reads each new one, asks a model which category it belongs to, and runs the action set for that category.

Each item costs one model call. **The model that classifies never has tools**: it is shown the item and the categories and can only answer with a category. Listing, reading and acting are done by ClawEh, with the agent's own tools.

## Turn it on

On the **Agents** page, on the agent's card:

1. Turn on **Allow classifiers**. The agent gets the `classifier_*` tools and its classifiers start running.
2. Pick the **Classifier model**. The list holds the models ticked **Classify** on the [Task models](../providers/task-models.md) page, CLI models included. Turning the switch on picks the first one; with none, the switch is refused until you tick Classify for a model.

When you turn the switch on and no model is set, the agent's first model is written in. It stays that model until you pick another, even if the agent's models change. Every classifier the agent owns uses this one model; agents cannot change it.

- There is no fallback. When the model fails or is in cooldown, the items are left for the next run.
- A CLI model runs in an empty folder of its own and never with **Allow CLI to bypass restrictions**, whatever the provider says.
- A model that is no longer one of the agent's models is marked on the card and on [Check Up](#check-up), and the agent's classifiers fail until you pick another.

Turning the switch off stops the agent's classifiers and removes its classifier tools. Its classifiers are kept and start again when you turn it back on. An agent that [represents a person](./human-agents.md) cannot have classifiers.

## Create one

Ask the agent. For example, to Alice:

> Every two minutes, sort my inbox into urgent, normal and low. Mail from bob@example.com is from a client and is always urgent. Move urgent mail to Priority/Urgent and low mail to Priority/Low.

Alice reads the format with `classifier_readme`, writes the definition and saves it with `classifier_create`. To change the rules later, tell her ("newsletters are always low"); she updates the definition with `classifier_update`.

A classifier belongs to the agent that created it and runs as that agent: the tools it lists, reads and acts with must be tools the agent has. This is checked when the definition is saved and again on every run. A tool that needs a conversation, and the classifier tools themselves, cannot be used.

A new or changed classifier runs as soon as it is saved, unless `dry_run` is set.

### How the agent sets one up

The agent's guide tells it to:

1. Agree the categories, actions and interval with you, and ask how often to run if you have not said. Every run calls the source tool, so it uses minutes, not seconds (five minutes for email unless you say otherwise).
2. Start in [dry run](#dry-run) unless you ask for it to act at once, and tell you that decisions are kept for 72 hours by default.
3. When you ask how it is doing, review the activity log and summarise what went where.
4. Turn your corrections into rules and check a message with `classifier_test`.
5. Switch to live only when you agree, warning you that everything still in the source is then classified and acted on.

## Where they live

Each classifier is one file, `classifiers/<id>.json` in the agent's workspace, beside `classifiers/state.db`, which records what has been handled. The agent's file tools can read the folder but not write it, so every change goes through the classifier tools and their checks.

You can edit a definition file by hand. ClawEh reads it again when it changes and checks it the same way. A file that fails the checks is not run, is logged once, and is marked on Check Up; it is never deleted.

## The definition

<div v-pre>

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
    "fields": { "from": "from.address", "subject": "subject", "body": "body.content" },
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
  "limits": { "items_per_run": 50, "parallel": 1, "stop_after_failures": 5 },
  "report": { "summary": "on_problems" }
}
```

</div>

| Key | What it is |
|---|---|
| `id` | The classifier's id: 1 to 64 letters, digits, `-` and `_`, starting with a letter or digit. |
| `enabled` | Whether it runs. |
| `dry_run` | Records decisions without running actions (see [Dry run](#dry-run)). |
| `trigger.every_seconds` | How often it runs. A run that finds nothing new makes no model call. |
| `source` | The tool that lists the items. `items` is the path to the list in its result (`""` when the result is the list); `id` is the path, inside one item, to its stable id. |
| `fetch` | Optional: the tool that reads one item. `fields` names what the model sees; everything else is dropped. `html` lists the fields whose HTML is turned into text. The fields are cut to `max_chars` together, longest first, and the model is told which were shortened. Without `fetch`, the model sees the listed item. |
| `classify` | The instructions, the rules (applied in order) and the categories. `unclassified` is reserved. |
| `actions` | The tool run for each category. A category with no action is only recorded. |
| `limits` | `items_per_run`: items taken per run, the rest wait for the next. `parallel`: items handled at the same time. `stop_after_failures`: consecutive failures that stop a run. |
| `report.summary` | `on_problems` (default): a summary to the agent's default chat when items failed or were unclassified. `always`: after every run that handled items. `never`. |
| `activity_hours` | How long the activity log is kept. |

ClawEh writes `owner`, `created_at` and `updated_at`.

### Paths and placeholders

<div v-pre>

A path names a value in a tool's JSON result: `subject`, `from.address`. When a path meets a list, the rest of the path applies to every element.

An argument in `fetch.args` or `actions.<category>.args` may be exactly one placeholder, replaced by the value it names:

- `{{item.<path>}}`: a value of the listed item.
- `{{fetched.<name>}}`: one of the `fetch.fields` (actions only).
- `{{category}}`: the category the model chose (actions only).

A placeholder must be the whole value: `"{{item.id}}"`, never `"id={{item.id}}"`. `source.args` takes none. A placeholder that names nothing fails that item; an empty value is never passed to a tool.

</div>

## Runs

- An item is done once its action succeeds. A failed item is tried again on the next run, with the model's first answer if it already gave one. An answer that is not a category is asked again once, then the item is `unclassified` and left where it is.
- Changing the rules, categories or actions does not reprocess items already handled. `classifier_rerun` classifies one item again on purpose.
- Handled items are remembered for 30 days (`classifiers.processed_days`), so an item that comes back is not classified again.
- One run at a time per classifier.

### Dry run

With `dry_run` on, the classifier decides but runs no action; the log shows the action and the arguments it would have been given. Dry-run decisions are kept apart: when you switch to live, every item is classified again with the current rules.

### Activity log

Every item handled (live or dry run) is logged with the fields sent to the model (each cut to 500 characters), the category, the reason, the action and its outcome. Ask the agent ("what did the inbox classifier do with mail from bob@example.com?"); it reads the log with `classifier_activity`. Nothing is sent to the agent unless you ask.

## Tools

| Tool | What it does |
|---|---|
| `classifier_readme` | The format, paths, placeholders and the install's limits. |
| `classifier_list`, `classifier_get` | The agent's classifiers; one definition. |
| `classifier_create`, `classifier_update`, `classifier_delete` | Save, change (a JSON merge patch) or remove a classifier. A save checks the definition and calls the source tool once. |
| `classifier_validate` | The same checks, without saving. |
| `classifier_status` | Last run, counts, recent decisions and the last error. |
| `classifier_test` | Classifies one item without acting or storing anything. |
| `classifier_rerun` | Classifies one item again and runs its action. |
| `classifier_activity` | The activity log, newest first, with `limit`, `offset` and `search`. |

## Limits

Install-wide, set on the System page (**Classifiers**) or in `config.json`:

```json
"classifiers": {
  "processed_days": 30,
  "limits": {
    "max_rules": 100,
    "max_prompt_chars": 20000,
    "max_item_chars": 50000,
    "max_items_per_run": 500,
    "max_parallel": 8,
    "max_activity_hours": 720,
    "min_every_seconds": 15
  }
}
```

| Key | Default | Limits |
|---|---|---|
| `processed_days` | 30 | How long handled items are remembered. |
| `max_rules` | 100 | Rules in one definition. |
| `max_prompt_chars` | 20000 | Characters of instructions and rules together. |
| `max_item_chars` | 50000 | `fetch.max_chars`. |
| `max_items_per_run` | 500 | `limits.items_per_run`. |
| `max_parallel` | 8 | `limits.parallel`. |
| `max_activity_hours` | 720 | `activity_hours`. |
| `min_every_seconds` | 15 | The shortest `trigger.every_seconds`. |

Absent or 0 uses the default; a negative value is refused. A definition above a limit, or running more often than `min_every_seconds`, is refused, naming the key and the limit. A change applies to the next save and run.

A definition that leaves its own settings out uses: `items_per_run` = `max_items_per_run`, `parallel` 1, `stop_after_failures` 5, `fetch.max_chars` = `max_item_chars`, `activity_hours` 72.

## Check Up

[Check Up](../admin/checkup.md) shows:

- **Classifier model (agent)**: none set, or not ticked Classify (the row says so when no model is ticked Classify at all).
- **Classifier (agent: id)**: a definition file that is not run, with the reason.

The full report lists, per agent, **Allow classifiers** and **Classifier model**, and a **Classifiers** table: each classifier with enabled, dry run, model, last run, its counts and last error. Invalid files and files owned by another agent are marked.

## Audit and backup

Every create, change and delete is recorded in the [audit log](../admin/audit.md) as **Classifier change**, and every item handled as **Classification**. The `classifiers/` folders are in the [nightly backup](../admin/backup.md).
