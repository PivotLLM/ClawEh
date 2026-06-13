<!--
COMPRESSION.md — optional, per-agent summarization profile.

Any text in this file OUTSIDE this HTML comment is appended to the built-in
summarization instructions when this agent's context is compacted. HTML comments
are stripped, so a comment-only file (like this one) adds nothing and costs no
tokens. Leave it comment-only — or delete it — to use the built-in behavior.

The summarizer must always return the built-in JSON summary schema, so write
guidance about WHAT to preserve, not about changing the output format. Anything
the profile asks to keep that does not fit an existing field goes into the
"notes" field.

Add ONLY guidance unique to this agent's role, for example:

  This agent manages software projects: always preserve active PR numbers,
  branch names, and worker dispatch IDs.
-->
