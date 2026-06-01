# Compression profile

This file tunes how this agent's conversation is summarized when context is
compacted. Its contents are appended to the base summarization instructions.
Edit it for this agent's role; delete it to fall back to the base behavior only.

## What to preserve

The summary COMPLEMENTS the always-present system prompt (identity, standing
rules, preferences, durable project state in CLAUDE.md / AGENTS.md / USER.md /
MEMORY.md). Do not restate anything already there. Capture the transient,
in-flight state that would be lost if the conversation were cleared right now:

- Active work and the branches / files / resources it touches.
- Dispatched or pending tasks and their IDs, and what they are waiting on.
- The last user instruction, and any open action items.
- Recent decisions and their rationale.

## How to cite and phrase

- Cite the specific message(s) that establish each item — a single seq or the
  tightest range. Never a broad span of the whole conversation.
- One fact per item. Split combined rules into separate constraints.

## Role notes

<!-- Add role-specific guidance here, e.g. "This agent manages software
projects: always preserve active PRs, branch names, and worker dispatch IDs." -->
