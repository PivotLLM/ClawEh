# Memory system overview

Our memory system, called cogmem, gives each assistant a lightweight way to remember what matters across conversations instead of starting from scratch every time. It stores durable facts, preferences, rules, and ongoing project context such as current projects, recurring workflows, important instructions, preferred formats, and user preferences. Memories are grouped by topic (we call this a domain) so the assistant can keep global information separate from project-specific details.

Assistants start with one memory domain called `General` and can create, rename, and delete domains as required. A domain can be marked sticky (always added to the context), and a domain can carry hints about when it should be recalled. For example, a domain can be associated with keywords, so if a future message mentions “morning routine,” the assistant automatically sees the relevant routine instructions.

Memory domains can also be associated with specific tools. For example, using a calendar, email, weather, or project-management tool can pull in the right memories. This makes memory feel practical rather than passive: the assistant does not have to remember to look something up — relevant memories can surface on their own.

From a technical perspective, the assistant is not constantly reading its entire memory database. Instead, each time it responds, the system assembles a fresh bundle of relevant context: the always-on (sticky) memories, plus the topic- or tool-specific domains that match the current message or the tools just used. The assistant can also intentionally search memory if it suspects something relevant exists but was not automatically included.

In the background, the system also reviews the entire conversation over time. This lets it notice useful patterns, extract important details, refine existing memories, and preserve lessons learned even if the assistant did not explicitly save them in the moment. That background review helps the assistant improve gradually, without requiring every important fact to be manually filed as it happens.  

The reason for this design is to make assistants more useful, reliable, and personal over time without overwhelming them with irrelevant history. It helps them remember preferences, avoid repeating mistakes, resume ongoing work, and follow established workflows while keeping the system understandable and controllable.
