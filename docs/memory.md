# Memory system overview

Our memory system, called cogmem, gives each assistant a lightweight way to remember what matters across conversations instead of starting from scratch every time. It stores durable facts, preferences, rules, and ongoing project context such as current projects, recurring workflows, important instructions, preferred formats, and user preferences. Memories are grouped by topic (we call this a domain) so the assistant can keep global information separate from project-specific details.

Assistants start with one memory domain called `General` and can create, rename, and delete domains as required. A domain can be marked sticky (always added to the context), and a domain can carry hints about when it should be recalled. For example, a domain can be associated with keywords, so if a future message mentions “morning routine,” the assistant automatically sees the relevant routine instructions.

Memory domains can also be associated with specific tools. For example, using a calendar, email, weather, or project-management tool can pull in the right memories. This makes memory feel practical rather than passive: the assistant does not have to remember to look something up — relevant memories can surface on their own.

A memory can also point at a markdown file instead of holding everything in its own text. Some reference material is simply too long to be a memory — a description of a writing voice, a house style guide, a detailed playbook. In that case the memory text says what the document is and when to use it, and `file` names the document (for example `files/voice.md`, or `maestro/style-guide.md` when the Maestro tree is mounted). Whenever that memory is in context, the **full contents of the file** are injected with it, read fresh each turn, so editing the file updates what the assistant knows without touching memory at all.

The pointer can be changed or removed later without disturbing the memory itself: the assistant can repoint a memory at a different document, or detach the document entirely, keeping the memory's id and history. (Memory *text* remains immutable by design — to change what a memory says, it is retired and re-created.)

Attached files are subject to the assistant's ordinary file permissions: the path must be one it could read with its file tools, so a memory cannot be used to smuggle in a file the assistant is not allowed to see. Pointers are validated when the memory is created, so a bad path fails immediately rather than quietly degrading later conversations. Only markdown (`.md` / `.markdown`) may be attached.

Because attached documents can be large, they have their own budget, separate from the ordinary memory-context limit: by default up to 256 KB per document and 512 KB across all documents in a single turn (`memory.prompt.file_max_bytes` and `memory.prompt.file_total_max_bytes`). These are sized so real reference documents load whole; if one ever exceeds them, the assistant is told explicitly that it is seeing a truncated prefix rather than being left to assume it has the whole thing. A document attached to a sticky memory is present in every turn; attaching it to a keyword- or tool-triggered domain instead means it loads only when the topic actually comes up, which is usually what you want for a large document. Unconfirmed (pending) memories name their document but do not load it until you confirm them.

Each document appears once, under its own heading in an attached-documents section, with the file's contents immediately below it:

```
### Attached: files/voice.md
From memory h2QHR0 ("Write in my voice."), 34392 bytes, current as of this turn.

<full file contents>
```

The memory itself carries no filename — a path sitting next to a memory, far above the text it names, reads as a citation rather than an inclusion, which is exactly the wrong impression. The path appears where the content is. A pending memory's document gets the same heading with a single line explaining that its contents are withheld until the memory is confirmed.

Memory belongs to the assistant, not to the channel you reach it through. In the default `unified` session mode there is one Amber: what she learns from your phone, from Slack, from a paired device like a Rabbit R1, or from an external integration driving her tools all goes into the same memory, and she can draw on any of it wherever you next speak to her. If you want an assistant with genuinely separate memory, create a separate agent (and turn cognitive memory off entirely for an agent that should not accumulate any). The isolating session modes (`per-user`, `per-platform`, `per-account`) divide memory by person or platform instead; see the session-scope section of the README.

From a technical perspective, the assistant is not constantly reading its entire memory database. Instead, each time it responds, the system assembles a fresh bundle of relevant context: the always-on (sticky) memories, plus the topic- or tool-specific domains that match the current message or the tools just used. The assistant can also intentionally search memory if it suspects something relevant exists but was not automatically included.

In the background, the system also reviews the entire conversation over time. This lets it notice useful patterns, extract important details, refine existing memories, and preserve lessons learned even if the assistant did not explicitly save them in the moment. That background review helps the assistant improve gradually, without requiring every important fact to be manually filed as it happens.  

The reason for this design is to make assistants more useful, reliable, and personal over time without overwhelming them with irrelevant history. It helps them remember preferences, avoid repeating mistakes, resume ongoing work, and follow established workflows while keeping the system understandable and controllable.
