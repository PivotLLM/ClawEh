# Memory system overview

Our memory system, called cogmem, gives each assistant a lightweight way to remember what matters across conversations instead of starting from scratch every time. It stores durable facts, preferences, rules, and ongoing project context such as current projects, recurring workflows, important instructions, preferred formats, and user preferences. Memories are grouped by topic (we call this a domain) so the assistant can keep global information separate from project-specific details.

Assistants start with one memory domain called `General` and can create, rename, and delete domains as required. A domain can be marked sticky (always added to the context), and a domain can carry hints about when it should be recalled. For example, a domain can be associated with keywords, so if a future message mentions “morning routine,” the assistant automatically sees the relevant routine instructions.

Memory domains can also be associated with specific tools. For example, using a calendar, email, weather, or project-management tool can pull in the right memories. This makes memory feel practical rather than passive: the assistant does not have to remember to look something up — relevant memories can surface on their own.

A memory can also point at a markdown file instead of holding everything in its own text. Some reference material is simply too long to be a memory — a description of a writing voice, a house style guide, a detailed playbook. In that case the memory text says what the document is and when to use it, and `file` names the document (for example `files/voice.md`, or `maestro/style-guide.md` when the Maestro tree is mounted). Whenever that memory is in context, the **full contents of the file** are injected with it, read fresh each turn, so editing the file updates what the assistant knows without touching memory at all.

The pointer can be changed or removed later without disturbing the memory itself: the assistant can repoint a memory at a different document, or detach the document entirely, keeping the memory's id and history. (Memory *text* remains immutable by design — to change what a memory says, it is retired and re-created.)

Attached files are subject to the assistant's ordinary file permissions: the path must be one it could read with its file tools, so a memory cannot be used to smuggle in a file the assistant is not allowed to see. Pointers are validated when the memory is created, so a bad path fails immediately rather than quietly degrading later conversations. Only markdown (`.md` / `.markdown`) may be attached.

Because attached documents can be large, they have their own budget, separate from the ordinary memory-context limit: by default up to 256 KB per document and 512 KB across all documents in a single turn (`memory.prompt.file_max_bytes` and `memory.prompt.file_total_max_bytes`). These are sized so real reference documents load whole; if one ever exceeds them, the assistant is told explicitly that it is seeing a truncated prefix rather than being left to assume it has the whole thing. A document attached to a sticky memory is present in every turn; attaching it to a keyword- or tool-triggered domain instead means it loads only when the topic actually comes up, which is usually what you want for a large document.

Each document appears once, under its own heading in an attached-documents section, with the file's contents immediately below it:

```
### Attached: files/voice.md
From memory h2QHR0 ("Write in my voice."), 34392 bytes, current as of this turn.

<full file contents>
```

The memory itself carries no filename — a path sitting next to a memory, far above the text it names, reads as a citation rather than an inclusion, which is exactly the wrong impression. The path appears where the content is.

## Memory types

Every memory has exactly one type, and the type is the only classification the assistant states — it decides what a memory *is*, and for one of them, whether it is ever in the prompt at all.

Four types are standing knowledge and load into context normally:

- **fact** — something true, and still true next month.
- **preference** — how you like things done.
- **rule** — a hard directive governing the assistant's output or behaviour toward you.
- **operational** — the assistant's own housekeeping: where it files things, how it works, a procedure it follows. The difference from a rule is who it serves. "Do not use the word thuddy" is a rule; "outline beats live in files/, not in memory" is operational.

The fifth is different:

- **event** — something that happened at a point in time, or a status as of a date: a trip, a delivery, a scheduled run. **Event memories are never loaded into the prompt.** Each domain reports how many it holds (`42 event memories here — cogmem_memory_search with include_events:true to read them`) and that is how the assistant reaches them.

That distinction is the one that does real work. Anything time-stamped goes stale immediately and accumulates without bound — a scheduled job writing one note per run produces hundreds of near-identical lines — and as a `fact` every one of them would sit in every prompt forever. As an `event` they stay searchable without crowding out the standing knowledge.

Memory lines carry their type, and their origin when it is worth knowing:

```
- (rule) Do not use the word "thuddy."
- (fact) Home is Ottawa. [origin: user]
```

`[origin: user]` means you wrote that memory by hand in the WebUI, rather than the assistant recording or inferring it. It is the one piece of provenance that is verifiable rather than self-reported, and the assistant is told to weight it accordingly.

## Curating memory

The memory page in the WebUI is where you correct what the assistant filed. The assistant picks a type when it writes and gets it wrong often enough — a trip log recorded as a fact, its own bookkeeping recorded as a rule — that fixing it has to be practical:

- **Change a memory's type** from the dropdown on its row.
- **Retire** a memory to take it out of use, or **restore** one you retired. Retired memories are hidden until you turn on "show retired".
- **Select many rows** and retype, retire or delete them together. This is not a nicety: clearing several hundred recurring notes one row at a time is not something anybody finishes.
- **Add a memory or a domain** by hand. What you add is recorded with `origin: user`.

Memory text stays immutable — to change what a memory says, retire it and write a new one. That keeps the audit trail honest.

## Export and import

The whole store exports as a YAML document holding every domain and memory with all their fields, from the memory page or from the assistant's own `cogmem_export` tool (which writes `files/MEMORY_EXPORT.yaml`). It is YAML rather than JSON because memory text is prose: a literal block stays readable and editable in a text editor, where JSON would collapse each memory onto one escaped line.

The dump can be read back. Importing offers two modes: **merge** adds what is missing and leaves everything else alone, so importing the same document twice changes nothing and an import can never destroy anything; **replace** empties the store first, which makes a dump a true restore point and is why it has to be chosen deliberately. Identifiers are re-minted on the way in, so a dump can also be loaded into a *different* assistant — one way to seed a new one from an existing one's domains.

Separately, the database is snapshotted automatically before any schema migration, to `<name>.pre-v<N>.db` beside itself, so an upgrade is recoverable without you having prepared for it.

## Tuning consolidation per agent

Each agent workspace holds a `COGMEM.md` for instructions specific to that assistant. Whatever you write there is **added to** the built-in consolidation prompt, which owns the rules and the output format and is not editable. Use it to say what this assistant should and should not record — subjects to leave alone, topics worth more detail, recurring output not worth recording unless something changed. It takes effect on the next consolidation run; an empty file changes nothing.

The prompt itself is deliberately not overridable. It contains the output schema every response is validated against, so a workspace copy would freeze that contract at whatever version the workspace happened to be seeded with — and a later change to it would then reach no existing agent. A file that is a copy of the built-in prompt (from a version of ClawEh where it was overridable) is ignored, with a warning naming it; reduce it to your own instructions, or delete it.

Memory belongs to the assistant, not to the channel you reach it through. In the default `unified` session mode there is one Amber: what she learns from your phone, from Slack, from a paired device like a Rabbit R1, or from an external integration driving her tools all goes into the same memory, and she can draw on any of it wherever you next speak to her. If you want an assistant with genuinely separate memory, create a separate agent (and turn cognitive memory off entirely for an agent that should not accumulate any). The isolating session modes (`per-user`, `per-platform`, `per-account`) divide memory by person or platform instead; see the session-scope section of the README.

From a technical perspective, the assistant is not constantly reading its entire memory database. Instead, each time it responds, the system assembles a fresh bundle of relevant context: the always-on (sticky) memories, plus the topic- or tool-specific domains that match the current message or the tools just used. The assistant can also intentionally search memory if it suspects something relevant exists but was not automatically included.

In the background, the system also reviews the entire conversation over time. This lets it notice useful patterns, extract important details, refine existing memories, and preserve lessons learned even if the assistant did not explicitly save them in the moment. That background review helps the assistant improve gradually, without requiring every important fact to be manually filed as it happens. What it writes is subject to the same rules as anything the assistant records deliberately, and shows up on the memory page tagged `consolidation` so you can tell it apart from what the assistant chose to save in the moment.

The reason for this design is to make assistants more useful, reliable, and personal over time without overwhelming them with irrelevant history. It helps them remember preferences, avoid repeating mistakes, resume ongoing work, and follow established workflows while keeping the system understandable and controllable.
