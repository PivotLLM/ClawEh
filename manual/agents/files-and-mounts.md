# Files and mounts

An agent's file tools work inside its own folder, plus any external folders you mount for it and the shared common folder.

These rules apply to the file tools (`file_read_lines`, `file_write`, `file_list` and the rest), which the agent has only if you [grant them](./tools-and-skills.md). Shell commands are not confined by them: an agent allowed shell commands can reach anything the ClawEh service account can.

## The folders in a workspace

Each agent's workspace (see [Creating and editing agents](./creating.md#workspace)) holds these folders:

| Folder | What it holds | The agent may |
|---|---|---|
| `files/` | The agent's own working files | Read and write |
| `skills/` | Skills of this agent alone | Read |
| `tasks/` | Results of its [sub-agents](./subagents.md) | Read |
| `tmp/` | Attachments and voice transcriptions it received | Read |
| `forums/` | Its [forums](../forums/results.md#where-the-files-are) and their results | Read |
| `maestro/` | [Maestro](../maestro.md) projects and tasks, when **Maestro** is on | Read and write |
| `sessions/` | Its conversation | Not through file tools |
| `cogmem/` | Its long-term memory | Not through file tools |
| `state/` | Turns in progress and its message tokens | Not through file tools |

The instruction files at the top of the workspace (`AGENTS.md`, `SOUL.md` and the others) are already in the agent's prompt; it does not read or change them with file tools.

The agent refers to these folders by name, for example `files/report.md`.

### Install-wide file settings

On the **System** page, in **Agent**:

- **Restrict to workspace** (on by default) keeps file tools inside the workspace and mounts. Turning it off lets file tools read and write anything the service account can, and [Check Up](../admin/checkup.md) marks it.
- **Agent base directory** moves every agent's workspace to another location.
- **Common directory** sets where the [common folder](#the-common-folder) is.

## Mounts: external folders

A mount makes a folder elsewhere on the machine available to one agent under a name of your choice, beside `files/`. A mount named `notes` is reached as `notes/...`.

1. Open the agent's card on the **Agents** page and find **Mounts (external folders, beside files/)**.
2. Click **Add mount**.
3. Type the name (letters, digits and `-`, for example `notes`) and the absolute path (`/srv/shared/notes`).
4. Turn on **write** to let the agent change files there. Without it the mount is read-only.
5. Turn on **notify** to tell the agent when a new file appears there (see below).

The agent cannot leave the mounted folder with `..`. The trash icon removes a mount.

In `config.json`:

```json
{
  "id": "alice",
  "mounts": [
    { "name": "notes", "path": "/srv/shared/notes", "writable": true, "notify": true }
  ]
}
```

### New-file notices

With **notify** on, ClawEh checks the mounted folder every 10 seconds. When a new file appears, the agent gets the message "A new file is available in a mounted folder: \<path\>" in its default channel, as if it had been sent there, and handles it like any other message. Files that are only changed do not count. The agent needs a **Default Channel** for this; without one the notice is skipped and logged.

### Reserved names

A mount cannot use the name of a workspace folder, in any case: `files`, `skills`, `tasks`, `tmp`, `forums`, `maestro`, `sessions`, `cogmem`, `state`, `common`.

- Saving such a mount is refused, for example "Alice's mount "files" uses a reserved name; choose another name."
- A mount with such a name already in `config.json` is ignored until renamed. Its row on the card says `Ignored: "files" is a reserved name.`, and **Check Up** lists it.

When **Maestro** is on, its folder is mounted automatically as `maestro/`.

## The common folder

The common folder is shared by every agent that has **Share common directory** on (the default). Agents reach it with the `common_*` tools (`common_list`, `common_get`, `common_put`, `common_delete`), which must also be granted in the agent's tools. They can copy files between it and their own folders.

By default the common folder is `common` in the data directory (`~/.claw/common`). Change it with **Common directory** on the System page.

Turn **Share common directory** off for an agent that must not see what other agents share.
