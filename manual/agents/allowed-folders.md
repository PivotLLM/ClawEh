# Files and allowed folders

An agent's file tools work inside its own folder, plus any outside folders you allow for it and the shared common folder.

These rules apply to the file tools (`file_read_lines`, `file_write`, `file_list` and the rest), which the agent has only if you [grant them](./tools-and-skills.md). They also decide which files the agent can send with `msg_send_file`, attach to its memory, use as a forum source or hand to a task model. Shell commands are not confined by them: an agent allowed shell commands can reach anything the ClawEh service account can.

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

The agent refers to these folders by name, for example `files/report.md`: a relative path starts at the workspace. It makes folders with `file_mkdir`, removes them with `file_rmdir`, and moves or renames them with `file_move`; it cannot remove `files/` itself or an allowed folder.

### Install-wide file settings

On the **System** page, in **Agent**:

- **Restrict to workspace** (on by default) keeps file tools inside the workspace and the allowed folders. Turning it off lets file tools read and write anything the service account can, and [Check Up](../admin/checkup.md) marks it. It applies to file tools only, not to shell commands.
- **Agent base directory** moves every agent's workspace to another location.
- **Common directory** sets where the [common folder](#the-common-folder) is.

## Allowed folders

An allowed folder gives one agent's file tools a folder elsewhere on the machine. The agent uses its real path, for example `/srv/src/notes.md`.

1. Open the agent's card on the **Agents** page and find **Allowed folders**.
2. Add a row and type the folder's absolute path, for example `/srv/src`.
3. Optionally type a **Note** saying what the folder holds, for example `inbound audio notes` (at most 100 characters).
4. Tick **Notify on new files** to tell the agent when a new file appears there (see below).

The trash icon removes a folder. A path that is not absolute is refused, for example "Alice's allowed folder "src" must be an absolute path.", and so is a note longer than 100 characters ("Alice's note for "/opt/audio/in" is longer than 100 characters."). A saved folder the file tools cannot use is marked on its row: "Ignored: not an absolute path" (left by an older configuration), "Ignored: path contains a line break" (a hand edit; such a path is refused on save) or "Ignored: folder not found"; Check Up lists it too.

ClawEh does not decide write access. The agent may read and write wherever the account ClawEh runs as may; in a folder that account cannot write, a write fails with "permission denied: \<path\> is read-only for this app". To give an agent a read-only folder, make it read-only for that account.

The agent's prompt lists each allowed folder by its path, marked "(read-only)" when the account cannot write it and followed by its note, for example `/opt/audio/in (read-only): inbound audio notes`. The same list names the workspace folders the file tools reach, by absolute path. With **Maestro** on, the folders are also in Maestro's reference library (see [Maestro](../maestro.md#what-maestro-can-read)).

Paths are checked after following symbolic links: a link inside an allowed folder that leads outside the workspace and every allowed folder is refused, and so is a link in `files/` to a part of the workspace the agent may not read, such as `sessions/` or `AGENTS.md`.

In `config.json`:

```json
{
  "id": "alice",
  "allowed_folders": [
    { "path": "/srv/src", "notify": true },
    { "path": "/opt/audio/in", "note": "inbound audio notes" }
  ]
}
```

### New-file notices

With **Notify on new files** ticked, ClawEh checks the folder every 10 seconds. When a new file appears, the agent gets a message naming the agent, the folder and the file, such as "New file in Alice's allowed folder /srv/src: /srv/src/sub/x.md", in its default channel, as if it had been sent there, and handles it like any other message. Files that are only changed do not count. The agent needs a **Default Channel** for this; without one the notice is skipped and logged.

ClawEh keeps the list of files each agent has seen in that agent's own folder (`state/folderwatch/` in its workspace), so a read-only folder gets notices too, and two agents watching the same folder are each told. The first check of a folder only records what is already there, and so does the first check after a folder is added again or its notices are turned back on. Versions before 0.6.4 kept this list in a `.claw` file in the folder; it is no longer used and can be deleted.

### Folders set as mounts before 0.6.4

Older versions gave an agent outside folders as *mounts*, each under a name (`notes/...`). At load each `mounts` entry becomes an allowed folder with the same path and notify setting; its name and its write setting are dropped, and the log has one warning per entry naming the agent and the path. The next save writes only `allowed_folders`. Tell the agent to use the real path instead of the old name, and set file permissions where a mount used to be read-only.

When **Maestro** is on, `maestro/` is simply part of the workspace; there is no automatic mount any more.

## The common folder

The common folder is shared by every agent that has **Share common directory** on (the default). Agents reach it with the `common_*` tools (`common_list`, `common_get`, `common_put`, `common_delete`), which must also be granted in the agent's tools; **Default tools** leaves out `common_delete`. They can copy files between it and their own folders.

By default the common folder is `common` in the data directory (`~/.claw/common`). Change it with **Common directory** on the System page.

Turn **Share common directory** off for an agent that must not see what other agents share.

## Sending files to the chat

With `msg_send_file` granted, the agent sends up to 10 files at a time to the chat it is answering, where its reply goes. It can send only files it is allowed to read: it needs `file_read_lines` or `file_read_bytes` too, and the rules above apply. Each file must be within `agents.defaults.max_media_size` (20 MB by default).

- Images go as pictures where the chat shows them (Telegram); other files, Markdown included, go as files, unchanged.
- Telegram, Slack, Discord and Matrix chats can receive files. The WebUI chat, devices and SecMsg cannot; the agent is told so.
- The agent learns, for each file, whether it was sent or why not.
- Files are never sent to another agent: a request answering another agent, or a sub-agent's task, is refused.
- A turn with no chat (`claw agent`, a service token) cannot send files.
- A Slack app needs the `files:write` scope to send files (see [Slack](../channels/slack.md)).

## Images the model generates

Some models return images with their reply (OpenRouter image models, OpenAI image generation). Each image is saved in the agent's workspace under `files/generated/<date>/`, and then:

- In a chat that can receive files, it is sent to that chat, like a file sent with `msg_send_file`.
- In a reply to another agent (a question, a forum, a sub-agent's task), in a chat that can't receive files, or when it is larger than `agents.defaults.max_media_size`, it is not sent; the reply gives its path instead. A reply to another agent names whose workspace the image is in.
- A temporary agent made from scratch keeps no image when it answers another agent or has no chat, since its workspace is deleted with it; its reply says so.
- An image the model gives as a link is shown in the reply as "Image: <link>", cut to 500 characters. It is never downloaded or saved.

The agent is told where each image was saved, so it can refer to it later or send it again with `msg_send_file`. A clone saves in its source agent's workspace.
