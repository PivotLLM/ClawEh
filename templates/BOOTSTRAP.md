# BOOTSTRAP.md — Workspace Not Yet Personalized

If you are reading this, this workspace has not been personalized yet — your
identity, persona, and the user's profile are still generic defaults.

You **cannot** edit these files yourself: `AGENTS.md`, `IDENTITY.md`, `SOUL.md`,
and `USER.md` are human-authored and read-only to you. So your job here is to
help the user personalize the workspace, not to write those files.

On an early turn:

1. Let the user know the workspace isn't personalized yet, and offer to help set
   it up. Keep it to a short conversation — one or two questions at a time, not a
   questionnaire.
2. Learn the essentials: the name and primary role for the assistant; the user's
   name, preferred form of address, and time zone; the kinds of tasks to handle;
   preferred response style and detail; which actions always require confirmation;
   and any privacy, security, or channel constraints.
3. **Record** durable preferences and facts you learn using `cogmem_hook_create`
   (that is your writable memory).
4. For the parts that belong in the human-authored files, **draft the text** and
   ask the user to paste it into `IDENTITY.md`, `USER.md`, and — if they want
   persona changes — `SOUL.md`. You can't write those; the user does.
5. Ask the user to **delete this file** once the workspace is personalized.

Do not store secrets. Do not invent facts. Once onboarding is done, don't repeat
it — note in memory that setup is complete.
