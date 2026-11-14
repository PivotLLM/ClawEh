# Classifiers

A classifier sorts the items one of an agent's tools can list, such as email messages, files or tickets. On every run it lists the items, skips the ones it has already handled, reads each new one, asks a model which category it belongs to, and runs the action set for that category.

Each item costs one model call. **The model that classifies never has tools**: it is shown the item and the categories and can only answer with a category. Listing, reading and acting are done by ClawEh, with the agent's own tools.

## Turn it on

On the **Agents** page, on the agent's card:

1. Turn on **Allow classifiers**. The agent gets the `classifier_*` tools and its classifiers start running.
2. Pick the **Classifier model**. The list holds the models in **Classification** on the [Task models](../providers/task-models.md) page, CLI models included. Turning the switch on picks the first one; while that list is empty, the switch is refused until you add a model to it.

When you turn the switch on and no model is set, the agent's first model is written in. It stays that model until you pick another, even if the agent's models change. Every classifier the agent owns uses this one model; agents cannot change it.

- There is no fallback. When the model fails or is in cooldown, the items are left for the next run.
- A CLI model runs in an empty folder of its own and never with **Allow CLI to bypass restrictions**, whatever the provider says.
- A model that is no longer one of the agent's models is marked on the card and on [Check Up](#check-up), and the agent's classifiers fail until you pick another.

Turning the switch off stops the agent's classifiers and removes its classifier tools. Its classifiers are kept and start again when you turn it back on. An agent that [represents a person](./human-agents.md) cannot have classifiers.

## Create one

Ask the agent. For example, to Alice:

> Every two minutes, sort my inbox into urgent, normal and low. Mail from bob@example.com is from a client and is always urgent. Move urgent mail to Priority/Urgent and low mail to Priority/Low.

Alice reads the format with `classifier_readme`, writes the definition and saves it with `classifier_create`. To change the rules later, tell her ("newsletters are always low"); she edits the rule files (see [Rules in Markdown](#rules-in-markdown)) or updates the definition with `classifier_update`.

A classifier belongs to the agent that created it and runs as that agent: the tools it lists, reads and acts with must be tools the agent has. This is checked when the definition is saved and again on every run. A tool that needs a conversation, and the classifier tools themselves, cannot be used.

A new or changed classifier runs as soon as it is saved, unless `dry_run` is set.

### How the agent sets one up

The agent's guide tells it to:

1. Agree the categories, actions and interval with you, and ask how often to run if you have not said. Every run calls the source tool, so it uses minutes, not seconds (five minutes for email unless you say otherwise).
2. Start in [dry run](#dry-run) unless you ask for it to act at once, and tell you that decisions are kept for 72 hours by default.
3. When you ask how it is doing, review the activity log and summarise what went where.
4. Turn your corrections into rules, kept as Markdown files under `files/`, and check a message with `classifier_test`.
5. Switch to live only when you agree, warning you that everything still in the source is then classified and acted on.

## Where they live

Each classifier is one file, `classifiers/<id>.json` in the agent's workspace, beside `classifiers/state.db`, which records what has been handled. The agent's file tools can read the folder but not write it, so every change goes through the classifier tools and their checks.

You can edit a definition file by hand. ClawEh reads it again when it changes and checks it the same way. A file that fails the checks is not run, is logged once, is reported to the agent like a [failed run](#failed-runs), and is marked on Check Up; it is never deleted.

When `classifiers/state.db` can't be opened, none of the agent's classifiers runs. ClawEh logs an error, raises an [alert](../admin/alerts.md), tells the agent and your chat ("Classifiers can't run: their state could not be opened."), once a day while it lasts (a restart can repeat it once), and marks it on Check Up, with the error the service last met.

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
| `classify` | The instructions, the rules (applied in order) and the categories. `unclassified` is reserved. `instructions_file` and `instructions_dir` add Markdown from the agent's `files/` (see [Rules in Markdown](#rules-in-markdown)); `instructions` may then be empty. |
| `actions` | The tool run for each category. A category with no action is only recorded. |
| `limits` | `items_per_run`: items taken per run, the rest wait for the next. `parallel`: items handled at the same time. `stop_after_failures`: consecutive failures that stop a run. |
| `report.summary` | `on_problems` (default): a summary to the agent's default chat when items failed or were unclassified, or the run failed. `always`: after every run that handled items or failed. `never`. A failed run is posted as [Failed runs](#failed-runs) says. |
| `report.errors` | `true` (default): failed runs are reported to the agent and your chat. `false`: they are only logged. |
| `activity_hours` | How long the activity log is kept. |

ClawEh writes `owner`, `created_at` and `updated_at`.

### Rules in Markdown

A classifier can keep its guidance and rules in Markdown files in the agent's `files/` folder, so the agent changes a rule by editing a file with its file tools instead of rewriting the definition. The definition keeps the source, fetch, categories and actions.

| Key | What it is |
|---|---|
| `classify.instructions_file` | One `.md` file with the overall guidance, such as `files/classifiers/inbox-priority/guide.md`. It is added under a `## <file name>` heading. |
| `classify.instructions_dir` | A folder, such as `files/classifiers/inbox-priority/rules`. Every `.md` file directly in it is added under a `## <file name>` heading. Sub-folders, names starting with `.` and anything that is not a regular file are skipped. |

Recommended layout, one short file per rule (for example one per sender domain):

```
files/classifiers/inbox-priority/
  guide.md
  rules/
    00-example.com.md
    10-news.example.org.md
```

- Folder files load in alphabetical order of their names. To control the order, you can start names with zero-padded numbers such as `00-`, `01-`, … `33-`.
- The model gets, in order: `instructions` (under `## instructions`), the file, the folder's files, the numbered `rules`, then the categories.
- Both paths are relative to the workspace and must be inside `files/`; an absolute path or one leading out with `..` is refused when the definition is saved. The files are read with the agent's own file permission: the agent needs `file_read_lines` or `file_read_bytes`, and a link leading out of `files/` is refused.
- The files are read on every run, so an edit applies on the next run. Items already handled are not reprocessed.
- Each file may have up to 256 KiB and the folder's files 1 MiB together; the text, with `instructions` and `rules`, counts toward `max_prompt_chars`.
- A save accepts files that do not exist yet, but refuses ones that exist and cannot be read or are over `max_prompt_chars`.
- When a file or folder is missing or cannot be read, or the text is over `max_prompt_chars`, the run classifies nothing and ends in error, naming the path: in `classifier_status`, on the Check Up **Classifiers** table and, as `report.summary` says, in the summary.
- `classifier_test` shows the system prompt with the files loaded.

### Which rule decided

The model names the rules that decided each item, so the agent can see where to tune. Each source of rules has a label: `instructions` for the inline instructions, the file name of the instructions file and of each folder file (such as `050-domain-example.md`), and `rule N` for the numbered `rules`. The model answers with the category, a short reason and a line such as `Rules: 050-domain-example.md, rule 2`, or `Rules: none` when no rule applied. The reason and the Rules line are optional: an answer without them, or with a Rules line that cannot be read, is still classified.

- A cited label is matched to the rules sent with that item, in any case; a file name may be given without `.md`, and a numbered rule as `rule 3`, `rule #3`, `#3` or `3`. The activity log keeps the known labels (`rules`, or `["none"]`) and counts the others (`unknown_rules`).
- The citation is the model's own account, not proof. To confirm that a rule decided an item, the agent calls `classifier_test` with `exclude_rules`: it classifies the item twice, with all rules and without the ones named, and says whether the category changed (`changed`). That costs two model calls and stores nothing.
- `classifier_activity` with `rule` lists the items that cited a rule; `rule: "none"` lists those that cited none.
- `classifier_status` shows `rule_usage` over `activity_hours`: how many decisions cited each current rule, the rules never cited, how many cited none or an unknown label, and cited labels whose file is gone (`no_longer_present`). Rules never cited may be dead; many decisions citing none may need a new rule.

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
- An item none of whose `fetch.fields` is found in the fetch result (for example, the fetch tool answered with text, not JSON) fails; it is never classified or acted on blind.
- Results that ClawEh marks as external content for the model (tools of external MCP servers, the web tools) are read as the JSON they carry.
- Stopping ClawEh in the middle of a run is not a failure: what it interrupted is taken again at the next start.
- Changing the rules (in the definition or its Markdown files), categories or actions does not reprocess items already handled. `classifier_rerun` classifies one item again on purpose.
- Handled items are remembered for 30 days (`classifiers.processed_days`), so an item that comes back is not classified again.
- One run at a time per classifier.

### Failed runs

A run fails when it can't start or can't go on: its Markdown files are missing, unreadable or too long, the source tool fails, a tool it names is no longer the agent's, the agent has no usable classifier model, or `stop_after_failures` items failed in a row (the error names the cause they share; the last item's error is kept in the activity log). The items wait for the next run. A definition file that is no longer valid is reported the same way ("Classifier inbox is not run (...)"), once per broken version of the file, even across restarts.

Every failed run is written to the [activity log](#activity-log) and logged as a warning. Errors are cleaned of anything that looks like an API key or password first. Error notices:

- Your chat gets one line, unless `report.summary` is `never`: "Alice: Classifier inbox: the run failed (source mail_list: the tool failed)." The tool's own message is not in it; the agent and the activity log have it.
- The agent gets the same sentence in its conversation, then what the tool, provider or item said, marked as external data so the agent does not take it as instructions, and advice that fits the cause: fix the definition and check it with `classifier_test`; check the tool or model itself (credentials or an outage) before changing the classifier; tell you when a tool it names is gone, or when only you can fix it (the Task models page, the classifier's state). Its answer goes to its default chat (when it has none, the answer stays in the conversation).
- The first and second failed runs in a row are reported. The second adds, in your chat, "Further errors won't be reported for 24 hours." and tells the agent how to report them again (`classifier_update` with `{"report": {"errors": true}}`). Later failures are only logged until those 24 hours are over; the next failure after that is reported again.
- Any change the agent makes to the classifier with `classifier_update` reports errors again at once. A run that succeeds starts the count again; it does not end the 24 hours. A run that was stopped (you turned the classifier off, ClawEh stopped) changes nothing. The count and the 24 hours survive a restart.
- With `report.errors` set to `false`, failed runs are only logged.
- Failed items (a fetch, the model or an action failing on one item) are not failed runs: they are in the usual summary, until `stop_after_failures` of them in a row fail the run.
- A model in cooldown is not a failure to fix: the run stops, its items wait, and only the usual summary is posted.

`classifier_status` shows `errors_reported`, `failed_runs` (failed runs in a row) and, while muted, `errors_muted_until`.

### Dry run

With `dry_run` on, the classifier decides but runs no action; the log shows the action and the arguments it would have been given. Dry-run decisions are kept apart: when you switch to live, every item is classified again with the current rules.

### Activity log

Every item handled (live or dry run) is logged with the fields sent to the model (each cut to 500 characters), the category, the reason, the rules the model cited, the action and its outcome (`acted`, `recorded`, `unclassified` or `failed`). An entry also shows what went wrong: the model's two answers of an `unclassified` item (`replies`), fetch fields not found (`missing_fields`) or cut to `fetch.max_chars` (`shortened_fields`), the arguments of an action that failed (`action_args`), and, when the fetch failed, the listed item's own fields. Every failed run is logged too, as `run_failed` with its error and no item; the same error in a row is one entry with `count`, the first time (`at`) and the latest (`last_at`). Entries are kept `activity_hours` (a `run_failed` entry from its latest time) and marked when they come from a dry run. Ask the agent ("what did the inbox classifier do with mail from bob@example.com?", "why did the inbox classifier fail?"); it reads the log with `classifier_activity`, which can filter by `outcome` or by cited `rule` and search the text, errors included. Nothing is sent to the agent unless you ask.

## Tools

| Tool | What it does |
|---|---|
| `classifier_readme` | The format, paths, placeholders and the install's limits. |
| `classifier_list`, `classifier_get` | The agent's classifiers; one definition. |
| `classifier_create`, `classifier_update`, `classifier_delete` | Save, change (a JSON merge patch) or remove a classifier. A save checks the definition and calls the source tool once. |
| `classifier_validate` | The same checks, without saving. |
| `classifier_status` | Last run, counts, recent decisions, the last error, whether error notices are on, off or muted, and how often each rule was cited (`rule_usage`). |
| `classifier_test` | Classifies one item without acting or storing anything, and shows the system prompt the model got. With `exclude_rules`, also classifies it without those rules and says whether the category changed. |
| `classifier_rerun` | Classifies one item again and runs its action. |
| `classifier_activity` | The activity log, newest first, with `limit`, `offset`, `search`, `outcome` and `rule`. |

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
| `max_prompt_chars` | 20000 | Characters of instructions and rules together, with the Markdown files (checked on every run). |
| `max_item_chars` | 50000 | `fetch.max_chars`. |
| `max_items_per_run` | 500 | `limits.items_per_run`. |
| `max_parallel` | 8 | `limits.parallel`. |
| `max_activity_hours` | 720 | `activity_hours`. |
| `min_every_seconds` | 15 | The shortest `trigger.every_seconds`. |

Absent or 0 uses the default; a negative value is refused. A definition above a limit, or running more often than `min_every_seconds`, is refused, naming the key and the limit. A change applies to the next save and run.

A definition that leaves its own settings out uses: `items_per_run` = `max_items_per_run`, `parallel` 1, `stop_after_failures` 5, `fetch.max_chars` = `max_item_chars`, `activity_hours` 72.

## Check Up

[Check Up](../admin/checkup.md) shows:

- **Classifier model (agent)**: none set, or not in Classification (the row says so when the Classification list is empty).
- **Classifier (agent: id)**: a definition file that is not run, with the reason.
- **Classifier state (agent)**: `state.db` can't be opened, so none of the agent's classifiers runs.

The full report lists, per agent, **Allow classifiers** and **Classifier model**, and a **Classifiers** table: each classifier with enabled, dry run, model, last run, its counts, last error and error notices (`on`, `off`, or `muted until` a time). Invalid files, files owned by another agent and a `state.db` that can't be opened are marked.

## Audit and backup

Every create, change and delete is recorded in the [audit log](../admin/audit.md) as **Classifier change**, and every item handled as **Classification**. The `classifiers/` folders are in the [nightly backup](../admin/backup.md).
