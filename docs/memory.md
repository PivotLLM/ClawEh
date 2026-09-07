# Cognitive memory (cogmem)

Cogmem gives each assistant a way to remember what matters across conversations
instead of starting from scratch every time. It holds durable facts,
preferences, rules, the assistant's own working notes, and a searchable record
of things that happened. Memories are grouped by topic — a **domain** — so
global information stays separate from project detail.

Each session has its own SQLite database, `<session>.cogmem.db`, in the agent's
`sessions/` directory.

## Domains

An assistant starts with one domain, `General`, and can create, rename, archive
and delete domains as it goes.

A domain is either **sticky** — included in every prompt, for global rules and
standing facts — or a topic domain, loaded only when it is relevant. `General`
is sticky.

A topic domain can say when it should be recalled:

- **Keyword triggers** match words or phrases in the incoming message, including
  a scheduled (cron) message. Prefer multi-word phrases; a common word matches
  too often.
- **Tool triggers** match tool-name substrings, so using a calendar, email or
  project tool pulls in the memories about it. `mcp_<server>` covers a whole MCP
  server.

Failing an explicit trigger, domains are also matched loosely against the text
of the current message. The assistant does not have to remember to look
something up — relevant memories surface on their own — and it can always
search deliberately with `cogmem_memory_search`.

## Memory types

Every memory has exactly one type. It is the only classification the assistant
states, and it decides what a memory *is* — and for one of them, whether it is
ever in the prompt at all.

Four types are standing knowledge and load into context normally:

- **fact** — something true, and still true next month.
- **preference** — how you like things done.
- **rule** — a hard directive governing the assistant's output or behaviour
  toward you.
- **operational** — the assistant's own housekeeping: where it files things,
  how it works, a procedure it follows. The difference from a rule is who it
  serves. "Do not use the word thuddy" is a rule; "outline beats live in
  `files/`, not in memory" is operational.

The fifth is different:

- **event** — something that happened at a point in time, or a status as of a
  date: a trip, a delivery, a scheduled run. **Event memories are never loaded
  into the prompt.**

That distinction is the one that does real work. Anything time-stamped goes
stale immediately and accumulates without bound — a scheduled job writing one
note per run produces hundreds of near-identical lines — and as a `fact` every
one of them would sit in the prompt forever. Worse, domains are matched by how
many of their memories match, so hundreds of near-identical rows make a domain
win that contest on volume alone and crowd out the one that was actually
relevant.

Events avoid all of that. A domain reports how many it holds and how to read
them:

```
(42 event memories here — cogmem_memory_search with include_events:true to read them)
```

Search excludes events by default, so an ordinary lookup is not buried under
them — but when a search finds nothing else, it searches events anyway and says
so. Retrieval never depends on the assistant remembering to pass the flag.

## What the assistant sees

Memory lines carry their type, and their origin when it is worth knowing:

```
- (rule) Do not use the word "thuddy."
- (fact) Home is Ottawa. [origin: user]
```

**Origin** records where a memory came from, and unlike anything the model says
about itself it is verifiable:

- `chat` — the assistant wrote it deliberately during a conversation.
- `consolidation` — the background review wrote it.
- `user` — you wrote it by hand in the WebUI. The assistant is told this
  outranks its own inferences.

Each memory also carries a **confidence** in 0–1. Memories below
`memory.prompt.min_confidence` (default `0.65`) are left out of the prompt.

The assistant is not reading its whole database each turn. It gets the sticky
domains, plus the topic domains that match the current message or the tools just
used, up to `memory.prompt.top_k_domains` (default 3) and
`memory.prompt.max_chars` (default 4000).

## Attached documents

A memory can point at a markdown file instead of holding everything in its text.
Some reference material is too long to be a memory — a writing-voice
description, a house style guide, a detailed playbook. The memory text says what
the document is and when to use it, and `file` names it (`files/voice.md`, or
`maestro/style-guide.md` when the Maestro tree is mounted). Whenever that memory
is in context the **full contents** are injected with it, read fresh each turn,
so editing the file updates what the assistant knows without touching memory.

Each document appears once, under its own heading, with the contents below it:

```
### Attached: files/voice.md
From memory h2QHR0 ("Write in my voice."), 34392 bytes, current as of this turn.

<full file contents>
```

The memory line itself carries no filename. A path sitting next to a memory, far
above the text it names, reads as a citation rather than an inclusion, which is
the wrong impression; the path appears where the content is.

The pointer can be repointed or detached later without disturbing the memory's
id or history. Attachments obey the assistant's ordinary file permissions — the
path must be one it could read with its file tools — and only `.md` or
`.markdown` may be attached. A bad path fails when the memory is created rather
than quietly degrading later conversations.

Documents have their own budget, separate from the memory-context limit: by
default 256 KB per document and 512 KB across all documents in one turn
(`memory.prompt.file_max_bytes`, `memory.prompt.file_total_max_bytes`). If one
exceeds them the assistant is told it is seeing a truncated prefix rather than
left to assume it has the whole thing. Attaching a large document to a triggered
domain rather than a sticky one means it loads only when the topic comes up.

## Consolidation

In the background, a "sleep cycle" reviews the conversation and distils it into
memories — noticing patterns, extracting detail, and preserving things the
assistant did not think to save at the time. It runs on a message count, after
an idle period, and nightly (`memory.consolidation.*`), and reuses the agent's
configured summarization models.

It states a memory's **type** and nothing else. Status is not its to choose, and
there is no per-memory retention or provenance argument: giving it more fields
to reason about produced inconsistent ones. Every operation that writes text
must cite the messages that justify it, so a memory cannot be asserted without
something in the conversation behind it.

**It also tidies the domains it touches.** Where two memories say the same
thing it retires the weaker and keeps the clearer; where a newer memory
contradicts an older one it retires the older and records the resolution. This
happens whether or not the conversation raised the topic — nothing else ever
revisits a memory once written, so redundancy and stale contradictions would
otherwise accumulate indefinitely.

It **retires rather than rewrites**, deliberately. Several specific facts that
merely share a topic are worth more than one vague paragraph, so only memories
that genuinely say the same thing are collapsed.

To make that possible each memory it reviews carries `age_days` — how long ago
it was asserted — and they are listed oldest first. When two memories conflict
and nothing else separates them, the newer one is the current instruction.
Without that the assistant had no way to tell which of two opposing rules was
still in force, and correctly declined to guess.

Exact-duplicate text is also retired automatically, without involving the model.

## Retention

Two kinds of memory are deleted by age:

- **Events** after **30 days**. They stop being useful long before they stop
  accumulating.
- **Retired memories** 90 days after they were retired. Retiring takes a memory
  out of use but leaves the row, so a store that retires steadily would grow
  forever while showing nothing for it.

Both are overridable per agent on the Agents page — blank means the default,
`-1` keeps forever. An assistant running an hourly job wants a few days; one
that remembers your travel wants the full window or more.

**Nothing else is ever deleted by age.** A `fact`, `preference`, `rule` or
`operational` memory is permanent, so no retention policy can silently drop a
standing instruction. The sweep runs as part of consolidation and logs what it
removed.

## Curating memory

The memory page in the WebUI is where you correct what the assistant filed. It
picks a type when it writes and gets it wrong often enough — a trip log recorded
as a fact, its own bookkeeping recorded as a rule — that fixing it has to be
practical.

- **Change a memory's type** from the dropdown on its row.
- **Retire** a memory to take it out of use, or **restore** one you retired.
  Retired memories are hidden until you turn on "show retired".
- **Select a whole domain** from the checkbox in its header, or individual rows,
  then retype, retire, restore or delete them together. Correcting a domain that
  accumulated several hundred entries is the case this page exists for.
- **Add a memory or a domain** by hand. What you add is recorded with
  `origin: user`.

Memory *text* stays immutable — to change what a memory says, retire it and
write a new one. That keeps the audit trail honest.

## Export and import

The whole store exports as a YAML document holding every domain and memory with
all their fields, from the memory page or from the assistant's own
`cogmem_export` tool (which writes `files/MEMORY_EXPORT.yaml`). YAML rather than
JSON because memory text is prose: a literal block stays readable and editable,
where JSON collapses each memory onto one escaped line.

The dump can be read back. Import has two modes: **merge** adds what is missing
and leaves everything else alone, so importing the same document twice changes
nothing and an import can never destroy anything; **replace** empties the store
first, which makes a dump a true restore point and is why it must be chosen
deliberately. Identifiers are re-minted on the way in, so a dump can also be
loaded into a *different* assistant — one way to seed a new one from an existing
one's domains.

## Upgrades

Databases are migrated when the agent loads, at startup and on config reload —
not when each session next happens to be used. A schema change is therefore a
single event you can point at, and a database that cannot be migrated is
reported at startup rather than surfacing mid-conversation.

Before any migration the database is copied to `<name>.pre-v<N>.db` beside
itself, named for the version it came *from*, so an upgrade is recoverable
without having prepared for it. To roll one back: stop the service, put the
snapshot back in place, and revert the binary.

## Per-agent instructions

Each agent workspace holds a `COGMEM.md` for instructions specific to that
assistant. What you write there is **added to** the built-in consolidation
prompt. Use it to say what this assistant should and should not record —
subjects to leave alone, topics worth more detail, recurring output not worth
recording unless something actually changed. It takes effect on the next run,
and a file containing only the seeded comments changes nothing.

The prompt itself is deliberately not overridable. It contains the output schema
every response is validated against, so a workspace copy would freeze that
contract at whatever version the workspace was seeded with, and a later change
would reach no existing agent. A `COGMEM.md` that is a copy of the built-in
prompt (from a version where it *was* overridable) is ignored with a warning
naming it; reduce it to your own instructions, or delete it.

## Memory and channels

Memory belongs to the assistant, not to the channel you reach it through. In the
default `unified` session mode there is one Amber: what she learns from your
phone, from Slack, from a paired device like a Rabbit R1, or from an external
integration driving her tools all goes into the same memory, and she can draw on
any of it wherever you next speak to her.

For an assistant with genuinely separate memory, create a separate agent — and
set `cogmem: false` for one that should accumulate none. The isolating session
modes (`per-user`, `per-platform`, `per-account`) divide memory by person or
platform instead; see the session-scope section of the README.

## Configuration

Under `agents.defaults.memory`, overridable per agent:

| Key | Default | Meaning |
|---|---|---|
| `prompt.top_k_domains` | 3 | Topic domains pre-loaded per turn |
| `prompt.max_chars` | 4000 | Budget for the routed block |
| `prompt.min_confidence` | 0.65 | Memories below this are not loaded |
| `prompt.file_max_bytes` | 262144 | Per attached document |
| `prompt.file_total_max_bytes` | 524288 | All attachments in one turn |
| `retention.event_days` | 30 | Events deleted after this; `-1` never |
| `retention.retired_days` | 90 | Retired memories deleted this long after retirement; `-1` never |
| `consolidation.every_n_messages` | 50 | Run after this many new messages |
| `consolidation.idle_minutes` | 60 | Run after this long idle |
| `consolidation.nightly` / `nightly_at` | true / 03:20 | Nightly run |

Retention is also settable per agent on the Agents page, which writes
`event_retention_days` and `retired_retention_days` on the agent itself.
